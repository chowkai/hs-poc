#!/bin/bash
# hs-poc 端到端组网测试 — 通过 Headscale 服务器协调，两台 WG 节点 Mesh 互通
# 验证: 注册 → 获取真 Curve25519 密钥 → 创建 WG 接口 → Ping
set -e
SERVER="http://43.165.166.56:8080"
KEY="8f132c90060f736bf191eb6146714a53"

echo "=== 1. 重置服务端 + 注册节点 ==="
ssh -o StrictHostKeyChecking=no -i ~/.ssh/tencent-tokyo-wg ubuntu@43.165.166.56 \
  'sudo systemctl stop hs-server; rm -f ~/hs-data/headscale.db; sudo systemctl start hs-server; sleep 1' 2>/dev/null

A=$(curl -s -X POST "$SERVER/api/v1/node/register?name=a&endpoint=127.0.0.1:51821" -H "Authorization: Bearer $KEY")
B=$(curl -s -X POST "$SERVER/api/v1/node/register?name=b&endpoint=127.0.0.1:51822" -H "Authorization: Bearer $KEY")
IPA=$(echo "$A" | python3 -c "import sys,json;print(json.load(sys.stdin)['node']['ip'])")
IPB=$(echo "$B" | python3 -c "import sys,json;print(json.load(sys.stdin)['node']['ip'])")
PKA=$(echo "$A" | python3 -c "import sys,json;print(json.load(sys.stdin)['node']['private_key'])")
PKB=$(echo "$B" | python3 -c "import sys,json;print(json.load(sys.stdin)['node']['private_key'])")
PUBA=$(echo "$PKA" | wg pubkey)
PUBB=$(echo "$PKB" | wg pubkey)
echo "  A: $IPA  pub=$PUBA"
echo "  B: $IPB  pub=$PUBB"

echo ""
echo "=== 2. 验证密钥: 服务端公钥 vs wg pubkey ==="
PUBA_SRV=$(echo "$A" | python3 -c "import sys,json;print(json.load(sys.stdin)['node']['public_key'])")
PUBB_SRV=$(echo "$B" | python3 -c "import sys,json;print(json.load(sys.stdin)['node']['public_key'])")
[ "$PUBA" = "$PUBA_SRV" ] && echo "  ✅ A 密钥匹配" || { echo "  ❌ A 不匹配"; exit 1; }
[ "$PUBB" = "$PUBB_SRV" ] && echo "  ✅ B 密钥匹配" || { echo "  ❌ B 不匹配"; exit 1; }

echo ""
echo "=== 3. 创建隔离网络空间 ==="
sudo ip netns del ns-a 2>/dev/null; sudo ip netns del ns-b 2>/dev/null
sudo ip netns add ns-a; sudo ip netns add ns-b
sudo ip netns exec ns-a ip link set lo up
sudo ip netns exec ns-b ip link set lo up

echo "=== 4. 创建 WireGuard Mesh ==="
sudo ip link add hs-a type wireguard
sudo ip link set hs-a netns ns-a
echo "$PKA" | sudo ip netns exec ns-a wg set hs-a private-key /dev/stdin listen-port 51821
sudo ip netns exec ns-a wg set hs-a peer "$PUBB" allowed-ips "$IPB/32" endpoint 127.0.0.1:51822 persistent-keepalive 3
sudo ip netns exec ns-a ip addr add "$IPA/24" dev hs-a
sudo ip netns exec ns-a ip link set hs-a up

sudo ip link add hs-b type wireguard
sudo ip link set hs-b netns ns-b
echo "$PKB" | sudo ip netns exec ns-b wg set hs-b private-key /dev/stdin listen-port 51822
sudo ip netns exec ns-b wg set hs-b peer "$PUBA" allowed-ips "$IPA/32" endpoint 127.0.0.1:51821 persistent-keepalive 3
sudo ip netns exec ns-b ip addr add "$IPB/24" dev hs-b
sudo ip netns exec ns-b ip link set hs-b up
sleep 3

echo ""
echo "=== 5. wg show ==="
sudo ip netns exec ns-a wg show
echo "---"
sudo ip netns exec ns-b wg show

echo ""
echo "=== 6. PING A→B ($IPA → $IPB) ==="
sudo ip netns exec ns-a ping -c 3 -W 2 "$IPB" 2>&1

echo ""
echo "=== 7. PING B→A ($IPB → $IPA) ==="
sudo ip netns exec ns-b ping -c 3 -W 2 "$IPA" 2>&1

echo ""
echo "=== 8. 清理 ==="
sudo ip netns del ns-a 2>/dev/null
sudo ip netns del ns-b 2>/dev/null
echo "✅ 全部通过！"