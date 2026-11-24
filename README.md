# GoBitMule（原 Universal Downloader / 通用下载器）

> 项目于 2026-11-24 正式更名为 **GoBitMule**，GitHub 仓库与 Go module 路径统一为
> `github.com/asdfgh129/gobitmule`。为保持命令行与协议兼容，`udd` / `udctl` 等工具名、
> `UD_*` 环境变量、`-UD0000-` PeerID 前缀继续作为兼容标识保留；历史证据中的旧名称
> 按取证时点原样保留，不追溯改写。

跨平台的统一下载引擎：把 **BT / 磁力 / ed2k / HTTP(S) / FTP(S) / SFTP / Metalink / 迅雷链接 / 视频网站 / Telegram / 微信** 等来源，收敛到一条流水线、一个调度器、一份状态。

> **当前阶段：P0「有条件通过」的附加条件已于 2026-11-10 全部关闭，P1 进行中（WBS 8 / 9 / 7 / 10 已交付，WBS 11 互操作套件 v1 已交付；ADR-0021 路径 A 已落地并于 2026-11-16 按发起人指令复签（非实名会签，见其决议记录 R5），真实双机实验室已部署（双机同处 `192.168.1.0/24`、iperf3 实测 94.1Mbps，本机已装真实 chihaya、`l4-preflight` PRESENT 1 / MISSING 5 / UNVERIFIED 3、方案见 `tools/l4_lab_runbook.md`）；**2026-11-16 两项发起人裁决**：`BT-02`/`BT-11` 注册表由 `productGap` 翻转为 `gateExternal`（本地现 14 SKIPPED / 0 UNSUPPORTED，仅 verified upload 才 PASS）、ADR-0022 将 NFR-PERF-01 门槛修订为「≥ 可用链路理论上限 × 80%」（百兆段 ≥10MB/s，原千兆 80MB/s 时点值保留不改）；**L4 真实客户端矩阵已于 2026-11-16 在双机实验室真实执行 `BT-01..BT-14`，14/14 PASS、`release_gate_met=true`**（报告 [`docs/19`](docs/19-L4实验室实测与发布门禁报告.md)，证据 [`docs/evidence/l4-2026-11-16/`](docs/evidence/l4-2026-11-16/)），NFR-PERF-01 双机应用层基准同日实测 **11.21MB/s ≥ 10MB/s、`met=true`**（ADR-0022 百兆段口径，报告 [`docs/20`](docs/20-NFR-PERF-01双机应用层基准实测报告.md)），**序 6 / WBS 11 已于 2026-11-17 按发起人裁决关闭（实名会签待补）**：法务文案口径与 `14` R-10 由发起人书面结论记档于 [`docs/18` §6.1](docs/18-序6评审与L4实验室准备材料包.md)，关闭不产生新证据）。** 当前态：**16 个门禁包**全部达标、**90 个链接模块** 0 copyleft / 0 未识别，`make gates-strict` 本机 **6 PASS / 0 WARN / 0 FAIL**（CI 于 2026-11-13 的 `cover` 步骤曾因 WAL 偶发 `SQLITE_BUSY` 失败，已在 `internal/store` 以有界事务重放修复，CI run `43399289527` / `43400218985` 均 `quality: success`；后续 `679a26e` run `43402970475` 与 `c3fbc70` run `43567640194` 于 2026-11-14 核验亦均 `quality: success`，另有被取代的 API 推送尝试 run `43566313113` 为 `failure`，属推送方式问题、非 flake 亦非门禁回归；后续 `84871b0` run `43570105065`、`0ca9164` run `43580889196` 与 `9395343` run `43595391886` 于 2026-11-14 核验亦均 `quality: success`，另 `915f783` run `43604236428` 于 2026-11-14 核验为 `quality: success`、`6ee661f` run `43695083832` 于 2026-11-15 核验为 `quality: success`，详见 `CHANGELOG` 与 `13` §4），`make interop` 本地 9 例全绿；本机 **aria2-next 3.0.9 + 真实 libtorrent 2.1.1 的 BX-01..BX-06 跨客户端证据（6/6 PASS，sha256 校验，均单向回环补充、不计入 L4 门禁；BX-05 / BX-06 为我方做种半向）**；`make perf` 产出 1GiB 回环参考基准（固定 `nfr_perf_01.met=false`）；唯一速查见 [`docs/README` §三](docs/README.md)，进度真值源为 [`CHANGELOG`](CHANGELOG.md) 与 [`docs/14` §11](docs/14-项目计划与风险管理.md)。

