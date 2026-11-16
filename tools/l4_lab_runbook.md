# L4 实验室搭建 Runbook（docs/18 §七 的可执行落地）

> 状态：**runbook，不是执行报告**。本文描述怎么搭，不描述已经搭好。
> 任何环节完成与否以 `make l4-preflight` 实测与归档证据为准；
> `PRESENT` ≠ PASS，签署文件 ≠ 实测达标。

## 0. 角色与决策记录

- 2026-11-16 项目发起人指令：**判据不动（`12` §3.1 维持原样），搭真实双机实验室**；
  本机允许安装实验室软件。
- 拓扑按 `docs/18` §7.1 简化为**双机直连/隔离交换机**（2 节点），抓包在主机 U 本机网卡
  进行（双机直连时 U 的 NIC 能看到全部双向流量；如经交换机且需第三方视角，
  再补 SPAN 口并归档说明）。该简化属于拓扑**事实**，须如实写入
  `UD_L4_TOPOLOGY_ATTESTATION`，不得谎称「五节点 SPAN 拓扑」。

| 节点 | 角色 | 承载 |
|---|---|---|
| 主机 U（macOS，本机） | 被测 | `udd` + **14 条**真实 harness 驱动 + `tcpdump` 抓包 + NFR-PERF-01 客户端侧 |
| 主机 A（Linux，192.168.1.222） | 对端 | `qbittorrent-nox`（BT-01/02/13）+ `transmission-daemon`（BT-03 之一半）+ `chihaya`（BT-05/06/07）+ NFR-PERF-01 服务端侧 |
| NAT 主机（Linux，192.168.1.221） | 对端/隔离段 | 第二台真实客户端 + iperf3 对端；与主机 A 同处 `192.168.1.0/24`，延迟约 0.36ms |

> libtorrent 2.1.1（Python 绑定）在本机已有，作为 BT-03 的 libtorrent 系对端之一；
> `LAB-CLIENT-LT`（双向会话）仍需抓包佐证后人工签署。

## 1. 网络与时间

1. 双机经隔离交换机或网线直连，**无外网路由**；建议静态地址
   `10.66.0.1/24`（U）/ `10.66.0.2/24`（A）。
2. 千兆验证：U 侧 `ifconfig <iface> media`、A 侧 `ethtool <iface> | grep Speed`，
   两边均须 `1000baseT/1000` 及以上，截图/输出归档 `dist/l4_lab/topology/`。
3. 时间同步：双机 `chrony`（A 由 setup 脚本安装；U 用 macOS 自带 `timed` 或指向 A 的
   chrony），执行前记录两侧 `date -u` 差值 < 1s。

## 2. 软件清单与核验

| 条件 | 位置 | 核验命令 | 对应 LAB |
|---|---|---|---|
| qbittorrent-nox | 主机 A | `qbittorrent-nox --version` | `LAB-CLIENT-QB` |
| transmission-daemon | 主机 A | `transmission-daemon --version` | `LAB-CLIENT-TR` |
| chihaya | 主机 A（跨机用）/ 主机 U（回环用） | `chihaya --help` | `LAB-TRACKER` |
| tcpdump + BPF 特权 | 主机 U（抓包机角色） | `sudo tcpdump -D` | `LAB-CAPTURE` |
| 真实 external config | 主机 U 仓库内 | 见 §4 | `LAB-CONFIG` |
| 拓扑签署 | 人工 | `UD_L4_TOPOLOGY_ATTESTATION` JSON | `LAB-TOPOLOGY` |
| 评审签署 | 已按发起人指令记录（`docs/18` §六） | `UD_L4_REVIEW_SIGNATURE` JSON | `LAB-REVIEW` |
| 双机基准 | 人工实测 | `UD_L4_PERF_ATTESTATION` JSON | `LAB-PERF` |

主机 A 一键部署（**已确认主机 A = Ubuntu Server 23.x，2026-11-16**）：
把 `tools/l4_lab_setup_hostA.sh`、`tools/chihaya_lab.yaml` 与
`~/l4lab/bin/chihaya-linux-{amd64,arm64}` 拷到主机 A 同一目录后执行
`bash l4_lab_setup_hostA.sh`。脚本会：① 检测 23.04/23.10 已 EOL 并把 apt 源
自动切到 `old-releases.ubuntu.com`（原文件留 `*.bak`）；② apt 安装
qbittorrent-nox / transmission-daemon / tcpdump / chrony；③ 优先安装随附的
**预编译 chihaya**（macOS 交叉编译、构建口径见 §5），找不到才回退本机 go 构建。
其它发行版按包管理器等价替换。
主机 U 已就位：`~/l4lab/bin/chihaya`（构建口径见 §5）。

