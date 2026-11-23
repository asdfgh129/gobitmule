# goed2k fork 源码 Spike（WBS 16 / ADR-0030 技术细化）

> 状态：**源码勘察报告，不是实现完成报告**。本文记录 2026-11-21 对
> `github.com/monkeyWie/goed2k` 真实源码的只读勘察结论，用于细化 ADR-0030
> 的 fork 改造范围。所有结论可由 `go mod download github.com/monkeyWie/goed2k@latest`
> 复现（pseudo-version `v0.0.0-20260602122456-f2a71d599dee`）。
> 门禁状态以 §11 为准：ED-06 `PASS`，ED-01/02/03/04/05 `PARTIAL`
> （ED-05 本轮由 BLOCKED 推进为 PARTIAL：obfuscation 协议层已实现并通过
> A/B 握手往返单测，但尚未接入 live TCP 连接、未做仅混淆对端真机取证）；
> 本文不翻转 P3 发布结论。

## 1. 勘察对象

- 模块：`github.com/monkeyWie/goed2k`，`go 1.24.2`，MIT。
- 组成：完整 eD2k 客户端库（ED2K 下载、多任务、server.met 找源、KAD bootstrap
  与 source 查找、UPnP、状态持久化/恢复、进度订阅、上传），附 TUI。
- 上游规模：单维护者（bus factor = 1），无 tag，仅 pseudo-version。这印证
  ADR-0030「fork 自维护」的必要性。

## 2. 与 ud Transport 契约的错配（实测确认）

ud 侧（`internal/core/interfaces.go`）：

```go
type Transport interface {
    Name() string
    Supports(r *Resource) bool
    Probe(ctx, r) (*ProbeResult, error)
    Run(ctx, r, w ChunkWriter, b Budget) error   // chunk 边界/状态由调度器持有
}
type ChunkWriter interface {
    Next(ctx) (Chunk, bool, error)
    WriteChunk(ctx, c, p []byte) error
    Verified(ctx, c, sum Hash) error
    Release(ctx, c, err) error
}
```

goed2k 侧（实测）：

| 维度 | goed2k 现实 | 冲突性质 |
|---|---|---|
| 分片 | `PieceSize = 9728000`（9.28MB）、`BlockSize = 190KiB`，协议硬编码常量（`constants.go`） | 与 ud 的 4MiB chunk **边界不对齐** |
| 落盘 | `PieceManager.WriteBlock` → `handler.File().Seek(offset)+Write`，自管 `*os.File` | 若直接依赖则 lib 自写盘，绕过 `ChunkWriter` |
| 校验 | `HashPiece` 由库内部计算并 `OnPieceHashCompleted` | 绕过 `w.Verified` |
| 状态 | `Transfer.state`（`Finished`/`Paused`/…）+ 可选 `AutoSaveInterval` 持久化 | 与调度器 SQLite 单一真相双写（违反 ADR-0002） |
| 限速 | 库内 policy/peer 限速 | 与 ud `Limiter`（唯一速率权威）并行 |

**结论**：把 goed2k 当"流式逐块源"直接塞进 `ChunkWriter` 需要改写 `PieceManager`，
触碰 eD2k piece/block 不变量，风险与工作量都远超预期。

## 3. 低成本适配路径：BT 同构的 reader 模式（关键发现）

`internal/transport/bt/bt.go::Run` 已建立先例：**第三方引擎自己下载到临时
dataDir，transport 用 reader 按 ud chunk 边界读回并喂 `ChunkWriter`**：

```go
dataDir, _ := os.MkdirTemp("", "ud-bt-")
cl := torrent.NewClient(clientConfig(..., dataDir))
file.Download()
reader := file.NewReader()
for { c, ok, _ := w.Next(ctx); reader.Seek(c.Start); readWithBudget(ctx, reader, buf, b); w.WriteChunk(ctx, c, buf) }
```

该模式**不要求引擎回调 ChunkWriter**，只需引擎提供「完整可读文件」，因此
完全适用于 goed2k。goed2k 提供所需全部 API（实测签名）：

