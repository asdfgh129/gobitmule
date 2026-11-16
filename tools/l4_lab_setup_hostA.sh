#!/usr/bin/env bash
# L4 实验室第二台主机（主机 A，Debian/Ubuntu）一键部署脚本。
#
# 角色分工见 tools/l4_lab_runbook.md：
#   主机 U（macOS，本仓库所在机器）：被测 udd + harness 驱动 + 抓包
#   主机 A（本脚本目标机）        ：真实 qBittorrent-nox / Transmission-daemon / chihaya
#
# 前提：以具备 sudo 的用户在主机 A 上执行；机器已在隔离网段（无外网路由），
# 如无外网需先配置内网 apt 镜像源。本脚本只装实验室软件，不改网络配置。
set -euo pipefail

echo "== [1/5] 基础工具与时间同步 =="
sudo apt-get update
sudo apt-get install -y curl ca-certificates chrony tcpdump ethtool bencode-tools 2>/dev/null || \
sudo apt-get install -y curl ca-certificates chrony tcpdump ethtool
sudo systemctl enable --now chrony || sudo systemctl restart chrony

echo "== [2/5] 真实客户端：qbittorrent-nox + transmission-daemon =="
sudo apt-get install -y qbittorrent-nox transmission-daemon
# 实验室专用：不为桌面/开机自启注册服务，仅由 harness 拉起，避免常驻干扰。
systemctl --user disable qbittorrent-nox 2>/dev/null || true
sudo systemctl disable transmission-daemon 2>/dev/null || true
sudo systemctl stop transmission-daemon 2>/dev/null || true

echo "== [3/5] 验证客户端版本 =="
qbittorrent-nox --version | head -1
transmission-daemon --version | head -1

echo "== [4/5] chihaya tracker（Go 工具链构建） =="
if ! command -v go >/dev/null 2>&1; then
  echo "未找到 go：请先在主机 A 安装 Go >= 1.21（apt 的 golang-go 偏旧，建议官方 tarball）" >&2
  exit 1
fi
go version
# 注意：chihaya 依赖 SermoDigital/jose 在 Go 1.24+ 下 init 崩溃（SHA3 注册被移除）。
# 实验室主机 A 若 Go >= 1.24，需用本仓库 tools/l4_lab_runbook.md §5 的补丁法构建；
# 若 Go < 1.24（如 1.21/1.22），可直接：go install github.com/chihaya/chihaya/cmd/chihaya@2f794401e4
GO=$(command -v go)
if "$GO" version | grep -qE 'go1\.(2[0-3]|1[0-9]| [0-9])'; then
  GOBIN="$HOME/l4lab/bin" "$GO" install github.com/chihaya/chihaya/cmd/chihaya@2f794401e4
else
  echo "Go >= 1.24：请按 tools/l4_lab_runbook.md §5 补丁法构建 chihaya（去掉 middleware/jwt blank import）" >&2
  exit 1
fi
mkdir -p "$HOME/l4lab"
cp "$(dirname "$0")/chihaya_lab.yaml" "$HOME/l4lab/chihaya_lab.yaml" 2>/dev/null || \
  echo "未找到 chihaya_lab.yaml，请从仓库 tools/ 复制" >&2

echo "== [5/5] 链路与抓包自检 =="
iface=$(ip -o -4 route show default | awk '{print $5}' | head -1)
echo "默认网卡：$iface"
ethtool "$iface" 2>/dev/null | grep -i speed || ip link show "$iface"
echo "本机 IP：$(ip -o -4 addr show "$iface" | awk '{print $4}')"
sudo -n tcpdump -D >/dev/null 2>&1 && echo "tcpdump：免密可用" || \
  echo "tcpdump：需 sudo；请在实验室执行 sudo tcpdump 验证 BPF 权限"

echo "== 完成。把以下信息回填 tools/l4_lab_runbook.md §2 清单 =="
echo "  主机A IP / qbittorrent-nox 版本 / transmission 版本 / chihaya 路径: $HOME/l4lab/bin/chihaya"