> **P2 时点补记（2026-11-20）**：WBS 13 HLS / DASH L1 已由 ADR-0023 / 0024 交付；
> WBS 14 内置纯 Go `remux` 与 WBS 12 opt-in `yt-dlp`（`-J` 只吐 JSON、自有格式评分）已落地。
> **但分离 A/V 下载 + 合并、lux 进程内解析、Deno/EJS 与真实站点回放仍未完成**，
> 「缺 ffmpeg 不退化到 720p」当时点未达成；边界详见 [`docs/23` v1.4](docs/23-当前状态审计报告.md) 与 ADR-0010 实现时点注。
> WBS 20 Web 控制台（Vue 3 + TS，五分区）于同日交付，前端 RPC 方法映射见 `docs/07` v0.14 §9；
> 备份 / 恢复 / 迁移仍为离线 CLI，未伪装为在线按钮。
> Web 控制台代码包 `4604d0c` 的 CI run `44692698811` 已 `quality: success`（含 Web 构建校验共 20 步），
> 当前滚动结论停在 `4604d0c`；`fd9fbf9` run `44672119353`（全 14 步）为时点值。
> 仓库已包含可运行的 Go 代码：HTTP(S) / FTP(S) / SFTP 直链与 BT / 磁力均能走完
> `Resolver → Resource → Transport → PostProcessor → Store` 全流水线。
> **P1 WBS 8 已交付（2026-11-10）**：`thunder://` / `flashget://` / `qqdl://` 解码与 Metalink
> v3/v4 + RFC 6249 清单解析已作为**内置、纯 Go、零网络 I/O** 的 Resolver 落地，含逐 URL
> SSRF 校验与按清单算法的整文件/逐片哈希校验。明确边界：多 `<file>` 清单、远端 `.meta4`
> 抓取、迅雷 P2SP / 高速通道 / 离线下载均**不支持**。
> **P1 WBS 9 已交付（2026-11-10）**：FTP / FTPS（隐式与 `AUTH TLS` 显式）/ SFTP
> 三个 Transport 落地，纯 Go、零 cgo、零新增 sidecar；FTP 被动数据连接逐条过 SSRF 数据通道
> 规则，SFTP 主机密钥必须 pin。凭据经 `POST /api/v1/credentials` 写入 OS 钥匙串
> （macOS Keychain / Windows DPAPI / Linux Secret Service；不可用时降级 0600 文件并在
> `/capabilities` 如实上报，见 ADR-0015 ④）后以 `credentialRef`
> 引用，明文不进 URL / 数据库 / 日志 / 事件。**P1 首版每个分片独立建会话（并发上限 8）**，
> 连接池与 FTPS 数据通道 TLS session 复用属后续优化；不承诺完整 FTP/SFTP 生态。
> **P1 WBS 7 已交付（2026-11-10）**：基于 `anacrolix/torrent v1.62.0` 的 BT / 磁力
> Transport 落地（MPL-2.0，未修改任何 MPL 文件）；magnet 与 `.torrent`（raw / base64 /
> 本地文件）作为内置 Resolver 接入，magnet 经 BEP 9 从直连对等点取元数据。
> **2026-11-14 路径 A 生效**：受限做种（分享率 1.0 / 60 分钟任一达标即停）、上传槽、
> UPnP/NAT-PMP 与统一 announce 统计已落地；私有种子仍强制禁 DHT/PEX **且不上传**、
> PeerID 诚实（`-UD0000-` 前缀，非 `-UD` 配置自动回落）；`File.NewReader`
> 流式拷回 `ChunkWriter` 并受统一 Budget 约束。明确边界：**不承诺 PT / 私有站准入**，
> 真实公网 BT 客户端互操作矩阵在 WBS 7 时点列为 WBS 11 待办、证据为回环 fixture
> （WBS 11 套件 v1 交付后该矩阵**仍未执行**，见下文）。
> **P1 WBS 10 已交付（2026-11-10）**：基于 `goja` 的 ES5.1 插件规则引擎落地于
> `internal/plugin/`——manifest 严格校验、静态扫描、Ed25519 规则包签名、能力裁剪
> （`fs`/`env`/`subprocess` 默认禁用）、`ctx.http` 逐跳 SSRF Guard 与白名单、目录指纹热更新、
> `RULE_OUTDATED` 兜底；`udctl plugin scaffold|keygen|sign|verify|test|list` 为离线工具链。
> 规则包在 `direct-http` 之前接入 Resolver 链，安全敏感内置仍优先。明确边界：
> goja 是**软隔离**（无 per-VM 堆上限），不承诺 WASM / Extism 硬隔离、Node/Deno 脚本兼容、
> 官方规则仓库与真实站点规则包；插件 REST 管理端点未实现。
> **P1 WBS 11 互操作套件 v1 已交付（2026-11-11）**：`internal/interop/` + `udinterop`
> 建立 `BT-01..BT-14` L4 门禁注册表与 9 条纯回环自动化用例——自建 BEP 3 HTTP/HTTPS 与
> BEP 15 UDP tracker（compact IPv4/IPv6、passkey、故障注入）、独立 seeder ↔ `bttransport`
> 真实线协议下载、BT 握手抓包诚实 PeerID、纯磁力 BEP 9 取元数据、多文件选择下载与流式首片；
> `make interop` 产出 `dist/interop.json` / `dist/INTEROP.md`，并自动合并
> `dist/interop_external.json` 的真实跨客户端证据段。**明确边界**：
> `BT-01..BT-14` 需真实 qBittorrent / Transmission、抓包与千兆内网，本次全部记为
> `SKIPPED`（`BT-02` / `BT-11` 仍为 `UNSUPPORTED`：套件 v1 注册表仍按
> `ScopeProductGap` 固定该状态；路径 A 已补齐实现但 L4 真实矩阵未执行，**不得**翻转状态），
> **发布门禁未通过**、
> 序 6 不关闭；NFR-PERF-01 千兆内网基准同样未执行。
> **2026-11-16 实测结果（L4 门禁已通过；上文「发布门禁未通过 / L4 0/14」为更早时点值，保留不改）**：
> 双机隔离段实验室以真实外部 harness 执行 `BT-01..BT-14`，
> **14/14 PASS**、0 FAIL / 0 SKIPPED / 0 UNSUPPORTED，`release_gate_met=true`；
> `BT-02` / `BT-11` 由真实 qBittorrent 做种 / 下载与 `bytes_uploaded` 同源统计判定，
> `BT-03` 完成真实双向，`BT-04` 走隔离段本地真实 DHT，`BT-08` / `BT-14` 有抓包归档。
> 报告见 [`docs/19`](docs/19-L4实验室实测与发布门禁报告.md)，原始证据与 SHA-256 见
> [`docs/evidence/l4-2026-11-16/`](docs/evidence/l4-2026-11-16/)。
> **边界**：受控实验室、指定客户端版本，不等于公网全矩阵 / PT / 千兆性能达标。
> 同日 NFR-PERF-01 双机应用层基准也已执行：1GiB / 91.367s = **11.21MB/s**、
> 4096/4096 分片校验，ADR-0022 百兆段门槛 ≥10MB/s，`nfr_perf_01.met=true`；
> iperf3 单流/8 流均 94.15Mbps ≈ 11.22MB/s。报告
> [`docs/20`](docs/20-NFR-PERF-01双机应用层基准实测报告.md)，证据
> [`docs/evidence/nfr-perf-01-2026-11-16/`](docs/evidence/nfr-perf-01-2026-11-16/)。
> **不宣称原千兆 80MB/s 达标**；技术验收项已全部满足，序 6 仅余治理复签。
>
> **2026-11-16 时点口径（上文 12 SKIPPED / 2 UNSUPPORTED 为套件 v1 时点值保留不改）**：
> 发起人裁决翻转 `BT-02` / `BT-11` 注册表为 `gateExternal`（由隔离段真实 harness 判定，
> 仅 verified upload 才 PASS），本地 `make interop` 现为 **23 例：9 PASS / 0 FAIL /
> 14 SKIPPED / 0 UNSUPPORTED**，但 L4 门禁仍 0/14、`release_gate_met=false`——翻转的是
> 判据归口，不是「实现落地即 PASS」（**同日更晚时点已由真实实验室判定 14/14 PASS，
> 见本节上方「2026-11-16 实测结果」；该句保留为注册表翻转时点口径**）。NFR-PERF-01 亦按 ADR-0022 修订为「≥ 可用链路 × 80%」
> （百兆段 ≥10MB/s，实测 94.1Mbps ≈ 11.8MB/s、非千兆），原 80MB/s 为时点值保留不改。
> 补充证据（2026-11-13 扩容）：`cmd/udinteropx` 支持双引擎，驱动项目真实 `bttransport`
> 对 **aria2-next 3.0.9**（MotrixNext，`BX-01` / `BX-02`）与真实 **libtorrent 2.1.1**
> （`BX-03` / `BX-04`，可选 `tools/interop_libtorrent.py` seeder，非项目依赖、CI 不跑）
> 做种方完成下载并 sha256 校验，取得 **4/4 PASS 真实互通证据**，`make interop-external` 可复跑。
> **2026-11-15 时点口径**：外部证据扩为 **BX-01..BX-06 6/6 PASS**，新增 `BX-05`
> （aria2-next）/ `BX-06`（libtorrent，`tools/interop_libtorrent_leecher.py`）由真实第三方
> 引擎先做种、我方带 `SeedingPolicy` 下载并保留后台做种会话，停掉原 seeder 后由新的
> 真实 leecher 从我方下载并 sha256 校验，首次验证**我方做种半向**真实线协议互通；
> 针对回环首试握手竞态改为有界 2 次真实执行重试并显式记录次数，
> `SKIPPED` / `UNSUPPORTED` 不重试、不伪造 PASS。
> **2026-11-15 续包**：`cmd/udinteropx/main_test.go` 5 条确定性回归锁定重试诚实语义；
> `runWithLoopbackRetry` 现按最终状态措辞，`FAIL → SKIPPED` / `UNSUPPORTED` 不再被写成「通过」。
> 但六条都是**单向、回环、自托管 tracker**：aria2-next 不点名于
> `BT-01..BT-14`；libtorrent 虽点名于 `BT-03`，该判据要求同一真实客户端会话内双向
> （含 qBittorrent / Transmission 矩阵），两个半向被拆成独立用例，且仍缺抓包、
> 隔离千兆内网与 `chihaya` / `opentracker` 二进制，故**均不计入 L4 门禁**；
> 套件 v1 注册表仍固定 `BT-02` / `BT-11` 为 `UNSUPPORTED`（2026-11-14 时点值，2026-11-16
> 已翻转为 `gateExternal`，见上文）。`make perf` 的 1GiB 回环参考值固定
> `nfr_perf_01.met=false`，不等于千兆内网达标。
> **序 6 阻塞定档（2026-11-13）**：核对已批准的 ADR-0004「后果 / 后续动作」、
> ADR-0017 决策 4 与 `03` 的 M 级需求后确认，它们都**要求**显式实现做种礼仪
> （分享率 / `seed-time` / 上传槽 / UPnP-NAT-PMP / 统一 announce 统计），
> 而 WBS 7 起 `bt.clientConfig` 固定 `NoUpload=true` / `Seed=false`。
> 因此此前「ADR-0004 不做种边界」属误归属：真正根因是**实现相对已批准决策的
> 未签署范围缩减（合规差距）**，与判据冲突、实验室条件缺失并存。
> 已按 `15` §4 提交 [`ADR-0021`](docs/adr/ADR-0021-L4门禁与P2P礼仪范围冲突裁决提案.md)
> （`Proposed`，**未生效**）列出三条须评审签署的路径；签署前
> `release_gate_met: false`、`UNSUPPORTED` 不得计 PASS 或豁免。
> **2026-11-14 时点口径（路径 A 裁决，上文为提案期原文、保留不改）**：项目发起人
> 明确指令「按 A 方案补齐」，ADR-0021 已按 `15` §4 规则④转为
> `Accepted（路径 A）` 并同步 ADR 索引；受限做种 / 分享率 / `seed-time` / 上传槽 /
> UPnP / 诚实 PeerID / `tasks.bytes_*` 统一统计与 `Completed → Seeding → Completed`
> 条件迁移均已落地，**未签署范围缩减（合规差距）这一根因已消除**。
> **但** `docs/18` §六 评审组五方签署表仍全部留白（本次为发起人指令裁决，
> 不冒充三方会签），法务口径与 `14` R-10 重评待补；`12` §3.1 判据未改，
> L4 仍 **0/14**、`BT-02` / `BT-11` 仍 `UNSUPPORTED`（实现具备 ≠ 已验证；2026-11-14
> 时点值，2026-11-16 已翻转为 `gateExternal`）、
> `release_gate_met=false`、`l4_executed=false`、序 6 **仍开放**。
> 同轮新增 `make l4-preflight` / `l4-preflight-check` / `l4-run` 打通实验室接线
> （前置条件清点，**不是 L4 执行证据**，示例 stub 配置只能产出 `SKIPPED`，三者均不接入 CI）。
> **L4 证据包完整性审计（2026-11-14）**：新增 `make l4-audit`（`internal/interop/audit.go`
> + `cmd/udl4audit`），独立重算**已产出** `ud.interop/v1` 证据包的 9 项 `AUD-*` 检查，
> 写 `dist/l4_audit.json` / `dist/L4_AUDIT.md`；**`CONSISTENT` 只是内部自洽结论，
> 不等于 L4 已执行或通过**，审计器恒 `l4_executed_by_audit=false`、不接入 CI、
> 不替代 `make interop-gate`。实测 stub 产物 `CONSISTENT` 且重算门禁 `false`，
> 篡改副本 `INCONSISTENT`（`AUD-SUMMARY` / `AUD-GATE-VERDICT`）。四目标
> （`l4-preflight` / `l4-preflight-check` / `l4-run` / `l4-audit`）均不接入 CI。
>
> **路径 A 做种半向证据与缺陷修复（2026-11-15）**：新增
> `internal/interop/seeding_e2e_test.go`（CI 可跑的确定性回环回归：关闭原 seeder 后
> 由第二个真实 BT 线协议 peer 从 ud 后台做种会话下载并校验字节、`Transport.Close()`
> 回收监听），并修复 `bt.listenHost` 为 IPv4 字面量时 `tcp6` 绑定失败导致
> `torrent.NewClient` 报错的缺陷（`applySeedingPolicy` 按地址族关闭 IPv6 / IPv4 监听）。
> **零 L4 证据**：L4 仍 0/14、`release_gate_met=false`、序 6 仍开放。