- `client.NewClient(settings)` / `Start()` / `ConnectServers(...)` / `Close()`
- `client.AddLink(link, outputDir)` 或 `AddTransfer(AddTransferParams)`：
  `AddTransferParams.Handler` 可注入自定义 `disk.FileHandler`，从而把下载文件
  落到受控临时目录（`NewAddTransferParamsFromFile` / `...FromHandler`）。
- `client.Wait()`（等同 BT 的 completion wait，内部轮询所有 handle 的 `IsFinished()`）
  或 `SubscribeTransferProgress()` 逐任务进度订阅。
- `PieceManager.ReadRange(begin, end)` 或自定义 handler 的 `File()` → 按
  `c.Start / c.Length` 读回。

**故 fork 改造范围从「重写 PieceManager」降级为「薄适配层」**：
`internal/transport/ed2k` 复用 BT 模式，把 goed2k 指向临时目录 → `Wait()` →
用 `os.File`/`ReadRange` 按 chunk 边界读回喂 `ChunkWriter`，完成后可选 kept-alive
上传会话（对齐 BT 的 SeedingManager）。**不触碰 ADR-0002 单一调度器不变量**。

### fork 内仍需处理的点（ADR-0030 第 1 条据此细化）

1. 关闭/隔离库内 `AutoSaveInterval` 持久化与库内限速，避免与 ud 状态/速率双写。
2. 显式暴露开关：CryptLayer 混淆、Secure Ident、AICH、`nodes.dat`/`server.met`
   路径、`ConnectSavedServer`。这些是 ED-05/ED-06 的验收对象，上游 README 未记载。
3. Identity 沿用 ADR-0013 冻结字段，零新增（ADR-0030 第 2 条不变）。
4. MIT 声明随 fork 保留。

## 4. 门禁可达性（实验室实测 2026-11-21）

- 可用实验室机：`192.168.1.222`（主机 A）、`192.168.1.221`（NAT 主机），
  均 Ubuntu 23.04，**无 Go 工具链、无外网**。编译须在主机侧交叉编译后 scp。
- `192.168.1.209` SSH 拒绝当前密钥（publickey/password/keyboard-interactive），
  需补充凭据方可使用。
- 三机 22 端口均可达，221↔222 互通（实测 HTTP Range 源可达）。
- ED-01/02/05/06 需要：aMule / eNode-go 真实客户端、KAD `nodes.dat`、
  TCP 4662 / UDP 4672 入站。实验室现装软件清单中**无 eD2k 客户端**，
  且隔离段无外网下载 server.met。故门禁仍 `NOT_EXECUTED`，需先补环境与凭据。

## 5. 结论与建议路径

| 项 | 结论 |
|---|---|
| fork 必要性 | 成立（bus factor=1、无 tag、缺 ED-05/06 开关） |
| fork 改造量 | **低于 ADR-0030 初判**：采用 BT reader 模式，薄适配层即可，无需重写 PieceManager |
| 可立即开工 | `internal/transport/ed2k` 的**离线骨架**（Probe/Supports/资源映射/单测），零网络 |
| 需前置条件 | 真实 `AddTransfer`+`Wait` 联调、ED 门禁：eD2k 客户端 + KAD + 端口入站 + 209 凭据 |
| 发布结论 | 不因本 spike 翻转，ED 门禁未过则 P3 不得发布（R-06） |

**建议**：下一步先落 `internal/transport/ed2k` 离线骨架（与已落地的
`resolve/ed2k.go` 衔接），再在获取 eD2k 客户端/凭据后补真实联调与 ED 门禁。

## 6. 落地补记（2026-11-21 同日）

### 6.1 离线骨架已实现

`internal/transport/ed2k/ed2k.go` + `ed2k_test.go` 已落地，94% 语句覆盖并
纳入 `tools/gates.py` 80% 门禁集：

- `Downloader` seam：`Open(ctx, r) (io.ReadSeekCloser, int64, error)`。生产
  实现（fork goed2k：`AddTransfer`+`Wait`+`ReadRange`）**未接入**；注入前
  `Run` 返回 `E_CAPABILITY_UNAVAILABLE`，绝不静默半下载。
