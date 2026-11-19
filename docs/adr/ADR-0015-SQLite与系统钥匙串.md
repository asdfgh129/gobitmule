# ADR-0015 状态用 SQLite（modernc，纯 Go），凭据存系统钥匙串

- **状态**：Accepted（2026-11-09 P0 里程碑评审批量批准）
- **日期**：2026-11-08
- **决策者**：架构组 + 安全
- **关联**：`06`、`11` §3、`13` §8

## 背景

单调度器的直接要求是**状态只有一份真值源**。若让各 Transport 自管断点（如 `.aria2` 控制文件）就会出现双真值源；同时凭据（Cookie、Telegram session、代理密码、PT passkey）绝不能随 DB 一起被复制、备份或打印。

## 决策

1. 主存储采用 **SQLite + `modernc.org/sqlite`（纯 Go、免 cgo、BSD）**，WAL 模式、`foreign_keys=ON`、`busy_timeout`。
2. 表结构见 `06` §3：`tasks / resources / files / mirrors / protocol_identity / checksums / chunks / task_options / events / sidecars / app_meta`。`chunks` 是统一分片真值表（HTTP Chunk 与 BT Piece 同表）。
3. 事务不变式：**状态迁移 + 分片进度更新**在同一事务；完成提交与最终路径写入在同一事务。进度批量刷盘（500ms 或 4MB 聚合），不每片一事务。
4. **凭据一律存系统钥匙串**（Keychain / Credential Manager / libsecret），DB 与日志只存 `credential_ref`；Telegram session / eD2k RSA 私钥存 0600 文件（必须持久化，否则重启换身份、积分归零）。
5. 崩溃恢复只信任 `chunks` 表 + 实际 `.part` 文件校验结果，不信任内存态；`assigned` 分片一律回 `pending`（`06` §5）。
6. 迁移用 `app_meta.schema_version` 单调递增 + 幂等脚本；升级前自动备份，无回滚脚本时拒绝启动。
7. 日志、事件、REST 响应统一过 `security.Redact`；passkey 不进日志、不进事件、不跨任务同步。

## 备选方案

| 方案 | 结论 |
|---|---|
| `mattn/go-sqlite3`（cgo） | ❌ 破坏 `CGO_ENABLED=0` 与移动端交叉编译 |
| 各协议自带断点文件（`.aria2` 等） | ❌ 双真值源，与单调度器冲突 |
| 凭据加密后存 DB | ❌ 备份/复制扩散风险，密钥管理仍回到系统钥匙串 |
| KV 存储 / 自研文件格式 | ❌ 缺事务与查询能力，分片状态一致性难保证 |

## 后果

**正面**：单一真值源、事务化状态迁移、崩溃可恢复、纯 Go 可交叉编译、`udctl backup` 单文件即可。

**负面**：`chunks` 是高频写表，需批量刷盘与 WAL 调优；1000 任务约 20 万行需索引与保留策略；凭据与 DB 分离使备份/恢复多一步（需用户单独导出钥匙串）。

## 合规与验证

- `12` 故障注入：`kill -9` 后已落盘分片 100% 复用、无重复下载、无损坏；`.part` 被外部篡改时只重下该片。
- 全量扫描 DB、日志、事件、REST 响应，无 `cookie|authorization|passkey|session` 明文（`11` §9）。
- `12` 发布门禁校验 `app_meta.schema_version` 迁移链与回滚脚本。

## 实现落地注（2026-11-13，不修改上述已批准决策）

`busy_timeout` 仍是 DSN 的一部分，但 WAL 下的写-写竞争可能返回 `SQLITE_BUSY_SNAPSHOT`，
SQLite 明确**不为该错误调用 busy handler**（CI run `43393512271` 的 `cover` 步骤曾因此
偶发失败，严格门禁当时为 5 PASS / 0 WARN / 1 FAIL）。`internal/store.withTx` 因此在
**不改动**第 1–7 条决策、DDL 与事务不变式的前提下增加有界事务重放：唯一写锁串行化 +
`SQLITE_BUSY` / `SQLITE_LOCKED` 最多重放 6 次（5ms 递增退避），每次重放前事务已回滚、
回调无 DB 外副作用；永久锁库仍 fail loud。这是**实现层加固，不是决策修订**，
该失败已由 CI run `43399289527` 复跑 `quality: success` 闭环。
详见 `06` §5 / §7 / §10、`12` §9 与 CHANGELOG Fixed。

