# Single-Layer SSH Acceleration (System SOCKS5)

Mihomo now supports a high-performance single-layer SSH proxy mode using the system's `ssh` command.

## Overview
Traditional SSH proxies often use a double-layer encryption (Go SSH client over a System SSH tunnel), which introduces significant CPU overhead and high latency (TTFB). 

The **Single-Layer Mode** uses `ssh -D` directly to establish a local SOCKS5 tunnel, allowing Mihomo to dial directly into the tunnel without a second layer of encryption.

### Key Features
- **Zero-Config Port Management**: Automatically finds and allocates a free local port for the SOCKS5 tunnel.
- **Improved Performance**: Reduces CPU usage and latency by up to 50% compared to double-layer mode.
- **Robust Lifecycle**: The SSH subprocess is managed by Mihomo; it starts on demand and is guaranteed to be terminated when the proxy is closed.
- **Advanced Compatibility**: Inherits all features of OpenSSH (`~/.ssh/config`, ProxyJump, Keys, etc.).

## Configuration

To enable this mode, add `use-system-socks: true` to your SSH proxy configuration.

### Basic Example
```yaml
proxies:
  - name: "SSH_FAST"
    type: ssh
    server: my-alias-in-ssh-config
    use-ssh-config-alias: true
    use-system-socks: true   # Enable single-layer mode
```

### Advanced Example (Manual Port)
If you want to pin the local SOCKS listener to a specific port:
```yaml
proxies:
  - name: "SSH_PINNED"
    type: ssh
    server: server.example.com
    port: 1080              # When use-system-socks is true, this is the local port
    use-system-socks: true
    # ssh-flags: ["-p", "2222"] # Pass extra flags to ssh.exe
```

## Troubleshooting

### Loop-back Issues (Fake-IP)
If you are using **Fake-IP** mode, the `ssh` process might accidentally try to resolve the server via Mihomo, causing a recursive loop. 

**Fix**: Add a DIRECT rule for the `ssh` process.
```yaml
rules:
  - PROCESS-NAME,ssh,DIRECT      # macOS
  - PROCESS-NAME,ssh.exe,DIRECT  # Windows
```

> [!NOTE]
> The latest implementation includes an automatic "Zero-Config" pre-resolution (`ssh -G`) which eliminates the loop-back issue in most environments without needing the above rule.

### Process Exited (255)
This usually means another SSH session is sharing a socket via `ControlMaster`. The current implementation explicitly disables `ControlMaster` for isolation, but if issues persist, check your `~/.ssh/config` for global overrides.

## UDP Handling & Fallback

SSH (including `-D` mode) natively supports **TCP only**. It does not support `UDP ASSOCIATE`.

### Recommended Fallback Configuration
To ensure a smooth experience when apps try to use UDP (like QUIC), configure Mihomo's **DNS Fake-IP** and **Sniffing**. This forces apps to fall back to TCP automatically when UDP times out.

```yaml
dns:
  enable: true
  enhanced-mode: fake-ip
  # nameservers...

sniffer:
  enable: true
  sniff:
    TLS:
      ports: [443, 8443]
    HTTP:
      ports: [80, 8080-8880]
    QUIC:
      ports: [443, 8443]
```

With this setup, browser traffic (Chrome/Edge) will seamlessly switch from QUIC (UDP) to HTTP/2 (TCP) and utilize the SSH acceleration.

## Future Considerations

### Connection Pool Pre-warming (Subprocess Pooling)
If extreme speed test performance or near-zero latency for the first request is required, the following architectural improvements can be considered:
1. **Pre-warming**: Launch the `ssh -D` subprocess immediately upon configuration load or profile switch, rather than waiting for the first dial.
2. **Auto-Respawn**: Implement an automated monitoring loop to restart the SSH subprocess immediately if it exits unexpectedly, ensuring the tunnel is always "hot".
3. **Wait Queue Optimization**: Refine the `waitReady` mechanism to handle extreme thundering herd scenarios (e.g., >1000 concurrent requests) with a managed backpressure queue.

Current implementation (v1.19.20_12) balances resources and complexity by using **On-Demand Startup with Concurrency Synchronization**.
