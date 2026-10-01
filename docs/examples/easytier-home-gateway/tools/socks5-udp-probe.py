#!/usr/bin/env python3
"""SOCKS5 UDP ASSOCIATE 探针（仅用标准库）。

为什么需要它：curl / nc 只会走 TCP CONNECT，无法验证 UDP 数据面。
本脚本按 RFC 1928 完整走一遍 UDP ASSOCIATE，用于判定：

    mihomo(mixed-port, udp)
      -> easytier 出站 ListenPacketContext
        -> EasyTier 用户态 UDP 数据面 + 对端 proxy-network
          -> 对端 platform.BindUDP -> 真实 UDP socket

任一段不通都会在这里暴露为 FAIL（附具体阶段）。

用法：
  python3 socks5-udp-probe.py --socks 127.0.0.1:17890 \
      --target 10.0.5.130 --port 18001 --payload hello-udp
退出码：0 成功；2 失败（原因打印在 stdout）。
"""

from __future__ import annotations

import argparse
import socket
import struct
import sys

SOCKS_VERSION = 5
CMD_UDP_ASSOCIATE = 3
ATYP_IPV4 = 1
ATYP_DOMAIN = 3
ATYP_IPV6 = 4


def recv_exact(sock: socket.socket, n: int) -> bytes:
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            raise ConnectionError("连接被对端关闭")
        buf += chunk
    return buf


def parse_atyp(sock: socket.socket) -> tuple[str, int]:
    atyp = recv_exact(sock, 1)[0]
    if atyp == ATYP_IPV4:
        host = socket.inet_ntoa(recv_exact(sock, 4))
    elif atyp == ATYP_IPV6:
        host = socket.inet_ntop(socket.AF_INET6, recv_exact(sock, 16))
    elif atyp == ATYP_DOMAIN:
        length = recv_exact(sock, 1)[0]
        host = recv_exact(sock, length).decode()
    else:
        raise ConnectionError(f"未知的 ATYP: {atyp}")
    port = struct.unpack("!H", recv_exact(sock, 2))[0]
    return host, port


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--socks", required=True, help="SOCKS5 服务地址 host:port")
    parser.add_argument("--target", required=True, help="UDP 目标 IPv4")
    parser.add_argument("--port", type=int, required=True, help="UDP 目标端口")
    parser.add_argument("--payload", default="udp-probe")
    parser.add_argument("--timeout", type=float, default=5.0)
    parser.add_argument("--relay", default="", help="覆盖 UDP 中继地址，host:port（端口映射/Docker 场景）")
    args = parser.parse_args()

    socks_host, socks_port = args.socks.rsplit(":", 1)
    socks_port = int(socks_port)

    tcp = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    tcp.settimeout(args.timeout)
    try:
        tcp.connect((socks_host, socks_port))
    except OSError as exc:
        print(f"FAIL[greeting] 无法连接 SOCKS5 {args.socks}: {exc}")
        return 2

    tcp.sendall(bytes([SOCKS_VERSION, 1, 0x00]))  # 只声明 no-auth
    version, method = recv_exact(tcp, 2)
    if version != SOCKS_VERSION or method != 0x00:
        print(f"FAIL[greeting] 协商失败: version={version} method={method}")
        return 2

    # UDP ASSOCIATE；DST.ADDR/PORT 是客户端预期发送来源，全 0 表示不限定
    tcp.sendall(bytes([SOCKS_VERSION, CMD_UDP_ASSOCIATE, 0x00, ATYP_IPV4]) + b"\x00\x00\x00\x00" + b"\x00\x00")
    version, rep, _ = recv_exact(tcp, 3)
    if version != SOCKS_VERSION or rep != 0x00:
        print(f"FAIL[associate] 服务端拒绝 UDP ASSOCIATE: rep=0x{rep:02x}")
        return 2
    relay_host, relay_port = parse_atyp(tcp)
    if args.relay:
        relay_host, relay_port = args.relay.rsplit(":", 1); relay_port = int(relay_port)
    elif relay_host in ("0.0.0.0", "::"):
        relay_host = socks_host  # 全 0 表示「就用服务端地址」
    print(f"[probe] UDP relay = {relay_host}:{relay_port}")

    udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    udp.settimeout(args.timeout)

    header = bytes([0x00, 0x00, 0x00, ATYP_IPV4]) + socket.inet_aton(args.target) + struct.pack("!H", args.port)
    payload = args.payload.encode()
    try:
        udp.sendto(header + payload, (relay_host, relay_port))
    except OSError as exc:
        print(f"FAIL[send] 向 relay 发送失败: {exc}")
        return 2

    try:
        data, _ = udp.recvfrom(65535)
    except socket.timeout:
        print(f"FAIL[recv] {args.timeout}s 内没有收到回包（UDP 数据面不通，或家侧 UDP 目标未响应）")
        return 2

    # 解析回包头部
    if len(data) < 10 or data[0] != 0x00:
        print(f"FAIL[recv] 回包格式异常: {data[:32]!r}")
        return 2
    r_atyp = data[3]
    offset = 4 + (4 if r_atyp == ATYP_IPV4 else 16 if r_atyp == ATYP_IPV6 else 1 + data[4])
    body = data[offset + 2 :]
    print(f"[probe] 收到来自 {args.target}:{args.port} 的回包 {len(body)}B: {body[:64]!r}")
    if body == b"echo:" + payload:
        print("PASS[udp] UDP 数据面端到端可用")
        return 0
    print(f"FAIL[verify] 回包内容不符合预期（期望 {b'echo:' + payload!r}）")
    return 2


if __name__ == "__main__":
    sys.exit(main())