- `Probe` 纯离线：从 `Resource.Files[0].Size` + `Identity.ED2KFileHash` 读取，
  零网络、零新增字段（ADR-0013）。
- `Run` 复用 BT reader 模式：`reader.Seek(c.Start)` → `readWithBudget` →
  `w.WriteChunk`，逐 chunk 计费走唯一 Limiter；越界 chunk `Release` 并报错，
  引擎上报 size 与 link size 不符时 `E_CHECKSUM_MISMATCH`。
- **默认不注册**到 daemon：无生产 Downloader 时不改变 dispatch 行为；
  fork 落地后 `transports.Register(ed2ktransport.New(goed2kDL))` 一行启用，
  `Supports` 只认 `KindED2K`，不影响其他 kind。

**刻意不 `go get goed2k`**：它是带 `charmbracelet/bubbletea/lipgloss/bubbles`
整套 TUI 的完整应用库，直接依赖会把 TUI 全家桶污染进生产依赖树，与
ADR-0030「fork 自维护、剥离 TUI」相悖。故骨架零新增依赖，用 seam 隔离。

### 6.2 实验室 209 补探（凭据已获取）

用 `bemin` 凭据成功登录 209（OpenSSH `SSH_ASKPASS` 通道）。**209 实为
Windows 10（build 19044）主机**，非 Linux，纠正 §4 的推断：

- IP：`192.168.1.209` / `192.168.34.209`；有 Python 3.11、Tailscale、Wireshark。
- **关键**：`emule.exe` 正在运行（PID 12124，路径
  `C:\Program Files (x86)\eMule\emule.exe`）——实验室已有真实 eD2k 客户端。
- 但 `netstat` 无 4662/4672/4711 监听，未见 `nodes.dat`/`server.met`，
  Tailscale 处于 NoState。即 eMule 在运行但 **KAD 引导与入站端口未配置**。

**结论（2026-11-21 时点，已被 §7 取代）**：ED 门禁的「客户端」前置条件已部分
满足（eMule 存在），但当时「KAD + TCP/UDP 入站 + 真实互通」仍缺，ED-01/02/05/06
维持 `NOT_EXECUTED`。

## 7. 对端就绪补记（2026-11-22）

用户在 209 上完成 eMule KAD 配置，§6.2 的「KAD 未配置」论断被推翻。真机取证
（证据：`docs/evidence/ed2k-209-2026-11-22/kad-environment.json` + SHA256SUMS）：

- **入网端口**：`Port=31969`（TCP）、`UDPPort=41296`；`netstat` 见
  TCP `0.0.0.0:31969 LISTENING` + **3 个外部 ESTABLISHED**，UDP `0.0.0.0:41296 LISTENING`。
  即 **High ID 入站成立**（用自定义端口，非 4662/4672）；属 ED-03 的对端侧证据。
- **KAD 引导**：`NetworkKademlia=1`，`nodes.dat` 6812 B、`preferencesKad.dat`、
  `KadUDPKey` 齐备 —— KAD bootstrap 成功，外部 TCP 对等节点已连入。
- **身份/混淆**：`SecureIdent=1`、`LogSecureIdent=1`、`CryptLayerSupported=1`、
  `known2_64.met` 4.5 MB（09:44 更新）—— ED-05/ED-06 的**对端配置**就绪。
- **活跃度**：`clients.met` 279 KB（09:41）、`server.met.new` 6.8 KB（09:45）刚刷新；
  `downloads.txt` 有一条真实 `ed2k://|file|...` 任务（仅记录 size/MD4 三元组，不渲染文件名）；
  `D:\eMule\incoming\<hash>` 下 72 个已完成文件（156 MB）——真实完成文件可直接作为 reader 源。
- WebServer `Enabled=0`（4711 未监听）：无 Web API 触发下载的通道，**不**把它当作
  生产 Downloader 路径。

### 7.1 门禁结论与边界（严格）