## 实现落地注（2026-11-17，落实决策 ③，不修改上述已批准决策）

决策 ③ 要求「进度批量刷盘（500ms 或 4MB 聚合），不每片一事务」。此前实现是**每片一事务**
（`WriteChunkDone`：`UPDATE chunks` + 摘要行 + `bytes_done` 增量三语句一个事务），
`06` §10 的「P0 偏差」条如实登记过这一点。本注记录该条落地的实际做法与边界：

- **缓冲**：`scheduler.chunkWriter` 在内存持有分片完成记录（分片、摘要、镜像序号），
  `.part` 字节仍立即写入；累计 **4MB** 或距上次刷盘 **500ms**（定时器）时触发一次刷盘，
  `Sync()` / `Close()` 与 `Verified()` 强制先刷盘。
- **一次事务**：新增 `store.WriteChunkDoneBatch`，把 N 条记录的 `state='done'`、
  摘要行 upsert 与**累加后的一次** `bytes_done` 增量放进同一事务；任一条分片不是 `assigned`
  则整体回滚（不做部分提交），跨任务混批直接拒绝。单条路径 `WriteChunkDone` 等同「批量 = 1」，
  保证恢复路径与测试只有一条代码路径。
- **崩溃窗口（必须如实标注）**：`kill -9` 时最多丢失**一个刷盘窗口**的完成记录
  （≤500ms 或 <4MB 的分片）。这些分片在 DB 里仍是 `assigned`，恢复按 `06` §5 回 `pending`
  重下——代价是一次重复下载，**不会**出现 `bytes_done` 与 `chunks` 表不一致（两者同事务），
  也不会损坏 `.part`。这与决策 ⑤「不信任内存态」一致。
- **顺序约束**：`Verified()` 是 `done → verified` 的迁移，必须先刷掉该分片的完成记录再迁移；
  否则 `UPDATE ... WHERE state='assigned'` 匹配不到，会报假的「not in assigned state」。
  该缺陷在实现中真实出现过，已由 `TestChunkWriterVerifiedFlushesBufferedCompletionFirst` 固定。
- **验证**：`store` 两条（批量原子性 / 跨任务拒绝）+ `scheduler` 四条（Sync 前不提交、
  字节阈值触发、定时器触发、verified 先刷盘）；真实路径用 `make perf` 的 1GiB / 4096 分片
  Range 下载复核（1GiB 全量 sha256 通过、`chunks_verified=4096`）。
- **不宣称吞吐提升**：本机回环 `make perf` 的 MB/s 波动达 ±3 倍（同机连续三次 309 / 702 / 244 MB/s），
  不足以支撑任何性能结论；本项改动的目的是**落实决策 ③ 的事务粒度**，不是性能声明。
- **仍未落实**：决策 ④ 的系统钥匙串适配器（当前只有 `MemoryCredentialStore`）与 `06` §7 的
  `events` 保留清理，均仍为开放项。

## 实现落地注续（2026-11-17，`06` §7 的 `events` 保留清理；不修改上述决策）

上一条落地注末尾把「`06` §7 的 `events` 保留清理」列为仍未落实项，本注关闭该条：

- `store.PruneEvents(ctx, beforeTS, maxRows)`：**一个事务**内先删 `ts < beforeTS` 的过期事件、
  再按主键把表削到最新 `maxRows` 条（用 `ORDER BY id DESC LIMIT 1 OFFSET maxRows-1`
  定位保留边界，不做 `NOT IN` 全表重扫），返回 `DeletedByAge` / `DeletedByCount` / `Remaining`。
  两个上限独立生效，`0` 表示该半条策略关闭。