## 3. 已知事实（2026-11-16）

- 本机 sudo 被运行环境禁止 ⇒ brew/qbittorrent-nox 无法装入 macOS 本机；
  qBittorrent 固定跑在主机 A（Linux 版本恰好是 `qbittorrent-nox` 官方形态）。
- `internal/interop/preflight.go` 报告头部的 `ReviewBlocker` 提案期口径已于 2026-11-16
  校准为「ADR-0021 已 Accepted（路径 A，R5 复签）+ R6 翻转 `BT-02`/`BT-11`」，
  不再写「Proposed，未生效」。若见到旧 `dist/l4_preflight.json` 中的提案期字符串，
  属历史时点证据，重跑 `make l4-preflight` 即以新文案覆盖。

## 4. 执行顺序（对应 docs/18 §7.4）

1. 主机 A 部署 + 网络隔离 + 时间同步（本文 §1/§2）。
2. **实装 14 条真实 harness**（下一编码包；本 runbook 就绪前 `make l4-run` 只会 SKIPPED）。
3. 编写真实 `l4_external_config.json`（覆盖 `BT-01..BT-14` 共 **14 条**；2026-11-16 起
   `BT-02` / `BT-11` 已翻转为 `gateExternal`，**必须出现在声称覆盖的配置里**，
   由真实 harness 判定、仅 verified upload 才 PASS）。
4. `UD_L4_TRACKER=~/l4lab/bin/chihaya`（U 侧回环项）+ 主机 A 侧 chihaya 跨机项，
   `sudo make l4-preflight`（root 使 `LAB-CAPTURE` 可转 PRESENT）→
   `sudo make l4-preflight-check`。
5. 归档三个签署 JSON（topology / review / perf）→ `make l4-run
   --require-external --external-config <真实配置> --release-gate-only`，
   证据落 `dist/l4_lab/`。
6. `make l4-audit` 复核完整性（`CONSISTENT` ≠ 通过）→ 人工复核 §7.3 留痕
   → 只有 14/14 PASS 且 NFR-PERF-01 达标才能进入序 6 收口评审。

## 5. chihaya 构建口径（如实记录）

- 上游 commit `2f794401e4`（2023-06-15，main 分支）。
- **补丁（唯一）**：删除 `cmd/chihaya/config.go` 中 `_ "github.com/chihaya/chihaya/middleware/jwt"`
  这一行 blank import。原因：JWT prehook 的依赖 `SermoDigital/jose` 在 `init` 里向
  stdlib `crypto` 注册 SHA3 哈希，Go 1.24 起注册表已移除这些常量，**任何 Go ≥ 1.24
  构建的 chihaya 启动即 panic**。实验室不使用 JWT prehook，剔除后 announce / scrape /
  UDP 协议逻辑零改动。
- 本机构建命令（Go 1.27.4 包装脚本）：
  `GOBIN=~/l4lab/bin ~/.../toolchain@v0.0.1-go1.27.4*/bin/go install ./cmd/chihaya`（在补丁后源码树内）。
- 冒烟证据（2026-11-16，本机回环）：HTTP announce / scrape 均 `200` + 合法 bencode
  （假 infohash 被正确拒绝 `d14:failure reason25:provided invalid infohashe`），
  UDP frontend 正常启动；配置模板 `tools/chihaya_lab.yaml`。
- `/tmp` 易失：`/tmp/udbin/chihaya` 只是过渡副本，权威位置 `~/l4lab/bin/chihaya`；
  主机 A 重建按本节口径执行。

## 6. 环境变量速查（主机 U 执行侧）

```bash
export UD_L4_TRACKER="$HOME/l4lab/bin/chihaya"          # 本机回环 tracker（BT-05/06/07 部分）
export UD_L4_QBITTORRENT="$HOME/l4lab/bin/ud-l4-qbittorrent-remote"   # 远程控制 wrapper（harness 包交付）
export UD_L4_TRANSMISSION="$HOME/l4lab/bin/ud-l4-transmission-remote" # 同上
export UD_L4_TOPOLOGY_ATTESTATION="dist/l4_lab/topology_attestation.json"
export UD_L4_REVIEW_SIGNATURE="dist/l4_lab/review_signature.json"
export UD_L4_PERF_ATTESTATION="dist/l4_lab/perf_attestation.json"
# 抓包：sudo 执行 preflight / l4-run，或在实验室授予当前用户 BPF 权限
```

> wrapper 与 14 条 harness 属下一个编码包交付物；在它们存在之前把上述变量指向
> 不存在的文件只会得到诚实的第一手 `MISSING`，这正是 preflight 的用途。