| 项 | 状态 | 说明 |
|---|---|---|
| 对端条件 | **READY** | 真实 eMule + 活跃 KAD + 入站 TCP + 完成文件 |
| ED-01 | `NOT_EXECUTED` | 需 ud 侧 fork 与对端互可见（缺第二 aMule/eNode-go 对等体） |
| ED-02 | `NOT_EXECUTED` | eMule 自身能找源，但 ud 引擎未跑 KAD 搜源/发布 |
| ED-03 | `PARTIAL` | High ID 在 eMule 上成立，未在 ud 引擎验证 |
| ED-05 | `NOT_EXECUTED` | 对端 `CryptLayerSupported=1`，未做 ud 侧「仅混淆」握手 |
| ED-06 | `NOT_EXECUTED` | 对端 `SecureIdent=1`，未做 ud 侧重启 UserHash 稳定性 |

**发布结论不变**：ED 门禁验证的是 **ud 侧 goed2k fork** 的互操作，不是 eMule 自身；
对端就绪只消除了环境前置，fork Downloader 仍未接入，故 ED-01/02/05/06 不翻转。

### 7.2 Transport 落地：`LocalFileDownloader`

`internal/transport/ed2k/filedownloader.go` 落地 `Downloader` 的一个**真实**实现：
从受控完成目录（如 209 的 `D:\eMule\incoming`）读取 eMule **已完成**文件，返回
seekable reader + 落盘 size，交给 `Run` 的 chunk 计划 / Budget / ChunkWriter。

- 这是 docs/25 §3「reader 适配模式」（BT 同构）的最后一环：eMule 负责下载落盘，
  ud 负责逐 chunk 读回并计费；**不**下载、不替代 fork 的 `AddTransfer+Wait`。
- 文件名来自不可信 `ed2k://` 链接，统一经 `security.PathGuard.Clean` 约束在 root 内
  （拒绝 `..`/绝对路径/保留设备名）；落盘 size 由 `Run` 与链接冻结 size 比对。
- `filedownloader_test.go` 用 `t.TempDir()` 真实文件做 byte-exact 端到端（814 B → 64 B
  chunks → 聚合逐字节一致 + Budget 计费）、size 守卫、路径穿越、未配置、目录拒绝，
  零网络。至此 transport 从「只有 fake reader 单测」推进到「真实文件 reader 可用」。
- 仍**默认不注册**：生产启用需显式构造 `NewLocalFileDownloader(root)` 或未来 fork，
  不改变 dispatch 行为。

## 8. fork 落地与引擎接入（2026-11-22）

按 ADR-0030「fork 自维护」执行，把 goed2k 核心库正式引入 ud：

### 8.1 fork 已落地（`internal/ed2k/`）

- 来源：module cache 的 `v0.0.0-20260602122456-f2a71d599dee`，MIT（LICENSE 随附，
  `internal/ed2k/FORK.md` 记录来源与改造）。
- **剥离 TUI**：只复制核心库（`*.go` + `data/` `disk/` `internal/` `protocol/`），
  `cmd/`（bubbletea/bubbles/lipgloss）与 `examples/` 不进 fork——核心库实测零第三方
  依赖，不污染 ud 依赖树（印证 §3 判断）。import 路径改为
  `github.com/asdfgh129/ud/internal/ed2k`。
- **编译验证**：fork 核心库 `go build ./internal/ed2k/...` 通过（EXIT=0）。

### 8.2 引擎适配器已落地（`internal/transport/ed2k/goed2k.go`）

- `Goed2kDownloader` 实现 `Downloader` 接口：`Open` 从冻结 Resource 还原
  `ed2k://|file|name|size|hash|/` → 配置 Settings（ListenPort/UDPPort/EnableDHT）
  → 加载 `nodes.dat` / `server.met` → `Start` → `AddLink` → `Wait` → 读回完成文件。
- **ADR-0002 隔离**：`MaxUploadRateKB=0`、`SetAutoSaveInterval(0)`，关掉库内限速与
  持久化，ud 保持唯一速率/状态权威。