> **ADR-0021 路径 A 实现落地（2026-11-14）**：`internal/transport/bt/seeding.go`
> （`SeedingPolicy` / `SeedingManager` / `SeedingObserver`）实现后台受限做种会话：
> 分享率 `1.0` 或 60 分钟任一达标即停、`maxUploadSlots=8` 上传槽、`enableUPnP`
> 端口映射、诚实 PeerID 强制回落、**私有种子恒不上传且恒禁 DHT/PEX**；
> `bt.*` 配置键由 `internal/config` 真正生效（`seedEnabled` / `maxUploadSlots` /
> `listenPort` / `listenHost` + `UD_BT_*` 覆盖），`Validate()` 拒绝无界做种目标。
> `Client.ConnStats()` 的上传计数与 tracker announce 同源写入 `tasks.bytes_uploaded`，
> 并条件驱动 `Completed → Seeding → Completed`；`transport.Registry.Close()` 与
> daemon ctx 保证会话与临时目录回收。未注入 policy 的 Transport 仍 leech-only，
> 因此 9 条 L3 回环用例行为不变。**上传字节速率不设第二 limiter**。
> ⚠️ 这是**编码部分**：`14` R-10 反吸血风险重评、安全 / 法务复签、
> 真实 L4 矩阵与 NFR-PERF-01 **均未执行**；`release_gate_met=false`、
> `l4_executed=false`、序 6 开放，评审组签署表仍留白。

