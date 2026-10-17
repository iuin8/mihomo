#!/bin/sh
# render.sh —— 用环境变量渲染订阅模板(不引入第三方依赖 ✓ 渲染后**必须**无残留占位符 ✓)
# 用法:
#   LAN_NODE=et-home NET_NAME=... NET_SECRET=... RENDEZVOUS=... EXIT_ADDR=... \
#   GROUP=内网入口 OVERLAY=10.144.0.0/24 HOME_CIDR=192.168.1.0/24 CORP_CIDR=10.0.0.0/8 \
#   CORP_DOMAIN=x.example.com CLUSTER_DOMAIN=cluster.local DNS=10.96.0.10 \
#   sh render.sh > my-profile.yaml
set -eu
T="$(dirname "$0")/mihomo-profile.template.yaml"
[ -f "$T" ] || { echo "找不到模板: $T" >&2; exit 1; }
python3 - "$T" <<'PY'
import os, re, sys
text = open(sys.argv[1]).read()
# 占位符形如 <名字,如 示例> ⇒ 用环境变量同名(大写、非字母数字转下划线)替换
missing = []
def repl(m):
    inner = m.group(1)
    name = re.split(r"[,，]", inner)[0].strip()
    key = re.sub(r"[^0-9A-Za-z]+", "_", name).strip("_").upper()
    val = os.environ.get(key)
    if not val:
        missing.append((key, inner)); return m.group(0)
    return val
out = re.sub(r"<([^<>\n]{2,60})>", repl, text)
if missing:
    print("✗ 缺少环境变量(模板未渲染完):", file=sys.stderr)
    for k, i in missing: print(f"   {k}   ← <{i}>", file=sys.stderr)
    sys.exit(2)
left = re.findall(r"<[^<>\n]{2,60}>", out)
if left:
    print("✗ 渲染后仍有占位符:", str(left[:3]), file=sys.stderr); sys.exit(3)
import yaml  # 语法自检 ✓
yaml.safe_load(out)
sys.stdout.write(out)
PY