- **ctx 取消**：`Wait()` 无 ctx 参数，用 goroutine + select 保证取消时 `Stop()` 引擎。
- **测试 seam**：窄 `engine`/`transferHandle` 接口注入 fake，`goed2k_test.go` 零网络
  覆盖正常路径与各错误分支。
- **编译验证**：适配器 `go build ./internal/transport/ed2k/...` 通过（EXIT=0）。

### 8.3 opt-in 注册与真机探针

- `config.ED2KConfig` 增 `Enabled/OutputDir/NodesDat/ServerMet`（默认 `Enabled=false`，
  dispatch 字节不变）；`daemon.go` 在 `Enabled=true` 时注册 `Goed2kDownloader`。
- `cmd/uded2kprobe/`：真机 KAD 探针，`LoadDHTNodesDat` + `Start` + 观察 `DHTStatus`
  （`Bootstrapped`/`LiveNodes`/`Firewalled`），JSON 输出，供 ED-02/03 取证。
- 209 的 `nodes.dat`（6812 B）已取得到 `docs/evidence/ed2k-209-2026-11-22/nodes.dat`，
  格式经核对为标准 eMule nodes.dat（goed2k `LoadNodesDat` 可解析）。
- 222 实测有公网（`8.8.8.8:53` 通、`https://github.com` HTTP 200），纠正 §4「无外网」
  旧论断——ud 引擎可连公网 KAD 与 209 eMule 互连。

### 8.4 真机门禁状态：NOT_EXECUTED（环境阻塞）

**ED-01/02/05/06 仍 `NOT_EXECUTED`，ED-03 仍 `PARTIAL`，发布结论不变。** 阻塞原因
不是代码或环境前置，而是**本机 Go 工具链故障**：在接入后期 `go build` 出现环境级
卡死——连最小 hello world 都超时（`go build -x` 停在 `WORK=` 目录创建后、零输出、
退出码 124），已排除进程残留、`/tmp` 与 Windows TEMP 残留目录、GOCACHE、
GOTOOLCHAIN、磁盘空间等因素。因此：

- 无法交叉编译 `uded2kprobe` 部署到 222，无法执行 KAD bootstrap / 双客户端互连 /
  混淆握手 / SecureID 重启等真机步骤；
- 无法完成最终全量门禁（`go test ./...` / `check_arch` / `cover`）与单测复验
  （`goed2k_test.go` 首次运行仅因 Windows 文件句柄未释放导致 TempDir cleanup 失败，
  已加 `t.Cleanup` 修复，修复后因编译故障未能复验）。

**恢复条件**：Go 工具链可用后，依次执行
① `go test ./internal/transport/ed2k/...` 复验单测；
② `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o uded2kprobe ./cmd/uded2kprobe/` 交叉编译；
③ scp 探针 + nodes.dat 到 222，`./uded2kprobe -nodes nodes.dat -tcp <port> -udp <port> -wait 60`
取 DHTStatus 证据；④ 与 209 eMule 做同 hash 搜源/发布验证 ED-01/02；⑤ 补混淆握手与
SecureID 重启验证 ED-05/06；⑥ 回填证据并重新判定门禁。

## 9. 真机 ED 门禁执行结果（2026-11-22 续）

### 9.1 Go 工具链故障已定位并恢复

上一节 §8.4 记录的「go build 全部卡死」并非网络问题（关闭代理、全新
GOMODCACHE、GOTOOLCHAIN=local、hello world 均复现卡死；go 进程无网络连接）。
逐层定位结果：`go version`/`go env`/`go list nonexistent` 秒回，`go list fmt`/
`go list internal/reflectlite` 卡死 → 卡在 **GOCACHE（`C:/Users/bemin/sdk/gocache`）**
的包元数据读，该路径的缓存目录异常。改用 `C:/Users/bemin/AppData/Local/Temp/udcache`
作为 GOCACHE 后完全恢复：`go test ./internal/transport/ed2k/...` 2.4s 全绿、
linux 与 windows 探针均交叉编译成功（11.5MB）。**未使用 Proxyhub**（确认非网络原因）。

### 9.2 真机取证（探针部署 222 + 209）