> **评审与实验室准备材料包（2026-11-14）**：[`docs/18`](docs/18-序6评审与L4实验室准备材料包.md)
> （`v0.3`、`草案`、**评审组复签待补**）集中已核实事实清单、A / B / C 影响面核对表、
> 留白豁免清单与评审签署表模板、隔离千兆内网拓扑 / 抓包方案、9 项 `LAB-*` 前置条件映射
> 与 NFR-PERF-01 双机基准方案；**不代行裁决、不产出 L4 证据**，空表未签署无效。
> 不得据本地用例宣称「已与主流客户端实测互通」「全矩阵通过」或「千兆内网性能达标」。
> `make interop-gate` 在门禁未满足时明确失败。
> **P0 不是功能完整版**——ed2k / 真实站点规则包 / WASM 硬隔离 / L1 RPC facade
> 仍为 P1/P3 目标；**可选 ffmpeg 后处理已于 P2 落地但仍默认关闭**（`postProcess.ffmpegEnabled`，
> 零 sidecar 基线不变），详见
> [ADR-0020](docs/adr/ADR-0020-P0实现基线与范围声明.md) 与 [更新日志](CHANGELOG.md)。

## 三条不可动摇的架构原则

1. **三类输入归一**：传输协议、链接封装格式、内容源解析器不是一类东西，全部经 `Resolver → Resource → Transport → PostProcessor` 处理。
2. **单调度器**：队列、限速、断点续传、进度、文件布局只在 Go 核心实现一次，不允许第二个组件拥有任务生命周期。
3. **纯 Go 单二进制**：传输层零 sidecar、零 cgo、零 GPL 依赖；外部能力一律以可降级插件形式接入，桌面与移动端可达性优先。

