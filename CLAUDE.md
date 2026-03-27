# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Fork Identity

- **Upstream**: `MetaCubeX/mihomo` (remote: `upstream`)
- **This fork**: `iuin8/mihomo` (remote: `origin`)
- **Merge strategy**: Sync upstream into the working branch periodically. To minimise conflicts, avoid modifying upstream files; prefer adding new files or patching via thin wrappers.

## Commands

```bash
# Build (default — with gVisor TUN stack)
go build -tags with_gvisor -trimpath -ldflags '-w -s'

# Build without gVisor
go build

# Run all tests
go test ./...

# Run tests with race detector
go test -race ./...

# Run a single package test
go test ./transport/sudoku/...

# Lint
golangci-lint run ./...

# Cross-compile (examples)
make darwin-arm64
make linux-amd64-v3
make windows-amd64-v3
```

## Fork-Specific Files

These files were **added by this fork** and do not exist upstream. They are safe to modify freely.

### Custom Protocols

| Path | Description |
|------|-------------|
| `transport/sudoku/` | Sudoku obfuscation protocol — custom transport with AEAD crypto, HTTP-mask obfs, multiplexing, and KIP handshake |
| `transport/trusttunnel/` | Adapted from `xchacha20-poly1305/sing-trusttunnel`; QUIC-based tunnel protocol |
| `adapter/outbound/sudoku.go` | Outbound proxy adapter for Sudoku |
| `adapter/outbound/trusttunnel.go` | Outbound proxy adapter for TrustTunnel |
| `listener/sudoku/server.go` | Inbound listener for Sudoku server mode |
| `listener/trusttunnel/server.go` | Inbound listener for TrustTunnel server mode |
| `listener/inbound/sudoku.go` + `sudoku_test.go` | Inbound config handler for Sudoku |
| `listener/inbound/trusttunnel.go` + `trusttunnel_test.go` | Inbound config handler for TrustTunnel |
| `listener/config/sudoku.go` | Config types for Sudoku listener |
| `listener/config/trusttunnel.go` | Config types for TrustTunnel listener |

### SSH System Proxy (enhanced)

| Path | Description |
|------|-------------|
| `adapter/outbound/ssh_system.go` | SSH outbound using the system `ssh` binary (reads `~/.ssh/config`) |
| `adapter/outbound/ssh_system_helper.go` | Parses `ssh -G` output, caches host configs |
| `adapter/outbound/ssh_system_socks.go` | Wraps system SSH into a local SOCKS5 listener |
| `adapter/outbound/ssh_resilience.go` | Auto-reconnect / resilience layer over SSH connections |

### CI / Release

| Path | Description |
|------|-------------|
| `.github/workflows/build.yml` | Main build and release workflow for this fork |
| `.github/workflows/ssh-system-release.yml` | Release workflow targeting the ssh-system feature branch |
| `.github/workflows/test.yml` | Test workflow |

## Architecture Overview

```
main.go
  └─ hub/                     # REST API + config reload
       └─ tunnel/tunnel.go    # Core routing loop
            ├─ rules/         # Rule matching (domain, GEOIP, IPCIDR, process…)
            ├─ adapter/outbound/   # Proxy protocol implementations
            │    ├─ (upstream) vmess, vless, ss, trojan, tuic, hysteria2…
            │    └─ (fork)    sudoku.go, trusttunnel.go, ssh_system.go
            ├─ adapter/outboundgroup/  # Selector, fallback, load-balance, url-test
            ├─ adapter/provider/      # Remote proxy list fetching
            └─ listener/      # Inbound listeners (HTTP, SOCKS, TUN, TPROXY, mixed)
                 └─ (fork)    sudoku/, trusttunnel/
```

Key data flow: **inbound listener** → `tunnel/tunnel.go` applies rules → picks **outbound adapter/group** → forwards via **transport layer**.

Config is parsed in `config/config.go`; runtime state lives in `hub/executor/`. The `constant/` package holds shared types (proxy metadata, rule types, paths).

## Upstream Sync Notes

- Pull upstream changes with `git fetch upstream && git merge upstream/Alpha` (or the relevant branch).
- Files in the table above are fork-only — upstream merges will not touch them.
- Files shared with upstream that this fork modifies (e.g. `adapter/outbound/base.go`, `hub/executor/executor.go`) are the most likely source of merge conflicts; review diffs carefully after each upstream sync.
- The `adapters/` top-level directory (plural) is a legacy leftover from very early commits and is not used in the current build.