交差编译 `cmd/uded2kprobe`（linux/amd64 + windows/amd64）部署到有公网的 222
与 High ID 的 209，用 209 的 nodes.dat 引导 ud fork 引擎接公网 KAD。证据见
`docs/evidence/ed2k-ed-gates-2026-11-22/`（含 SHA256SUMS）。

**确凿成果**：
- **KAD bootstrap 两机真机 PASS**（ED-02 前半）：222/209 均 `Bootstrapped=true`、
  `InitialBootstrap=true`、`LiveNodes=50`、`KnownNodes=200`。这是 ud 侧 fork 引擎
  首次真机入网，证明 fork 核心库与 ud 接入链路可用。
- **对端节点可见**（ED-01 部分）：222 注入 `192.168.1.209:41296` 后
  `KnownNodes` 200→201，ud 引擎把 209 纳入 KAD 节点表。
- **High ID**（ED-03 部分）：209 eMule TCP 31969 外部 ESTABLISHED（前轮取证）。

**首轮未闭环 / 阻塞（如实记录，不谎报；补缺复验见 §10）**：
- **KAD 搜源未返回源**（ED-02 后半）：首轮两机 `search_started=true`、debug 日志
  有 `dht source search started`，但两阶段 traversal（find-sources → search-sources）
  在 40–45 秒窗口内回调未触发（`search_completed=false`），`source_count=0`；
  两机 `Firewalled=true`（实验室 NAT 后）。
- **ED-01 双客户端连接层可见**未做到（需 aMule/自建 eD2k server 对手）。
- **ED-05 混淆握手**：首轮无「仅混淆对端」环境，未执行。
- **ED-06 SecureIdent/UserHash 持久化**：首轮 fork 未暴露 UserHash 读取/持久化
  校验 API，能力缺口，无法验证。

### 9.3 首轮门禁判定（已被 §10 补缺复验更新）

| 门禁 | 首轮状态 | 依据 |
|---|---|---|
| ED-01 | PARTIAL | ud 引擎把 209 纳入 KAD 节点表；未做到连接层双向可见 |
| ED-02 | PARTIAL | bootstrap 真机 PASS；搜源未返回具体源（traversal 未收敛） |
| ED-03 | PARTIAL | 209 High ID 已取证；ud 探针 Firewalled，源数量未验证 |
| ED-04 | NOT_EXECUTED | Low ID 模拟未执行 |
| ED-05 | NOT_EXECUTED | 无仅混淆对端 |
| ED-06 | BLOCKED | fork 无 UserHash/SecureIdent 持久化校验 API |

**发布结论不变**：ED-01/02/05/06 未全部通过，按 `docs/12` 规定 P3 不得发布，
R-06 维持。首轮把「不能执行」推进为「bootstrap 真机 PASS + 其余 PARTIAL/BLOCKED」，
但不翻转发布结论。

## 10. 补缺复验（2026-11-22 续）

### 10.1 代码补缺

- **KAD 搜源收敛**：`DHTTracker.readLoop` 改为在同一 goroutine 内用 500ms ticker
  推进 `node.tick()`，避免持续收包时 RPC 超时被饿死；`DHTSearchTimeout` 不再被
  忽略，RPC short/expired 超时由该值派生。探针新增 `-dht-timeout`。回归
  `TestSourceSearchCallbackCompletesOnRPCTimeout`。
- **ED-06 身份持久化**：fork `Settings` 增 `SecureIdent`；`Client` 暴露
  `UserHash` / `SetUserHash` / `SecureIdent` / `SetSecureIdent`；`ClientState`
  增 `user_hash` / `secure_ident` 并随 `SaveState` / `LoadState` 往返。探针新增
  `-state` / `-user-hash` / `-secure-ident`，退出时（含失败分支）落盘。回归
  `TestUserHashAndSecureIdentPersistAcrossRestart`、
  `TestSetUserHashRejectsInvalidHash`。
- **ED-04 Low ID 模拟**：`TestLowIDSimulatedServerHandshake` 用本地伪造 eD2k
  server 发送 `IdChange(ClientID=123456)`，断言 `LOW_ID` 分类且会话保活。