## 快速开始（P0）

```bash
# 需要 Go 1.27.4（因 GO-2026-6809 / GO-2026-6859 两次安全升级）；所有构建强制 CGO_ENABLED=0
make lint          # gofmt + go vet + 架构静态检查（6 守卫）
make test          # 全量单测 / 契约 / 集成
make build         # 六平台交叉编译 → dist/（udd + udctl 各 6 个）
make gates         # 评审门禁：覆盖率 / SBOM / 许可证 / govulncheck / 基准 / 回滚演练 → dist/
make interop       # WBS 11 互操作套件 v1（本地回环用例）→ dist/interop.json + INTEROP.md
make interop-gate  # L4 发布门禁（外部矩阵未执行时明确失败）
make l4-preflight  # L4 实验室前置条件清点（不是执行证据，不接入 CI）
make l4-run        # 隔离实验室执行外部矩阵（示例 stub 配置必须失败，不接入 CI）
```

> `make gates` / `make gates-strict` 需要 `govulncheck`（`go install golang.org/x/vuln/cmd/govulncheck@latest`，
> 可用 `GOVULNCHECK=<path>` 指定）。工具缺失或漏洞库不可达时该门禁明确报 WARN，不会伪装通过。
> 若本机全局 `GOSUMDB=off`，Makefile 会在项目级设为 `sum.golang.org` 以允许 `GOTOOLCHAIN=auto` 校验工具链模块，不修改用户全局配置。

启动 daemon 与 CLI（默认 `127.0.0.1:0` 随机端口 + 本地发现文件，不占固定 6800）：

