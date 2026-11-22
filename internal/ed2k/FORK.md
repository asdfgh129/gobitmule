# goed2k fork（WBS 16 / ADR-0030）

本目录是 `github.com/monkeyWie/goed2k` 的自维护 fork，用于 ud 的 eD2k 引擎接入。

## 来源与许可

- 上游：`github.com/monkeyWie/goed2k`，pseudo-version
  `v0.0.0-20260602122456-f2a71d599dee`，MIT，Copyright (c) 2026 monkeyWie。
- 原 LICENSE 见本目录 `LICENSE`，MIT 声明随 fork 保留（ADR-0030 第 4 条）。
- 源码取自本机 Go module cache 的只读副本，非网络实时拉取。

## fork 改造（相对上游）

1. **剥离 TUI**：上游 `cmd/` 与 `examples/` 依赖 bubbletea/bubbles/lipgloss
   整套 TUI；本 fork 只保留核心库（`*.go` + `data/` `disk/` `internal/` `protocol/`），
   零第三方依赖进入 ud 依赖树。`cmd/` 与 `examples/` 不复制。
2. **改 import 路径**：`github.com/monkeyWie/goed2k` →
   `github.com/asdfgh129/ud/internal/ed2k`。
3. **移除上游测试文件**：上游 `*_test.go` 含真实网络集成测试，不进 fork；
   ud 侧测试走 `internal/transport/ed2k` 的 Downloader seam（CI 零网络）。
4. **待接入时的隔离**（ADR-0030 / docs/25 §3）：生产 Downloader 必须关闭库内
   限速（`MaxUploadRateKB=0`）与 AutoSave 持久化（`SetAutoSaveInterval(0)`），
   避免与 ud 唯一 Limiter / SQLite 单一真相双写；CryptLayer/SecureIdent/KAD
   `nodes.dat` 路径显式配置（ED-05/ED-06 验收对象）。

## 门禁边界

本 fork 落地本身不翻转任何发布结论。ED-01/02/05/06 需在隔离实验室真实互通
并取得证据后方可判定；未执行前如实标注（docs/12 §3.2、docs/25）。
