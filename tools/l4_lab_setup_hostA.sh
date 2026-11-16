#!/usr/bin/env bash
# L4 实验室第二台主机（主机 A，Debian/Ubuntu）一键部署脚本。
#
# 角色分工见 tools/l4_lab_runbook.md：
#   主机 U（macOS，本仓库所在机器）：被测 udd + harness 驱动 + 抓包
#   主机 A（本脚本目标机）        ：真实 qBittorrent-nox / Transmission-daemon / chihaya
#
# 前提：以具备 sudo 的用户在主机 A 上执行；机器已在隔离网段（无外网路由），
# 如无外网需先配置内网 apt 镜像源。本脚本只装实验室软件，不改网络配置。
#
# Ubuntu 23.04 / 23.10 已 EOL：脚本第 0 步自动把 apt 源切到 old-releases.ubuntu.com
# （原始文件保留 *.bak 备份）。chihaya 优先使用随脚本分发的预编译二进制
# （仓库维护者在 macOS 上交叉编译，见 tools/l4_lab_runbook.md §5），无需主机 A 装 Go。
set -euo pipefail

echo "== [0/5] Ubuntu 版本与 EOL 源检测 =="
. /etc/os-release
echo "主机 A 系统：${PRETTY_NAME:-unknown}（$(uname -m)）"
case "${VERSION_ID:-}" in
  23.04|23.10|22.10|21.10|20.10)
    echo "检测到已 EOL 的 Ubuntu ${VERSION_ID}：全部 /ubuntu 仓库路径（含第三方镜像）→ old-releases.ubuntu.com"
    # EOL 后 archive/security/ports 与第三方镜像（aliyun/tuna/163 等）都会 404，
    # 因此不区分 host，凡 /ubuntu 仓库路径一律指向 old-releases。
    # 注意：-E（ERE）下 `?` 本身就是量词，写成 `\?` 反而匹配字面问号（已踩坑）。
    SED='s#https?://[^/ ]*/ubuntu#http://old-releases.ubuntu.com/ubuntu#g'
    if [ -f /etc/apt/sources.list.d/ubuntu.sources ]; then
      sudo sed -i.bak -E "$SED" /etc/apt/sources.list.d/ubuntu.sources
    fi
    if ls /etc/apt/sources.list.d/*.list >/dev/null 2>&1; then
      sudo sed -i.bak -E "$SED" /etc/apt/sources.list.d/*.list
    fi
    if [ -f /etc/apt/sources.list ]; then
      sudo sed -i.bak -E "$SED" /etc/apt/sources.list
    fi
    echo "（原始源已备份为 *.bak；非 x86_64 上 old-releases 的 ports 支持不完整，如 404 请反馈架构）"
    ;;
  "" )
    echo "警告：未能读取 /etc/os-release 的 VERSION_ID，按非 EOL 源继续" >&2 ;;
  * )
    echo "Ubuntu ${VERSION_ID} 仍在支持期内，apt 源不改动" ;;
esac

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

echo "== [4/5] chihaya tracker（优先预编译二进制） =="
mkdir -p "$HOME/l4lab/bin"
ARCH=$(uname -m)
PREBUILT="$(dirname "$0")/chihaya-linux-${ARCH/amd64/amd64}"
[ "$ARCH" = "x86_64" ] && PREBUILT="$(dirname "$0")/chihaya-linux-amd64"
[ "$ARCH" = "aarch64" ] && PREBUILT="$(dirname "$0")/chihaya-linux-arm64"
if [ -x "$HOME/l4lab/bin/chihaya" ]; then
  echo "chihaya 已就位：$HOME/l4lab/bin/chihaya"
elif [ -f "$PREBUILT" ]; then
  cp "$PREBUILT" "$HOME/l4lab/bin/chihaya" && chmod +x "$HOME/l4lab/bin/chihaya"
  echo "已安装预编译 chihaya（macOS 交叉编译，构建口径见 runbook §5）：$HOME/l4lab/bin/chihaya"
else
  echo "未找到预编译二进制 $PREBUILT，尝试本机构建……"
  if ! command -v go >/dev/null 2>&1; then
    echo "未找到 go：请把仓库里的 chihaya-linux-{amd64,arm64} 与本脚本放同一目录后重跑" >&2
    exit 1
  fi
  go version
  # 注意：chihaya 依赖 SermoDigital/jose 在 Go 1.24+ 下 init 崩溃（SHA3 注册被移除）。
  # 实验室主机 A 若 Go >= 1.24，需用本仓库 tools/l4_lab_runbook.md §5 的补丁法构建；
  # 若 Go < 1.24（如 Ubuntu 23.10 apt 的 1.21），可直接 go install。
  GO=$(command -v go)
  if "$GO" version | grep -qE 'go1\.(2[0-3]|1[0-9])'; then
    GOBIN="$HOME/l4lab/bin" "$GO" install github.com/chihaya/chihaya/cmd/chihaya@2f794401e4
  else
    echo "Go >= 1.24：请按 tools/l4_lab_runbook.md §5 补丁法构建 chihaya（去掉 middleware/jwt blank import）" >&2
    exit 1
  fi
fi
cp "$(dirname "$0")/chihaya_lab.yaml" "$HOME/l4lab/chihaya_lab.yaml" 2>/dev/null || \
  echo "未找到 chihaya_lab.yaml，请从仓库 tools/ 复制" >&2
"$HOME/l4lab/bin/chihaya" --help 2>&1 | head -1 || true

echo "== [5/5] 链路与抓包自检 =="
iface=$(ip -o -4 route show default | awk '{print $5}' | head -1)
echo "默认网卡：$iface"
ethtool "$iface" 2>/dev/null | grep -i speed || ip link show "$iface"
echo "本机 IP：$(ip -o -4 addr show "$iface" | awk '{print $4}')"
sudo -n tcpdump -D >/dev/null 2>&1 && echo "tcpdump：免密可用" || \
  echo "tcpdump：需 sudo；请在实验室执行 sudo tcpdump 验证 BPF 权限"

echo "== 完成。把以下信息回填 tools/l4_lab_runbook.md §2 清单 =="
echo "  主机A IP / qbittorrent-nox 版本 / transmission 版本 / chihaya 路径: $HOME/l4lab/bin/chihaya"
