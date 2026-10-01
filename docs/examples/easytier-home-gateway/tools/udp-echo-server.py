#!/usr/bin/env python3
"""UDP 回声服务：作为 overlay 另一侧（家侧）的真实 UDP 目标。

用途：验证 mihomo 的 SOCKS5 UDP ASSOCIATE -> EasyTier 出站 ListenPacketContext
     -> 家侧 platform.BindUDP -> 真实 UDP socket 这条完整链路。

用法：python3 udp-echo-server.py --bind 10.0.5.130 --port 18001
"""

from __future__ import annotations

import argparse
import socket
import sys


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--bind", required=True, help="本地绑定地址（真实的局域网 IP）")
    parser.add_argument("--port", type=int, required=True)
    args = parser.parse_args()

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    sock.bind((args.bind, args.port))
    print(f"[udp-echo] listening on {args.bind}:{args.port}", flush=True)

    while True:
        data, addr = sock.recvfrom(65535)
        print(f"[udp-echo] recv {len(data)}B from {addr[0]}:{addr[1]}: {data[:64]!r}", flush=True)
        sock.sendto(b"echo:" + data, addr)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(0)
