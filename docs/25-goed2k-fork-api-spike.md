# goed2k fork 源码 Spike（WBS 16 / ADR-0030 技术细化）

> 状态：**源码勘察报告，不是实现完成报告**。本文记录 2026-11-21 对
> `github.com/monkeyWie/goed2k` 真实源码的只读勘察结论，用于细化 ADR-0030
> 的 fork 改造范围。所有结论可由 `go mod download github.com/monkeyWie/goed2k@latest`
> 复现（pseudo-version `v0.0.0-20260602122456-f2a71d599dee`）。
> 门禁 ED-01/02/05/06 仍为 `NOT_EXECUTED`，本文不翻转任何发布结论。

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