```bash
cp config.example.yaml config.yaml
go run ./cmd/udd --config config.yaml          # 或 go build -o udd ./cmd/udd
go run ./cmd/udctl health
go run ./cmd/udctl capabilities
go run ./cmd/udctl add "https://example.com/file.iso" --conn 4
go run ./cmd/udctl list
go run ./cmd/udctl show <id>
```

浏览器打开 daemon 地址即为嵌入式 **Vue 3 + TypeScript Web 控制台**（`web/` 源码，构建为单文件由 `go:embed` 嵌入）：概览 / 任务 / 新建下载 / 设置 / 运维五个分区；`GET /api/v1/healthz`、`/stats`、`/capabilities`、`GET /events/history` 与 `WS /api/v1/events` 可用，前端类型化 RPC 方法到 L0 路由的映射见 [`docs/07` §9](docs/07-接口规范-API.md)。本地开发：`make web`（或 `cd web && npm run dev`，用 `UD_API_TARGET` 指向 daemon 端口）。

> 若默认 Go 模块代理不可达，可覆盖：`GOPROXY=https://goproxy.cn,direct make build`。

## 代码地图（当前仓库）

| 目录 | 职责 |
|---|---|
| `internal/core` | 冻结的 `Resource` 全字段模型、Task 状态机、错误码、ULID、事件总线、全部契约接口（零实现依赖） |
| `internal/config` | YAML/环境变量配置、校验、运行时 `PATCH /config` 管理器 |
| `internal/security` | SSRF Guard（含 FTP 被动数据通道规则）、PathGuard、脱敏、`CredentialStore` 的进程内参考实现 |
| `internal/store` | SQLite DDL v1（11 表）、Resource 规范化存取、分片真值表、按清单算法的整文件/逐片校验、崩溃恢复、备份 |
| `internal/resolve` | 内置 Resolver 注册链：Metalink v3/v4 + RFC 6249、thunder/flashget/qqdl 解码、HTTP(S) 与 FTP(S)/SFTP 直链、`Unsupported` 兜底（安全敏感内置优先） |
| `internal/transport/http` | Range 分段 / 无 Range 退化 / 重定向逐跳 / 重试 / 分片 sha256 |
| `internal/transport/ftp` · `sftp` | FTP/FTPS（隐式 + 显式 TLS、REST 续传、被动数据通道 SSRF pin）与 SFTP（host key pin、ReadAt 随机读） |
| `internal/transport/tputil` | 三个传输共享的凭据 / 目标校验 / 分片循环 / Budget 计费 |
| `internal/postproc` | ADR-0012 本地文件契约：P0 `none` 直通 + **P2 可选 `ffmpeg-transcode`**（真实 exec、默认关闭、能力探测失败只降级） |
| `internal/scheduler` | 唯一调度器、唯一 Limiter、ChunkWriter、清单 piece 长度锁定分片边界、原子提交 |
| `internal/api` · `internal/app` | L0 REST + WebSocket、daemon 装配、嵌入式最小 UI |
| `internal/plugin` | WBS 10 规则通道：manifest 严格校验、ES5.1 静态扫描、Ed25519 签名、goja 运行时、能力裁剪 / 逐跳 SSRF、目录指纹热更新与离线回放；在通配 `direct-http` 之前接入注册链 |
| `internal/interop` | WBS 11 互操作套件：L4 门禁注册表、外部 harness、BEP 3/15 自建 tracker fixture、9 条回环自动化用例与证据输出 |
| `cmd/udd` · `cmd/udctl` · `cmd/udinterop` | daemon、CLI 与互操作套件入口 |
| `tools/` | `build_docs.py`（文档门户）、`check_arch.py`（架构守卫）、`gates.py`（评审门禁证据） |

## 文档入口

**→ [`docs/README.md`](docs/README.md)：文档总览、阅读路径、完整清单与 P0 实现基线索引**

| 我想…… | 读这份 |
|---|---|
| 了解项目边界、非目标、成功指标 | [`docs/01-项目章程.md`](docs/01-项目章程.md) |
| 查功能/非功能需求与 P0 追踪矩阵 | [`docs/03-需求规格说明书-SRS.md`](docs/03-需求规格说明书-SRS.md) |
| 理解整体架构与流水线 | [`docs/04-架构设计说明书-SAD.md`](docs/04-架构设计说明书-SAD.md) |
| 查接口签名、状态机与 P0 检查表 | [`docs/05-详细设计说明书-LLD.md`](docs/05-详细设计说明书-LLD.md) + [`docs/06-数据模型与存储设计.md`](docs/06-数据模型与存储设计.md) |
| 查 REST / WS / CLI 契约 | [`docs/07-接口规范-API.md`](docs/07-接口规范-API.md) |
| 查「为什么这么定」 | [`docs/adr/README.md`](docs/adr/README.md) |
| 查实际改了什么、修了什么 | [`CHANGELOG.md`](CHANGELOG.md) |
| 查 P0 范围边界与残余风险 | [`ADR-0020`](docs/adr/ADR-0020-P0实现基线与范围声明.md) |
| 查排期与 P0 验收结果 | [`docs/14-项目计划与风险管理.md`](docs/14-项目计划与风险管理.md) |
| 查 P0 评审结论、签署与附加条件 | [`docs/17-P0里程碑评审材料.md`](docs/17-P0里程碑评审材料.md) |
| 看哪些文档待审核 / 已决定 | 文档门户首页 `#/home` 的状态总览 |

