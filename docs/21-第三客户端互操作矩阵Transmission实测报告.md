# 21 第三客户端互操作矩阵（Transmission）实测报告

> 文档编号：UD-DOC-21 ｜ 版本：v1.0 ｜ 状态：**草案（实测报告；证据已归档，待评审组复核）** ｜ 日期：2026-11-17 ｜ 维护责任：架构组 / QA ｜ 上游：[12](12-测试策略与测试计划.md) §3.1、[16](16-互操作性与兼容性设计.md) §4、[19](19-L4实验室实测与发布门禁报告.md)、[ADR-0021](adr/ADR-0021-L4门禁与P2P礼仪范围冲突裁决提案.md)

> **本报告是 BT-03 的补充证据，不是新的发布门禁。** BT-03 的官方判定仍由
> `make l4-run` 在 `BT-01..BT-14` 上给出（当前 14/14 PASS，见 [`19`](19-L4实验室实测与发布门禁报告.md)）。
> 本报告增加的是**第二个真实客户端家族**在同一隔离段上的双向实测：
> 此前 BT-03 只由 qBittorrent（libtorrent 系）支撑，Transmission 仅部署未实测。

## 一、结论摘要

2026-11-17T00:49:33Z，隔离段上以真实 `transmission-daemon 3.00 (bb6b5a062e)` 与 ud 引擎
完成**双向**交换，两向均为端到端 sha256 一致：

```text
MX-TR-A  Transmission 做种 → ud 下载           PASS
         sha256 700138ed…6533（期望一致）
         seeder uploadedEver 262352 ≥ 载荷 262144
MX-TR-B  ud 做种（ADR-0021 路径 A）→ 全新 Transmission 下载   PASS
         leecher downloadedEver 262144 = 载荷
         ud 观测到的 bytes_uploaded 262144（与 announce 同源）
         对端落盘文件 sha256 700138ed…6533（端侧校验一致）
```

- 报告产物：`ud.peer-matrix/v1`，`all_pass: true`，归档于
  [`docs/evidence/peer-matrix-transmission-2026-11-17/`](evidence/peer-matrix-transmission-2026-11-17/)。
- 载荷 256KiB 确定性数据，自建 chihaya tracker（HTTP announce），DHT/PEX/LPD 全关，
  与 L4 实验室的 BT-03 使用同一套隔离段与同一 tracker 口径。
- **两个方向都不是「同一个进程自连」**：做种端与下载端是不同主机上的不同进程，
  B 向在切换前**先停掉原始做种端**，确保字节只能来自 ud。

## 二、拓扑与环境

| 角色 | 地址 | 作用 | 关键版本 |
|---|---|---|---|
| 主机 A | 隔离段 `192.168.1.222/24`；控制面 SSH `192.168.34.222` | 运行 `udinteropx`（真实 BT 引擎）、chihaya tracker、`transmission-remote` 控制端 | Ubuntu 23.x，kernel `6.2.0-39-generic` |
| 主机 B | 隔离段 `192.168.1.221/24`（另有 `192.168.1.239/32`）；控制面 NAT SSH `112.5.60.4:54022` | 运行真实 `transmission-daemon`（RPC + peer 端口） | Transmission **3.00 (bb6b5a062e)**，Ubuntu 23.x |
| 控制面 | macOS 工作站 | 交叉编译 `udinteropx`、触发主机 A 命令 | Go 1.27.4，`linux/amd64`，`CGO_ENABLED=0` |

- Transmission 以独立配置目录启动（`-g`/`-w` 指向 `/tmp/udl4-tr-*`），RPC 与 peer 端口
  每次运行由主机 B 的空闲端口探测决定，避免与 qBittorrent 用例互相干扰。
- RPC 白名单显式放行主机 A（`-a 192.168.1.222,127.0.0.1`），并关闭认证（`-T`）
  ——仅在隔离段、单次运行内有效，运行结束即销毁目录与进程。

## 三、复跑方式

```bash
# 控制面：交叉编译并部署（主机 A 需要 chihaya、以及到主机 B 的免密 SSH）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/udinteropx ./cmd/udinteropx
scp /tmp/udinteropx root@192.168.34.222:/root/l4lab/bin/udinteropx

# 主机 A：跑双向矩阵（会自动起停 transmission-daemon 与 chihaya）
cd /root/l4lab && UD_L4_HOST_IP=192.168.1.222 \
  ./bin/udinteropx --peer-matrix transmission \
    --peer-matrix-out /root/l4lab/evidence/peer_matrix.json

# 或在控制面用 Makefile 目标（需本机可直连隔离段；CI 不运行）
make peer-matrix            # 等价于 --peer-matrix transmission
```

退出码含义：`0` = 两向都 PASS；`1` = 至少一向不是 PASS（`FAIL` / `UNSUPPORTED`），
报告 JSON 仍会写出，便于定位。**未执行不会被写成 PASS。**

## 四、两个方向的结果

### 4.1 MX-TR-A：真实 Transmission 做种 → ud 下载