- `internal/app/janitor.go`：daemon 启动时先清一次（长时间未启动的实例不必等一个节拍），
  之后按 `events.pruneInterval` 执行；清理失败只记 warn 并在下一节拍重试，**不阻塞数据面**。
  关闭顺序：janitor 的 stop defer 晚于 `st.Close()` 注册，因此先停 janitor 再关库。
- 配置：`events.retentionDays`（默认 7）/ `events.maxRows`（默认 100000）/
  `events.pruneInterval`（默认 1h）。**两者都为 0 时 `Validate` 拒绝启动**——
  无界审计日志是磁盘风险，按 fail-closed 处理，与决策 ⑤「不信任内存态」同一取向。
- 验证：`store/events_prune_test.go` 5 条（按时间 / 按条数 / 先时间后条数 / 三种空操作 /
  取消上下文不产生部分提交）、`app/janitor_test.go` 3 条（注入时钟的保留判定、
  启动即清且可停止、失败只报错不致命）、`config/events_test.go` 9 个用例。
- **仍未落实**：决策 ④ 的系统钥匙串适配器（当前只有 `MemoryCredentialStore`），
  以及 `06` §10「P0 偏差」里 `task_options` / `resources.headers` 的 500ms 聚合。

## 实现落地注（2026-11-17，落实决策 ④，不修改上述已批准决策）

决策 ④ 要求「凭据一律存系统钥匙串，DB 与日志只存 `credential_ref`」。本注记录三平台
**零 cgo** 适配器的实际做法与边界（发起人已确认「零 cgo 三平台适配」路线）：

- **macOS（`macos-keychain`）**：`/usr/bin/security` CLI。Security.framework 需要 cgo，
  被 ADR-0001 排除；CLI 覆盖同一把登录钥匙串。写入走 `security -i` 把整条命令经
  **stdin** 送入——Apple 自文档标注 `-p/-w` 走 argv 不安全（`ps` 全用户可见），而
  「`-w` 置尾提示输入」需要 TTY，守护进程没有；退出码实测稳定（0 成功 / 44 条目不存在 /
  45 重复 / 2 用法错误），44 用于区分 not-found 与真故障。
- **Windows（`windows-dpapi`）**：纯 Go `syscall` 调 `crypt32.dll` 的
  `CryptProtectData` / `CryptUnprotectData`（恒置 `CRYPTPROTECT_UI_FORBIDDEN`），
  密文落 `<state>/credentials/<id>.bin`。选 DPAPI 而非 Credential Manager：后者
  generic blob 上限 5×512 字节，装不下 PEM 私钥。
- **Linux（`linux-secret-service`）**：`secret-tool`（libsecret-tools CLI）。
  原方案为直接实现 D-Bus Secret Service 协议（godbus）——**变更**为 CLI，理由：
  不重造协议、零新依赖、与 macOS 同构可审；代价是要求宿主装 `libsecret-tools` 且有
  session bus，二者缺一即降级 `file`。
- **降级（`file`）与兜底（`memory`）**：`file` 是 0600 文件 + 0700 目录，
  **显式不是钥匙串**（无 ACL、无 OS 加密），`GET /capabilities` 的 `credentials`
  字段如实上报，绝不能把它当钥匙串宣传；`memory` 重启即失，仅测试与无状态目录宿主。
- **共用契约**：ref `<scheme>://ud/cred/<32hex>` 由铸造后端校验前缀与 id 形状；
  `Put` 拒空 / 拒控制字符 / 限 64KiB；`Delete` 幂等；锁钥匙串返回后端错误而非
  not-found；读取为每任务一次（`Run`/`Probe`/`injectHeaders`），不进分片循环。
