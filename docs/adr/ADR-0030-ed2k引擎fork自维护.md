# ADR-0030 ed2k 引擎 fork 自维护（WBS 16 OPEN-03 裁决）

- **状态**：Accepted（项目发起人 2026-11-21 指令「WBS 16 直接 fork 自维护」；代行架构角色，非实名会签，依 `17` §1 P0 先例）
- **日期**：2026-11-21
- **决策者**：项目发起人（用户）在「fork / pin / 仅骨架」三选项中明确选择 fork
- **关联**：ADR-0005（goed2k 选型）、ADR-0013（Identity 冻结字段）、ADR-0002（单调度器）、`12` §3.2 ED-01/02/05/06、`14` WBS 16 / R-06 / OPEN-03、`10` §4

## 背景

ADR-0005 已选 `github.com/monkeyWie/goed2k`（MIT，纯 Go）作为 eD2k 引擎，
但把 OPEN-03「是否 fork 自维护」留给 P3 启动前决策。WBS 15（Telegram）已落地，
P3 推进至 WBS 16 时该决策成为阻塞项。

**实测勘察（2026-11-21）**：上游仓库 8 stars / 3 forks / 7 commits，
创建 2026-03-13、最近提交 2026-06-02，无 tag（仅 pseudo-version），单一维护者
（bus factor = 1）。公共 API 为**任务型**：`NewSettings` → `NewClient` → `Start`
→ `ConnectServers` → `AddLink` → `Wait`，库自带任务调度、状态持久化与恢复、
进度订阅。这与 ud 的 `Transport` 契约（`Probe` + `Run(ctx, r, w ChunkWriter, b Budget)`，
分片与状态由唯一调度器持有，ADR-0002）**模型错配**：若直接依赖，需把库下载到临时目录
再读回 `ChunkWriter`，造成双重磁盘 IO、库侧限速与 ud Limiter 并行、状态双写。
此外 README 未记载 AICH / Secure Ident / CryptLayer 混淆 / LowID / Collection /
`nodes.dat`，而这些正是 ED-05 / ED-06 的验收对象。

## 决策

1. **fork goed2k 到本组织自维护**（模块路径以 fork 后为准），pin 到固定 commit。
   在 fork 内完成：① 关闭库内调度与持久化，改为暴露流式/逐块读取钩子以对接
   `ChunkWriter`；② 显式暴露并开启 CryptLayer 混淆、Secure Ident、AICH、
   `CreditsOnlyVerified=false` 等开关；③ RSA PEM（0600）与 `server.met`/`nodes.dat`
   路径可配置；④ 补齐上游缺失的互操作修正。MIT 声明随 fork 保留。
2. **Identity 字段零新增**：沿用 ADR-0013 冻结的 `ED2KFileHash / AICHRoot /
   UserHash / IdentKeyRef / CreditsRef` 与 P0 已建 DDL 列，`Resource` 结构与 API
   schema 不变。
3. **分层落地**：先做无网络的 `resolve/ed2k.go`（解析 `ed2k://|file|name|size|hash|/`
   → `KindED2K` Resource + `Probe`）与单测；Transport 实接在 fork 源码 spike
   确认钩子后推进。
4. **门禁如实**：ED-01/02/05/06 需在隔离网络（aMule + eNode-go + Kad `nodes.dat`
   + TCP 4662/UDP 4672 入站）执行；未过则 P3 不得发布（R-06）。CI 无真实 eD2k
   环境时仅跑离线单测，`release_gate` 不因本包翻转。
5. **关闭 OPEN-03**：结论为「fork 自维护」，不再列为未决事项。

## 备选方案

- **直接依赖 pin**（否决）：零维护成本，但任务型 API 与 ChunkWriter 契约冲突、
  开关缺失，实测暴露冲突后仍需 fork；把决策推迟只是把成本后移。
- **不做 eD2k**（否决）：R-12 判定投入产出比低，但 WBS 16 已在 P3 范围且
  ADR-0005 已批准选型，发起人明确要求启动。

## 后果

正面：契约适配与互操作缺陷可自主修复；行为锁定便于 ED 回归；不引入第二个调度器。
负面：承担 eD2k 协议长期维护（资料零散、上游低活跃）；需 eD2k 专长；MIT fork
需保留声明；与上游分叉后升级困难。

## 合规与验证

- fork 内所有改动保留上游 LICENSE 与版权声明；`tools/gates.py` 许可证门禁须 0 copyleft。
- 离线：ed2k resolver 单测（解析/拒绝/Identity 字段）、`make gates-strict` 全绿。
- 真实：ED-01/02/05/06 在隔离实验室执行并归档证据；未执行前如实标 `NOT_EXECUTED`。

## 落地补记（2026-11-21 源码 Spike）

`docs/25-goed2k-fork-api-spike.md` 对上游源码（pseudo-version
`v0.0.0-20260602122456-f2a71d599dee`）做了只读勘察，细化本决策：

- **fork 必要性成立**（bus factor=1、无 tag、ED-05/06 开关缺失），不撤销。
- **改造量低于初判**：发现 `internal/transport/bt` 已建立「第三方引擎下载到临时
  目录 → reader 按 ud chunk 边界读回喂 `ChunkWriter`」的适配模式；goed2k 提供
  `AddTransfer(Handler)` / `Wait()` / `ReadRange()`，可用同构薄适配层对接，
  **不必改写 `PieceManager`、不触碰 ADR-0002 单一调度器不变量**。故本决策第 1 条
  中「关闭库内调度改为暴露流式钩子」细化为「以 reader 模式包装，并隔离库内自
  持久化/限速」。
- **门禁可达性**：实验室 `222`/`221` 无 Go、无 eD2k 客户端、隔离段无外网；
  `209` 拒绝当前 SSH 密钥。ED-01/02/05/06 仍 `NOT_EXECUTED`，需补客户端 + KAD
  `nodes.dat` + 端口入站 + 209 凭据后方可执行，不因本 spike 翻转。