| 项 | 值 |
|---|---|
| 载荷 | 262144 bytes，`mxtr.bin` |
| infohash | `44b946a5b551dcc6025bd1ea1b276af8411e71cc` |
| 做种端状态 | `seeding`，`percentDone=1`，peer 端口 `44402` |
| 做种端 `uploadedEver` | **262352**（≥ 载荷，含协议开销） |
| ud 下载结果 | sha256 `700138ed208215f2a2547149ef2c0d64c8c1efb0af613eb107cb2391ca376533`，与期望一致 |
| 判定 | **PASS** |

### 4.2 MX-TR-B：ud 做种 → 全新 Transmission 下载

| 项 | 值 |
|---|---|
| 前提 | ud 先经真实做种端完成下载，再以 ADR-0021 路径 A 保持受限做种会话 |
| 切换动作 | **停掉原始 Transmission 做种端**，随后启动全新实例做 leecher |
| leecher 结果 | `percentDone=1`，`downloadedEver=262144`（等于载荷） |
| 端侧校验 | 主机 B 上 `sha256sum` 落盘文件 = `700138ed…6533`（与源一致） |
| ud 侧计数 | `bytes_uploaded=262144`，与 tracker announce 同源 |
| 判定 | **PASS** |

> **为何要写「有界等待」**：`bytes_uploaded` 由传输层 200ms 统计节拍刷新，
> 真实 leecher 刚完成的瞬间采样会落后若干分片（首次运行即出现 65536 &lt; 262144 的
> 假失败）。现改为**有界轮询**（上限 30–45s，采样序列写入证据）等到计数覆盖载荷，
> 判据本身未改动：仍要求 `bytes_uploaded ≥ 载荷`，否则如实 FAIL。

## 五、与 BT-03 / L4 发布门禁的关系

| 关系 | 说明 |
|---|---|
| 判据 | BT-03 的 `BT-01..BT-14` 判据、注册表、门禁脚本**零修改** |
| 证据强度 | 本报告只提升「BT-03 由单一客户端家族支撑」这一表述的证据面，不改变 14/14 的既有结论 |
| 不得替代 | `make peer-matrix` 绿灯**不能**替代 `make l4-run`；`release_gate_met` 只由 `make interop-gate` 从 L4 证据判定 |
| 不接入 CI | 该目标需要隔离段双机与真实 Transmission，CI 环境不具备，故与 `l4-run` / `perf-lab` 同级：仅实验室手动运行 |
| 顺带修复 | 同一包内把 BT-02 / BT-03 的上传计数读取改为同一有界等待，消除官方 harness 的采样竞态（判据不变，见 §4.2 注） |

## 六、实现要点（均为实测踩坑，已固化进代码注释）

| 问题 | 现象 | 处理 |
|---|---|---|
| `-p` / `-P` 语义与直觉相反 | 用 `--port` 设 peer 端口会得到 usage 输出并退出 | Transmission 3.00：`-p` = **RPC 端口**，`-P` = **peer 端口** |
| RPC 白名单 | 默认只允许 `127.0.0.1`，主机 A 的 RPC 被拒 | `-a 192.168.1.222,127.0.0.1` 显式放行 |
| RPC 会话握手 | 首次请求返回 `409` + `X-Transmission-Session-Id` | 客户端保存并重试一次；把 `409` 当致命的实现永远连不上真机 |
| 清理命令自匹配 | `pkill -f /tmp/udl4-tr-*` 会匹配到 SSH 自身的命令行，把会话杀掉并静默返回空 | 只用 PID 文件清理，禁止按路径 `pkill` |
| 做种端比率限制 | 默认 `ratio-limit 2.0` 可能中途停止做种 | 启动时加 `-GSR`（不限全局比率） |
| 检查态被误判为做种 | `percentDone=1` 但 `status=checking` 时并未对外供种 | 判定要求 `percentDone≥1` **且** `status ∈ {seed-wait, seeding}` |

## 七、证据清单

| 文件 | 说明 | SHA-256 |
|---|---|---|
| `peer_matrix.json` | 两向判定与全部 evidence 字段（`ud.peer-matrix/v1`） | `ffa06e635badf21ae2eebb670ae1ff43ffc367dd07c1ea9cc67bdd1fb13230a9` |
| `SHA256SUMS` | 上述文件的校验和清单 | （清单不含自身） |

`dist/peer_matrix.json` 是每次运行的产物且不入库；**归档副本是唯一权威证据**。

## 八、未覆盖 / 不宣称

- **Transmission 4.x 未实测**：本报告只覆盖部署在实验室的 3.00。
- **uTP / MSE 单独矩阵未做**：本矩阵走 TCP + 默认加密偏好，未对 Transmission 侧单独
  构造 uTP-only 或 MSE-required 场景（L4 的 BT-10 覆盖我方默认 MSE 优先链路）。
- **未做抓包级 DHT/PEX 断言**：本矩阵全程关闭 DHT/PEX/LPD，私有种子行为由 BT-08 覆盖。
- **不宣称「与全部主流客户端实测互通」**：已实测的是 qBittorrent（libtorrent 系）与
  Transmission 两个家族；aria2 仅在回环 BX-01/BX-02 覆盖。
- **不改变序 6 状态**：技术验收项已满足，序 6 仍只差治理复签（见 [`14`](14-项目计划与风险管理.md) §11）。