- **验证边界（如实）**：macOS 为**真机验证**——空格/引号/反斜杠往返、同 id 替换（`-U`）、
  删除后 44、stdin 非 argv 由源码断言固定（`internal/security/keychain_darwin_test.go`）。
  Windows / Linux 仅交叉编译（`CGO_ENABLED=0` 六目标全过）+ 单测，**真机路径
  `NOT_EXECUTED`**；无 Linux 桌面会话可验证 Secret Service，故不宣称其可用。
  **2026-11-19 时点补记（不修改决策正文）**：Windows 10 x64 真实 DPAPI syscall 五项 PASS
  （两项 POSIX 权限用例如实 SKIP）；Linux 亦已在 Ubuntu 23.04 主机上以真实 D-Bus session +
  GNOME Keyring + `secret-tool` 验证 5/5 PASS（后端选择、往返、OS 钥匙串中实际存在/删除消失、
  not-found 与幂等、id 隔离、stdin 不进 argv），证据
  `docs/evidence/linux-keychain-2026-11-19/keychain_linux_real_machine.txt`。上述旧句为
  2026-11-17 时点值，保留不改；依赖权限位拒绝的用例在 root 宿主仍 `NOT_EXECUTED`。
- **代价如实**：macOS 每次 helper 调用 ≈0.5s（9 次调用 4.24s 实测），属任务启动成本；
  不做进程内缓存——按次直读让「钥匙串里删除凭据」立即生效，缓存会引入撤销语义问题。
- **仍未落实**：`06` §10「P0 偏差」里 `task_options` / `resources.headers` 的 500ms 聚合。


## 实现落地注终（2026-11-18，`06` §10 P0 偏差最后一条；不修改上述已批准决策）

上一条落地注的「仍未落实」为 2026-11-17 时点值，本注关闭最后一项：决策 ③ 要求的
**批量写**不止 `chunks`，还覆盖 `task_options` / `resources.headers`。

- **核查结论（如实）**：P0 基线条文写「每片与分片状态在同一事务提交」，但自初始提交起
  生产代码从未在分片热路径重写这两类元数据；实际写点是 `PATCH /tasks/{id}`（`SaveOptions`）、
  运行期键值（`SetOption`）与解析完成（`SaveResource`）。因此不是「再优化一次分片写」，
  而是把真实的元数据写纳入 500ms 上限的批量事务。
- **实现**：`internal/store/metabatch.go` 的 `metaBatcher.submit` 按
  `options:<taskID>` / `option:<taskID>:<key>` / `resource:<resourceID>` 去重（同一 key
  的新写覆盖旧写），一个窗口内的所有写由 `flush` 在**一个事务**提交。窗口自首笔起上限
  **500ms**；单笔空闲写在 **2ms settle** 后提交，不给单次 PATCH 强加半秒延迟；
  满 **64** 笔或 `Store.Close()` 立即排空。
- **不变式**：每个调用方同步等待 COMMIT 结果——PATCH 返回 200 即已落库；事务失败返回给
  该批全部调用者；某一请求取消只结束它自己的等待，不取消共享事务（与
  `chunkWriter.flushPending` 同一取向）。`§5`「状态迁移 + 分片进度同事务」、
  `§6` 敏感头剥离（批量路径同样调用 `security.StripSensitiveHeaders`）均不受影响。
- **验证**：`store/metabatch_test.go` 8 例（窗口合并 / 单笔快速提交 / 去重 last-writer-wins /
  错误传播 / 取消隔离 / 条数上限 / Close 排空 / 敏感头剥离）与
  `scheduler/chunkflush_test.go:TestChunkWriterDoesNotWriteMetadata`（分片热路径零元数据写）。
  **不宣称吞吐提升**；本机覆盖率 `internal/store` 86.6%（阈值 80%），
  `go test -race ./...` 与 `make gates-strict` 全绿。
- **ADR-0015 实现债状态**：决策 ①②③④ 及 `06` §7 / §10 的 P0 偏差项**全部关闭**。
  仍未补的是非本 ADR 的项：`12` §5 的 ffmpeg 两项（随 P2）、移动端 BT 内存（需真机）、
  实名评审会签（非技术侧可产出）。