预览型阅读：运行 `python3 tools/build_docs.py` 后打开 `docs/_site/index.html`。这是一个带侧栏的单文件门户，**默认打开 `#/home` 状态首页**：统计卡 + 状态徽章 + 分类卡片，直观显示每份文档是「待审核 / 已决定 / 持续维护 / 已废止 / 未标注」，并支持按状态、类型筛选与搜索；侧栏保留全部 48 篇文档链接，CHANGELOG、ADR 与 P0 评审材料均可直接跳转。`docs/_site/` 是生成物，禁止手工编辑。

## 明确的能力边界（写进产品文案）

- ✅ **P1 WBS 11 已实现（套件）**：`BT-01..BT-14` 门禁注册表、自建 HTTP(S)/UDP tracker fixture、9 条可复跑回环用例与 `dist/INTEROP.md` 证据链；✅ **2026-11-16 双机实验室实测：`BT-01..BT-14` 14/14 PASS、`release_gate_met=true`；NFR-PERF-01 按 ADR-0022 达标（11.21MB/s、`met=true`）**，证据 `docs/evidence/`、报告 [`docs/19`](docs/19-L4实验室实测与发布门禁报告.md) / [`docs/20`](docs/20-NFR-PERF-01双机应用层基准实测报告.md)；但 CI 内 `make interop` 的绿灯（9 PASS + 14 `SKIPPED`、无外部配置）**仍不等于**发布门禁通过，不得把 CI 结果当作 L4 证据。✅ **补充证据（不计入 L4）**：`BX-01..BX-06`（aria2-next 3.0.9 + 真实 `libtorrent 2.1.1`，6/6 PASS、sha256 校验）均为**单向回环**证据，`BX-05` / `BX-06` 为我方做种半向；libtorrent 虽点名于 `BT-03`，但该判据要求同一真实客户端会话内双向且需抓包 / 千兆内网，故不通过。**2026-11-17 补充**：真实 `transmission-daemon 3.00` 的双向矩阵已单独实测通过（`MX-TR-A` 对方做种→我方下载、`MX-TR-B` 我方做种→对方下载，两向端到端 sha256 一致），同样**不计入 L4**、不改变 14/14，见 [`docs/21`](docs/21-第三客户端互操作矩阵Transmission实测报告.md)。性能侧 `docs/12` §5 的单机三项已用真实 `udd` 进程实测：1000 任务常驻内存增量 **8.6MB**、含 1000 任务恢复启动 **261ms**、创建→首字节 P99 **45ms**（2h 长稳 **12.5MB**），见 [`docs/22`](docs/22-性能基准实测-单机可复跑项.md)；`ffmpeg` 两项已于 2026-11-18 用**真实 ffmpeg 5.1.2** 补测（exec 中位 **163.1ms**、哈希并发为单跑 **73%**，均 PASS，见 `docs/22` §九），**仅移动端 BT 内存未执行**（需真机）。⚠️ **[`ADR-0021`](docs/adr/ADR-0021-L4门禁与P2P礼仪范围冲突裁决提案.md) 已由 `Proposed` 转 `Accepted（路径 A）`**：按项目发起人 2026-11-14 指令裁决并补齐做种实现，「未签署范围缩减（合规差距）」根因已消除（不是「ADR-0004 不做种边界」）；但 **L4 矩阵已于 2026-11-16 实测通过**（14/14，其中 `BT-02` / `BT-11` 由 `gateExternal` 以 verified upload 判定 PASS，不再固定 `UNSUPPORTED`）；该结论限于实验室环境与所列客户端版本，产品文案仍按 `16` §7 口径（待法务会签）。**评审组实名复签、法务与安全会签、R-10 复评仍待补**（**2026-11-17 当前态**：法务文案口径与 R-10 已由发起人书面结论记档——R-10 记「已接受，非已消除」、等级维持「中」、处置移交运营侧；序 6 / WBS 11 记为**按发起人裁决关闭（实名会签待补）**，见 [`docs/18` §6.1](docs/18-序6评审与L4实验室准备材料包.md)）；`make l4-preflight` 只清点条件、`PRESENT` 绝不等于 PASS（`12` §3.1.2 / §3.1.3）；`make perf` 只是 1GiB 回环参考（固定 `nfr_perf_01.met=false`），双机结论只看 `make perf-lab` 产出的 `dist/perf_lab.json`。
- ✅ **P1 WBS 10 已实现**：goja ES5.1 插件规则引擎（manifest 严格校验、Ed25519 签名、能力裁剪、逐跳 SSRF Guard、目录指纹热更新、`udctl plugin` 离线工具链、`RULE_OUTDATED` 兜底）；**软隔离**，不承诺 WASM / Extism 硬隔离、Node/Deno / 浏览器扩展脚本兼容、现代 JS 语法、官方规则仓库与真实站点规则包（L4 互操作矩阵属 WBS 11）。
- ✅ **P1 WBS 7 已实现**：BT / 磁力链接下载（标准 BitTorrent 网络、公开 HTTP/UDP/HTTPS tracker、magnet 经 BEP 9 取元数据）；**2026-11-14 路径 A**：受限做种 / 分享率 / `seed-time` / 上传槽 / UPnP 与统一 announce 统计已生效，私有种子禁 DHT/PEX 且不上传、诚实 PeerID（`-UD` 前缀强制回落）；**上传字节速率仍由 `scheduler.Limiter` 唯一权威管理**；标准 eD2k 网络（线协议层）为 P3 目标，尚未实现。
- ✅ **P0 已实现**：HTTP(S) 直链、Range 分段、断点续传、暂停/恢复/取消、崩溃恢复、全局限速、L0 REST/WS/CLI、Web 控制台（2026-11-20 起 Vue 3 + TS）、六平台纯 Go 构建。
- ✅ **P2 可选后处理（默认关闭）**：`ffmpeg-transcode` 只处理已下载的本地文件（mp4/mov/mkv/webm），固定参数白名单、显式 muxer、进程组取消、磁盘空间预检、全局并发槽位；缺 ffmpeg 或不合规构建只降级，不影响下载。**需要 operator 自备二进制，不随包分发**。
- ❌ **不承诺**私有 tracker 准入、迅雷 P2SP/高速通道/离线下载、ed2k 下载速度、DRM 内容解析、100% aria2 RPC 兼容。
- ⚠️ 微信视频号嗅探为**单独分发、默认不安装**的可选插件，涉及逆向与平台条款风险。

