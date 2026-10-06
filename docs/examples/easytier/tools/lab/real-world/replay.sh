#!/usr/bin/env bash
# 端到端重放 ✓：渲染两份配置 → 起隔离实验台 → 跑三条判据 → 输出结论 ✓
# 用法：  ./replay.sh                     # 用 lab 默认值（脱敏 ✓）
#         VALUES=prod.env ./replay.sh     # 用你的真实值（**不要提交 prod.env** ✗）
set -euo pipefail
cd "$(dirname "$0")"
echo '=== ① 渲染配置 ==='
python3 render.py
echo '=== ② 起实验台（独立网段 ✓ 不碰真实网络 ✓）==='
GW_CONFIG=./real-world/rendered/gw.yaml CLIENT_CONFIG=./real-world/rendered/client.yaml docker compose -f ../docker-compose.yml --project-name "${LAB_NAME:-etlab}" up -d
echo '   等待节点互联…'; sleep 30
echo '=== ③ 判据一：两个节点是否都在 overlay 里（节点间天然双向 ✓）==='
GW_CONFIG=./real-world/rendered/gw.yaml CLIENT_CONFIG=./real-world/rendered/client.yaml docker compose -f ../docker-compose.yml --project-name "${LAB_NAME:-etlab}" exec -T gw easytier-cli peer 2>/dev/null | head -8 || \
  docker exec lab-rendezvous easytier-cli peer | head -8
echo '=== ④ 判据二：客户端 → 家侧局域网（应通 ✓）==='
docker run --rm --network "container:lab-client" curlimages/curl:latest \
  -s -m 12 -o /dev/null -w '   客户端→家侧: %{http_code} （200=通 ✓ / 502=不通 ✗）\n' \
  -x http://127.0.0.1:7891 http://10.99.1.2/ || true
echo '=== ⑤ 判据三：家侧 → 客户端所在网段（本次要修的方向 ✓）==='
docker run --rm --network "container:lab-gw" curlimages/curl:latest \
  -s -m 12 -o /dev/null -w '   家侧→客户端网段: %{http_code} （200=通 ✓ / 502=不通 ✗）\n' \
  -x http://127.0.0.1:7890 http://10.99.2.3/ || true
echo '=== ⑥ 若不通：看错误类型（判据口诀 ✓）==='
echo '   network is unreachable  → 没进 overlay ✗：规则指向的出站缺 exit-nodes'
echo '   i/o timeout             → 进了 overlay ✗：数据面问题（TUN 未启用 / 网关角色没生效）'
docker logs lab-gw 2>&1 | grep -aE 'unreachable|timeout' | tail -2 | sed 's/^/   gw: /' || true
docker logs lab-client 2>&1 | grep -aE 'unreachable|timeout' | tail -2 | sed 's/^/   client: /' || true
echo '=== 清理：docker compose ... down -v ==='
