#!/usr/bin/env python3
'''把两侧配置模板渲染成可运行配置 ✓ —— 脱敏：密钥/域名/IP 全部来自 values.env ✓'''
import os, pathlib, sys
def load(p):
    d = {}
    for line in pathlib.Path(p).read_text(encoding='utf-8').splitlines():
        line = line.strip()
        if line and not line.startswith('#') and '=' in line:
            k, v = line.split('=', 1); d[k.strip()] = v.strip()
    return d
here = pathlib.Path(__file__).parent
vals = load(os.environ.get('VALUES', here / 'values.env'))
out = pathlib.Path(os.environ.get('OUT', here / 'rendered')); out.mkdir(parents=True, exist_ok=True)
for tpl in ('gw.yaml.tmpl', 'client.yaml.tmpl'):
    t = (here / tpl).read_text(encoding='utf-8')
    for k, v in vals.items():
        t = t.replace('${%s}' % k, v)
    left = [x for x in t.split() if x.startswith('${')]
    if left: print('  ✗ 未替换的占位符:', left, file=sys.stderr); sys.exit(1)
    (out / tpl.replace('.tmpl', '')).write_text(t, encoding='utf-8')
    print(f'  ✓ 已渲染 {tpl} → rendered/{tpl.replace(".tmpl","")}')