## P0 已知限制

1. ~~`MemoryCredentialStore` 是进程内参考实现~~ **2026-11-17 已落地 OS 钥匙串凭据适配器（ADR-0015 ④）**：macOS 走 `/usr/bin/security`（密钥经 stdin，不进 argv）、Windows 走纯 Go DPAPI、Linux 走 `secret-tool`（需装 libsecret-tools + session bus，否则降级）；实际后端经 `GET /api/v1/capabilities` 的 `credentials` 字段如实上报，`file`（0600 文件）**不是钥匙串**、`memory` 重启即失。Windows / Linux 真机路径未验证（仅交叉编译 + 单测）。
2. 非回环监听必须配置 `remoteToken`（否则拒绝启动），且目前只有 Bearer 鉴权，无 TLS / Origin 校验。
3. `postProcess` 可选 `none` / 内置 `remux` / opt-in `ffmpeg-transcode`；Web 控制台已于 2026-11-20 交付，但备份 / 恢复 / 迁移仍是离线 CLI，分片级明细与 sidecar 运行时开关未实现（见 `docs/07` v0.14 §9）。
4. CycloneDX SBOM / `govulncheck` / 许可证扫描 / 覆盖率报告 / 回滚演练已由 `make gates-strict` 执行，覆盖率全部达标；WBS 11 起互操作套件 v1 的**本地自动化部分**已由 `make interop` 执行；`BT-01..BT-14` L4 真实客户端矩阵已于 2026-11-16 在双机实验室 **14/14 PASS**（`docs/19`），NFR-PERF-01 双机应用层基准同日 **`met=true`**（11.21MB/s ≥ 10MB/s，ADR-0022，`docs/20`）。技术验收项已满足；**序 6 于 2026-11-17 按发起人裁决关闭（实名会签待补）**；**不宣称公网全矩阵 / PT / 千兆 80MB/s 达标**。

完整清单见 [`docs/11` §10](docs/11-安全与合规设计.md) 与 [`docs/14` §9](docs/14-项目计划与风险管理.md)。

## 许可证

**代码：MPL-2.0**（2026-11-09 定档，见 [`LICENSE`](LICENSE)）；**文档：CC-BY-4.0**。发布二进制须随附 `LICENSE` 与 `THIRD_PARTY_NOTICES.md`，修改过的 MPL 文件按 MPL-2.0 提供并记录修改清单。依赖许可证矩阵见 `docs/10-技术选型与依赖清单.md` §4，P0 实际锁定依赖见其 §7。
