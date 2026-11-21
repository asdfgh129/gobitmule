# ADR-0029 Telegram MTProto L1（WBS 15 技术交付）

- **状态**：Accepted（项目发起人 2026-11-21 全权授权按建议顺序执行 P3；代行五角色，非实名会签，依 `17` §1 P0 先例）
- **日期**：2026-11-21
- **决策者**：项目发起人（用户）指令「按建议顺序执行」
- **关联**：ADR-0009（gotd/td 选型）、ADR-0027（顺序提交原语）、`03` FR-SRC-04 / UC-05、`05` §7、`06` §6、`07` v0.16、`10` §4、`11` §3 / §6、`12` §1 / §3、`14` WBS 15、`docs/README`

## 背景

ADR-0009 已批准 `gotd/td`（MIT，纯 Go MTProto 2.0）作为 Telegram 实现
（进程内接入、`Stream(io.Writer)` 灌入自有 Transport、FLOOD_WAIT / DC 迁移 /
会话存储）。WBS 15 属 P3，本次落地。

## 勘察与裁决

**依赖足迹实测**（gotd/td v0.166.1，仅 import `telegram/downloader` + client 栈）：
**17 个链接模块**（gotd{td,ige,log,neo}、go-faster{errors,jx,xor}+segmentio/asm、
klauspost/compress、cenkalti/backoff、cespare/xxhash、coder/websocket、otel+metric、
uber/multierr、x/sync），全 MIT/BSD/Apache。连带升级共享依赖
（x/sync v0.22、x/sys v0.47、x/text v0.40、pion/webrtc 等，gates 复验）。
足迹与 lux 精选子集同量级，**接受 import**。

## 决策

1. **opt-in 配置**：`sources.telegram.{enabled,apiId,apiHash,timeout}`
   （默认关；apiId/apiHash 是 Telegram **APP 凭据**——应用标识而非用户密钥，
   `apiHash` 不经 `GET /config` 回显；**用户会话本体走 POST /credentials 进
   OS 钥匙串**，FR-SRC-06，绝不落 SQLite / 日志）。关闭时链路不变。
2. **链接解析离线化**：支持的形态——`https://t.me/<channel>/<id>`、
   `https://t.me/c/<internalID>/<id>`（私有）、`tg://resolve?domain=…&post=…`、
   `tg://privatepost?channel=…&post=…`；全部**归一化为内部镜像**
   `tg://ud/link|private?...`——无 http(s) 传输可声明该 scheme，分发无歧义，
   通用 SSRF Guard 不需要看见 Telegram DC 地址（MTProto 自有寻址）。
   `Match` 与 `ParseLink` 一致（仅 host 启发式会误吞无消息 ID 的预览链接）。
3. **可选 `MediaClient` seam**：`Stat`（消息 → 文档元数据）+ `Stream`
   （文档字节 → `io.Writer`，gotd `Stream(io.Writer)` 恰为 ADR-0009 的设计）。
   生产实现 `gotdMedia`：每操作开/关连接（Telegram 下载突发性强，daemon 不保持
   空闲 MTProto 连接）；`WithPartSize(512KiB)` + `WithAllowCDN(true)` 按 ADR-0009。
   测试注入假实现（`12` §1：CI 不触达真实 Telegram）。
4. **会话凭据化**：`session.Storage` 双方法接口以 `CredentialStore` 承载——
   `LoadSession` 从 `credentialRef` 读会话 blob；**L1 只读**（gotd 运行中的
   会话变更不回写，轮换需重新存储新 blob——如实记录）。缺失会话 →
   `E_CAPABILITY_UNAVAILABLE` 并指引用户先 POST /credentials。
5. **私有链接拒绝**：`t.me/c/<id>/<msg>` 需要账号侧的 channel access hash
   （dialogs 同步），L1 干净拒绝并指向 P3；仅公开频道 / 用户消息可下载。
6. **顺序提交契约复用**：gotd `Stream` 写入 io.Pipe，读侧经
   `tputil.NewChanReader` + `SequentialCommit` 进 chunk 计划——与 hls/dash 同一
   提交语义；逐字节过 Budget（限速权威唯一）；断点续传按首 pending chunk 的
   字节跳过（gotd `WithOffset` 精确续传为后续优化）。
7. **FLOOD_WAIT 归类**：`tgerr.AsFloodWait` → `E_TRANSIENT_NETWORK`
   （消息携带服务端给定的等待时长），符合 ADR-0009「按给定秒数退避而非暴力重试」。
8. **每操作开/关连接**：无长驻 MTProto 连接（下载突发性强）；连接生命周期由
   gotd `client.Run` 管理。

## 发现并修复

- **streamErr 双接收死锁**：`Run` 在 `return <-streamErr` 与延迟清理各接收一次
  writer 侧错误——第二次永久阻塞（集成测试超时抓出）。改为提交结束后单次接收。
- **tg:// 深链 host 解析**：`tg://resolve` 的 `resolve` 段被 url.Parse 归为 host
  而非 path——按 host+path 联合判定。
- **Match 启发式过宽**：仅按 host 判定会把无消息 ID 的预览链接放进链路再失败，
  改为 parse-as-match。

## 落地

| 文件 | 内容 |
|---|---|
| `internal/transport/tg/link.go` | 四形态链接解析、内部镜像归一化、用户名形状校验 |
| `internal/transport/tg/tg.go` | Transport（seam + 生产 gotdMedia：ResolveUsername → ChannelsGetMessages → Document → downloader.Stream）、FLOOD_WAIT 分类、会话凭据存储 |
| `internal/resolve/tg.go` | 链接分类、tg://ud 镜像归一化、credentialRef 透传 |
| `internal/config/config.go` | `sources.telegram.{enabled,apiId,apiHash,timeout}` + Validate |
| `internal/app/daemon.go` | opt-in 注册（resolver + transport） |

## 验证

- 链接解析 10+ 形态与拒绝路径；归一化往返；resolver 归一化 + credentialRef 透传。
- 传输：seam 假媒体 Probe/Run 经 chunk 计划逐字节一致 + Budget 计量；FLOOD_WAIT
  分类（服务端 42s 断言）；私有链接拒绝；无会话拒绝；非 tg 资源拒绝。
- **不宣称（NOT_EXECUTED）**：真实 MTProto 互通（2GB 文件 / DC 迁移 / 会话失效 /
  MTProxy——ADR-0009 验证清单）需真实凭据与网络，CI 禁止（`12` §1）；生产
  `gotdMedia` 已编译接线、真实互通待实验室凭据后补测（同 Windows 钥匙串先例）。

## 后果

- WBS 15 技术交付（L1）：链接 → 解析 → MTProto 下载骨架全链路成型，唯一缺口是
  真实网络验证（非代码缺口）。
- +16 链接模块（gotd 栈）进入 SBOM / 许可证 / govulncheck 门禁面（全宽松许可，
  门禁实测 PASS）；共享依赖 x/sync、x/sys、x/text、pion/webrtc 等随之升级。
- 解析链（opt-in 全开）：`… hls → dash → lux → telegram → yt-dlp → [plugin] …`；
  传输注册表新增 `telegram`（仅声明 tg://ud 镜像）。
