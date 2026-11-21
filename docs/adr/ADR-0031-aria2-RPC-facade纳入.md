# ADR-0031 aria2 RPC facade 纳入（WBS 17 OPEN-04 裁决）

- **状态**：Accepted（项目发起人 2026-11-21 指令「WBS 17 纳入，视为用户明确需求」；代行产品角色，非实名会签，依 `17` §1 P0 先例）
- **日期**：2026-11-21
- **决策者**：项目发起人（用户）在「纳入 / 先补记录 / 保持 L0」三选项中明确选择纳入
- **关联**：ADR-0019（RPC 三档）、ADR-0003（不捆绑 aria2c）、ADR-0013（多文件字段）、`07` §5 / §8、`12` §6、`14` WBS 17 / OPEN-04、`23`

## 背景

ADR-0019 把 RPC 兼容分 L0（自有 REST/WS）、L1（自写 aria2 facade）、
L2（捆绑 aria2c，已否决）三档，P0/P1 停在 L0，并把 L1 的启用条件写为
「有用户明确要接 AriaNg 时再上」，范围由产品在 P2 结束前决定（OPEN-04）。
截至 2026-11-21，P2 已收口、WBS 15 已落地，但 OPEN-04 未决且决策时点已过；
`docs/23` 曾把 AriaNg facade 列入「明确不做」。本次发起人明确指令启动 WBS 17，
构成 ADR-0019 所要求的「用户明确需求」，条件已满足。

**前置条件已全部具备**：ADR-0013 多文件 `ResourceFile`、L1 预留字段
（bitfield/numPieces/pieceLength/connections/followedBy/belongsTo）、
chunks 表 bitfield 编码规则（ADR-0020）、唯一 Limiter 的全局限速、EventBus WS 事件、
10 态任务状态机——即 ADR-0019 判断的「只需写映射层」。

## 决策

1. **启用 L1，opt-in**：新增 `rpc.enabled`（默认 false）。关闭时 `/jsonrpc`
   返回 404、`capabilities.rpcLevel` 保持 `"L0"`，行为与现状逐字节一致；
   开启后注册 `POST /jsonrpc` 与 `WS /jsonrpc`，`rpcLevel` 报 `"L1"`。
2. **只做 compat subset**：实现 `docs/07` §5.2 列出的 25 方法与 6 通知，
   **不承诺长尾 100% 兼容**；`system.multicall` 必须实现；未知选项接受但忽略；
   `saveSession` / `getSessionInfo` 由 SQLite 顶掉，no-op 返回 OK。
3. **零新依赖、零第二引擎**：纯 Go 映射层委托 `core.ControlPlane`，不捆绑
   aria2c（ADR-0003 / ADR-0019 L2 否决）、不引入自带引擎的 third-party 库。
4. **鉴权不退化**：`/jsonrpc` 纳入现有 fail-closed 鉴权路径；同时接受 aria2
   惯用的 `token:<secret>`（首参 / `jsonrpc` 字段前缀）与现有 Bearer，统一用
   `subtle.ConstantTimeCompare`；非回环无 token 必须拒绝。WS 鉴权复用一次性票据，
   不把长期 token 写进 URL。
5. **字段映射以 core 为唯一真值源**：`uploaded/downloaded/left` 取
   `tasks.bytes_*`（禁止各 Transport 自算）；`bitfield` 由 chunks 表按
   `ceil(numPieces/8)` 高位在前小写 hex 生成，HTTP 任务返回空串；状态机按
   6 态映射（waiting/active/paused/complete/error/removed）；GID 为 16 位 hex，
   与内部 ULID 建双向映射表。
6. **验收**：以 AriaNg 为兼容性测试套件跑通 `docs/07` §5.5 全部交互，
   映射层逐项单测；`docs/07` §8 契约矩阵从「未实现」更新为「L1 opt-in」。
7. **关闭 OPEN-04**：结论为「纳入 L1」，不再列为未决事项；`docs/23`
   「明确不做」清单移除该项并记本 ADR 时点。

## 备选方案

- **保持 L0**（否决）：架构最简，但已具备全部前置、发起人已明确要求，
  继续不做等于无视指令与 ADR-0019 的触发条件。
- **L2 捆绑 aria2c**（否决）：ADR-0003 已否决，违反零 sidecar 基线。

## 后果

正面：解锁 AriaNg / 浏览器扩展生态；零新依赖；控制面兼容不影响已通过的 L4 P2P 门禁。
负面：25+ 方法 + 6 通知 + 选项翻译表的长期维护；两套控制面需双份映射；
新增 token 入口扩大鉴权面（由决策 4 缓解）。

## 合规与验证

- 契约测试：`/jsonrpc` 404（关闭态）、方法白名单、multicall、GID 双向映射、
  状态映射、bitfield 编码、未知选项忽略、鉴权 fail-closed、WS 票据。
- AriaNg 真机交互验收记录归档 `docs/12`；未执行时如实标注。
