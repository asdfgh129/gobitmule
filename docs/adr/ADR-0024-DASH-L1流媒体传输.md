# ADR-0024 DASH L1 流媒体传输（WBS 13 收口）

- **状态**：Accepted（项目发起人 2026-11-20 全权授权按建议顺序推进开发；代行五角色，非实名会签，依 `17` §1 P0 先例）
- **日期**：2026-11-20
- **决策者**：项目发起人（用户）于 2026-11-19/20 指令「按建议顺序继续完成下一步开发计划」
- **关联**：ADR-0023（HLS 半边）、`03` FR-TRN-08、`05` §10 Resolver 链 / §9、`06` §3、`07` v0.12、`12` §3.3、`14` WBS 13、`16`、`04` 零 sidecar 基线

## 背景

ADR-0023 交付了 WBS 13 的 HLS 半边，DASH 明确留作同 WBS 后续包。DASH 与 HLS 的
差异在于：清单是 XML（MPD）、段寻址有三套（SegmentTemplate / SegmentList /
SegmentBase）、模板标识符带格式位（`$Number%05d$`）、init 段与媒体段分离、
音视频通常分属不同 AdaptationSet（分离流）。

## 决策

1. **仅静态 MPD（`type="static"`）**。`dynamic` 是 live 会话，与固定分片数 +
   崩溃恢复的存储模型冲突，与 HLS live 同口径拒绝。
2. **仅单 Period + 单 AdaptationSet**。分离音视频轨产生两条字节流，只有
   WBS 14 纯 Go Remux 能合成可播放文件；在此之前这类 MPD 拒绝并指向
   Remux（`E_CAPABILITY_UNAVAILABLE`），不静默下载无声视频。
3. **仅明文**。`<ContentProtection>`（DRM / CENC）拒绝——FR-TRN-08 的
   AES-128 条款由 HLS 半边交付；DASH 加密等价物是 CENC，与 Remux 耦合。
4. **支持 SegmentTemplate（`@duration` 与 SegmentTimeline 两种定界）与
   SegmentList；SegmentBase 拒绝**（需解析 sidx 索引箱，属另一个工作包）。
   模板属性按规范从 AdaptationSet 向 Representation 继承（§5.3.9.2.2）。
5. **选流策略与 HLS 一致**：`BANDWIDTH` 最高、并列取文档序。日历型时长
   （`P1Y` / `P1M`）拒绝，`PnD` / `PT…` 支持（段数学需要精确秒）。
6. **段数上限 20000、MPD 4MiB、单段 512MiB**，DTD / 实体声明拒绝（姿态同
   Metalink，ADR-0006）。
7. **提交流水线与 HLS 共享**：`tputil.SequentialCommit` /
   `tputil.NewChanReader` 由本次重构提炼——首个 chunk / 越界 / Release 语义
   全部顺序传输一处实现；字节信号量 + 有界 worker + 按序整理的段源两包
   各自持有（内容形态不同：HLS 需解密，DASH 需 init 段前置）。
8. **传输分发分区**：两个流传输都声明 `KindStream`，注册表按序取第一个
   声明者——为此引入**内部分发标记头** `X-Ud-Stream`（值 `hls` / `dash`）：
   Resolver 盖章、`resources.headers` 持久化（重启后分发仍正确）、两个传输
   在 `headerOf` 出网前剥离（`headerOf` 的单测断言标记不进任何请求）；
   无标记时按 URL 扩展名 `.m3u8` / `.mpd` 分派。
9. **强制路由在注册表集中实现**：`{"hls": true}` / `{"dash": true}` 任务选项
   让 `Registry.Resolve` 直呼对应 Resolver——`Resolver.Match` 的签名属冻结
   契约拿不到任务选项，两个流 Resolver 的 forced 分支原为死代码，本次修复。
10. **通用 HTTP 传输补 Kind 守卫**：其 `Supports` 原本对任何带 http(s) 镜像
    的资源都声明（Kind 盲），先注册的它会把 `.m3u8` / `.mpd` 任务当普通
    文件下载——由 2026-11-20 分发测试发现（此前单测用的资源无镜像、未暴露）。
    现仅声明 `single` / `multi`（兼容空 Kind fixture），流 / 种子 / ed2k
    Kind 一律不抢。

## 落地

| 文件 | 内容 |
|---|---|
| `internal/transport/dash/mpd.go` | MPD 解析、BaseURL 链、模板继承与展开（`$Number$` / `$Time$` / `%0Nd` / `$$`）、Timeline、SegmentList、ISO 8601 时长 |
| `internal/transport/dash/dash.go` | Transport：选流、探测、init+段按序提交、字节信号量 worker 池 |
| `internal/resolve/dash.go` | `.mpd` 分类 + 标记盖章，http(s) 白名单，SSRF 出口校验 |
| `internal/resolve/registry.go` | forced 流路由集中实现 |
| `internal/core/types.go` | `HeaderStreamKind` 内部路由常量 |
| `internal/transport/{http,hls,dash}` | Supports 分区 + `headerOf` 剥离标记 |
| `internal/transport/tputil/sequential.go` | 提炼的顺序提交共享原语 |
| `tools/gates.py` | `internal/transport/dash` 覆盖率阈值 80% |

边界（不宣称）：dynamic / 多 Period / 分离音视频轨 / DRM(CENC) / SegmentBase /
实时偏移（@availabilityStartTime 失效裁剪）均不支持；真实公网 DASH 站点互通
未验证，证据为本机 httptest 端到端与单测。

## 后果

- FR-TRN-08 两半齐备（HLS + DASH），WBS 13 技术交付完成；`12` §3.3 对应用例
  随本包补齐。WBS 14 Remux 是流媒体链的下一个交付。
- Resolver 链变为 `metalink → link-wrapper → magnet → torrent → hls → dash →
  [plugin] → direct-http → direct-file`；传输注册表新增 `dash`。
- 通用 HTTP 传输的 Kind 守卫改变了空 Kind 资源外的分发行为——生产 resolvers
  恒设置 Kind，无兼容影响；`app` 链序与分发断言已更新。