- **ED-05 能力判定**：fork 新增 `CryptLayerSupported() bool`（当前 `false`），
  探针报告 `crypt_layer_supported`；不把对端配置误读为本端能力。

### 10.2 真机复验

证据均在 `docs/evidence/ed2k-ed-gates-2026-11-22/`（含 SHA256SUMS）：

- **ED-06 PASS**：222 与 209 各做两次独立进程启动。首轮显式
  `-user-hash` + `-state` 落盘，次轮只给 `-state` 恢复；两机输出 UserHash
  分别恒为 `2222…2222` / `2092…0920`，`secure_ident=true`。状态文件含明文
  `user_hash` 与 `secure_ident` 字段，可审计。
- **ED-02 搜源收敛推进**：222/209 用 `-dht-timeout 3 -search-wait 60` 复跑，
  `search_completed=true`、`RunningTraversals=0`；debug 日志出现
  `find-sources` → `search-sources` 两段 `kad traversal done`。但真机节点
  `responses=0`、`timeouts=41-46`，`source_count=0`：两阶段收敛已修复，
  NAT/Firewalled 下找到可用源仍未闭环。
- **ED-05 BLOCKED 明确化**：两机探针均输出 `crypt_layer_supported=false`，
  证实 fork 无 CryptLayer/RC4 握手能力，不能构造「仅混淆对端」取证。

### 10.3 补缺后门禁判定

| 门禁 | 状态 | 依据 |
|---|---|---|
| ED-01 | PARTIAL | 节点表可见 209；连接层双向可见仍未做到 |
| ED-02 | PARTIAL | bootstrap PASS；搜源两阶段可收敛，但 `source_count=0` |
| ED-03 | PARTIAL | 209 High ID 已取证；ud 探针 Firewalled |
| ED-04 | PARTIAL | 本地 Low ID 握手模拟 PASS；真实 NAT 源比例未测 |
| ED-05 | BLOCKED | fork `CryptLayerSupported()=false`，能力缺口 |
| ED-06 | **PASS** | 222/209 双机重启 UserHash 不变，SecureIdent 状态持久化 |

**发布结论不变**：ED-01/02/04/05 未全部通过，P3 不发布，R-06 维持。

**后续**：① 为 ED-02 补可响应 KAD 节点（aMule/eNode-go 双端），把
`responses=0` 变为可复现的源返回；② ED-05 协议层已完成（见 §11），下一步是
接入 live TCP 连接并做仅混淆对端取证；③ 用真实 NAT 流量比例补 ED-04 的
「可见源下降」指标。

## 11. ED-05 混淆协议层落地（2026-11-23）

### 11.1 为什么不是纯环境阻塞

上一轮把 ED-05 判为 BLOCKED，依据是 fork 完全没有 CryptLayer/RC4 握手能力。
本轮从代码层补齐**协议实现**这一可控缺口。ED-02 的搜源零响应则经协议核对
确认是**环境层**：`FindNode=0x0B`、`FindValue=0x02`、`Store=0x04` 经
Kad 协议论文核对是 eMule 正确常量，UDP 请求编码无误；两机 `responses=0`、
`timeouts=41-46` 源于 nodes.dat 节点时效与 NAT/Firewalled 下双向 UDP 受限，
不通过篡改协议常量"制造"响应。

### 11.2 权威算法依据（三源交叉验证）

eMule basic obfuscation（client↔client TCP）的字节级规则，由以下独立来源
交叉核对一致后才实现，避免凭记忆写错常量：

