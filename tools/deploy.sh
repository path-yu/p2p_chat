#!/usr/bin/env bash
# 把服务器节点编译好、传上去、装成 /usr/local/bin/lumend。
#
#   bash tools/deploy.sh root@1.2.3.4            # amd64，默认端口 47821 / 47823
#   bash tools/deploy.sh root@1.2.3.4 50000      # 换端口
#   ARCH=arm64 bash tools/deploy.sh root@1.2.3.4 # 甲骨文/树莓派这类 ARM 机器
set -euo pipefail

HOST="${1:-}"
if [ -z "$HOST" ]; then
    echo "用法: bash tools/deploy.sh 用户名@服务器IP [端口]" >&2
    exit 1
fi
PORT="${2:-47821}"
UDPPORT=$((PORT + 2))
ARCH="${ARCH:-amd64}"

here="$(cd "$(dirname "$0")/.." && pwd)"
bin="$here/lumen-linux-$ARCH"

echo "==> 编译 linux/$ARCH（纯 Go，不依赖 glibc 与图形库）"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
    go build -trimpath -ldflags "-s -w" -o "$bin" "$here/cmd/lumend"
ls -lh "$bin" | awk '{print "    "$5, $9}'

echo "==> 传到 $HOST"
scp -C "$bin" "$HOST:/tmp/lumend"

echo "==> 安装到 /usr/local/bin/lumend"
ssh "$HOST" 'sudo install -m755 /tmp/lumend /usr/local/bin/lumend && rm -f /tmp/lumend && /usr/local/bin/lumend -h 2>&1 | head -3'

cat <<EOF

==> 装好了。在服务器上跑：

  lumend -network mynet -port $PORT -udp-port $UDPPORT

  想在 SSH 断开后继续跑：

  nohup lumend -network mynet -port $PORT -udp-port $UDPPORT > ~/lumen.out 2>&1 &
  tail -f ~/lumen.out

  在云控制台的「安全组 / 防火墙」里放行：

    UDP $UDPPORT   ← 打洞必需
    TCP $PORT      ← 直连可选，放行后连得更快

  本机用同一个网络名即可自动找到它：

    .\\lumen.exe -network mynet
EOF