| 项 | 值 | 来源 |
|---|---|---|
| `MAGICVALUE_REQUESTER` | `34` (`0x22`) | aMule `EncryptedStreamSocket.cpp:98`、eNode `ed2k/tcpcrypt.js` |
| `MAGICVALUE_SERVER` | `203` (`0xCB`) | 同上 |
| `MAGICVALUE_SYNC` | `0x835E6FC4` | aMule 源码、eNode 源码（eMule Wiki 所载 `0xA4815E5F` 与其冲突，以源码为准） |
| `ENM_OBFUSCATION` | `0` | eMule Wiki、aMule 源码 |
| 密钥输入 | `MD5(hash[16] || magic[1] || randomPart[4])` 共 21 字节 | aMule、eMule Wiki 均标 `Md5(...) 21` |
| RC4 初始化 | 丢弃前 `1024` 字节 | aMule 协议注释、eMule Wiki |
| A 发送帧 | `[marker明文][randomPart明文][SYNC+method+method+padLen+padding 加密]`（第 5 字节起加密） | aMule `SetConnectionEncryption`/`SendNegotiatingData(...,5)` |
| B 应答帧 | `[SYNC+method+padLen+padding]` 全加密 | aMule `Negotiate()` |
| marker 约束 | 不得等于 `0xE3/0xD4/0xC5` | aMule `GetSemiRandomNotProtocolMarker` |

密钥对称性：A 用目标 hash 派生（send=`0x22`、recv=`0xCB`），B 用自身 hash +
A 的 randomPart 派生（recv=`0x22`、send=`0xCB`）；A.send↔B.recv、
A.recv↔B.send 配对，无需交换 UserHash。

### 11.3 实现范围

新增 `internal/ed2k/protocol/obfuscation/`（fork 自维护，零第三方依赖）：

- `rc4.go`：`Cipher`（KSA+PRGA）、`Discard(1024)`；本地实现以固化丢弃行为、
  避开已弃用的 `crypto/rc4`。
- `keys.go`：`DeriveDirectionKeys(hash, randomPart, initiator)` 生成 (send, recv)
  密钥对，固定 21 字节 MD5 布局。
- `handshake.go`：`InitiatorRequest` / `ParseInitiatorRequest` /
  `ResponderReply` / `ParseResponderReply`，含 marker 校验、SYNC/method 校验、
  padding 一致性校验与明确错误类型。

`client.go`：
- `CryptLayerSupported()` 由 `false` 改为 `true` —— 仅表示**协议层能力已实现**。
- 新增 `CryptLayerWired() bool`（当前 `false`）—— 表示是否已接入 live TCP
  连接路径。二者分离，避免把协议层完成误读为 ED-05 PASS。

探针新增 `crypt_layer_wired` 字段；`crypt_layer_supported=true`、
`crypt_layer_wired=false` 如实反映当前状态。

### 11.4 回归测试

`obfuscation_test.go` 7 项全绿：

- `TestRC4KnownVectors`：3 组公开 RC4 标准向量（Key/Wiki/Secret）。
- `TestRC4DiscardMatchesDrop`：Discard(1024) 与逐字节消费等价。
- `TestDirectionKeysPairwise`、`TestKeyMaterialLayout`：密钥对称性与 21 字节布局。
- `TestFullHandshakeRoundTrip`：A↔B 完整握手 + 握手后载荷流往返（零网络）。
- `TestHandshakeRejectsBadFrames`、`TestPickMarkerAvoidsProtocolHeaders`：
  篡改检测与 marker 重试。

### 11.5 门禁判定（更新）

| 门禁 | 状态 | 依据 |
|---|---|---|
| ED-05 | **PARTIAL**（由 BLOCKED 推进） | obfuscation 协议层实现 + A/B 握手往返单测通过；`crypt_layer_supported=true`。但未接入 live TCP（`crypt_layer_wired=false`），未做仅混淆对端真机取证 |

**发布结论不变**：ED-01/02/03/04/05 均未全部通过，P3 不发布，R-06 维持。
把 ED-05 从"能力缺失 BLOCKED"推进为"协议层 PARTIAL"，不翻转发布结论。

**ED-05 剩余步骤**：① 把 `obfuscation.Cipher` 接入 `Connection` 的
DoRead/DoWrite 握手阶段，按 marker 判定明文/混淆；② 用 eNode-go/aMule 起
"仅混淆"对端做真实连接取证；③ 取证通过后才可将 ED-05 提升为 PASS、
`CryptLayerWired()` 置 true。
