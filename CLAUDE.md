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

### Upstream-Synced Protocols (originally contributed by this fork)

Sudoku and TrustTunnel were originally contributed by this fork; the initial add
commits were absorbed into upstream `MetaCubeX/mihomo`, and the fork now tracks
upstream byte-for-byte on every file below. **Treat them as upstream-maintained**
— do not assume merge conflicts are impossible here, and do not assume upstream
lacks these files.

| Path | Description |
|------|-------------|
| `transport/sudoku/` | Sudoku obfuscation protocol — AEAD crypto, HTTP-mask obfs, multiplexing, KIP handshake |
| `transport/trusttunnel/` | Adapted from `xchacha20-poly1305/sing-trusttunnel`; QUIC-based tunnel protocol |
| `adapter/outbound/sudoku.go` | Outbound proxy adapter for Sudoku |
| `adapter/outbound/trusttunnel.go` | Outbound proxy adapter for TrustTunnel |
| `listener/sudoku/server.go` | Inbound listener for Sudoku server mode |
| `listener/trusttunnel/server.go` | Inbound listener for TrustTunnel server mode |
| `listener/inbound/sudoku.go` + `sudoku_test.go` | Inbound config handler for Sudoku |
| `listener/inbound/trusttunnel.go` + `trusttunnel_test.go` | Inbound config handler for TrustTunnel |
| `listener/config/sudoku.go` | Config types for Sudoku listener |
| `listener/config/trusttunnel.go` | Config types for TrustTunnel listener |

### SSH System Proxy (fork-only)

These files do **not** exist upstream. Safe to modify freely. See
`docs/ssh_single_layer_guide.md` for the design spec.

| Path | Description |
|------|-------------|
| `adapter/outbound/ssh_system.go` | User resolution, sudo command construction, lifecycle entrypoints |
| `adapter/outbound/ssh_system_helper.go` | `buildEnvCommand` shell-capture helper |
| `adapter/outbound/ssh_system_socks.go` | Wraps system OpenSSH into a local SOCKS5 listener with managed `ssh -N -D`, SOCKS5 greeting probe, and `ssh-flags` filter |
| `adapter/outbound/ssh_resilience.go` | `startHealthCheck` + per-user shell env cache (`fetchUserEnv` / `clearUserEnv`) |

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
            │    ├─ (upstream)        vmess, vless, ss, trojan, tuic, hysteria2,
            │    │                    sudoku, trusttunnel …
            │    └─ (fork ssh layer)  ssh_system*.go, ssh_resilience.go
            ├─ adapter/outboundgroup/  # Selector, fallback, load-balance, url-test
            ├─ adapter/provider/      # Remote proxy list fetching
            └─ listener/      # Inbound listeners (HTTP, SOCKS, TUN, TPROXY, mixed,
                              #  sudoku, trusttunnel — all upstream)
```

Key data flow: **inbound listener** → `tunnel/tunnel.go` applies rules → picks **outbound adapter/group** → forwards via **transport layer**.

Config is parsed in `config/config.go`; runtime state lives in `hub/executor/`. The `constant/` package holds shared types (proxy metadata, rule types, paths).

## Upstream Sync Notes

- Pull upstream changes with `git fetch upstream && git merge upstream/Alpha` (or the relevant branch). The recommended workflow lives in `.claude/skills/upstream-sync/SKILL.md`.
- Only the **SSH System Proxy** files are fork-only; sudoku/trusttunnel are upstream-maintained even though originally contributed here.
- Files shared with upstream that this fork modifies (e.g. `adapter/outbound/ssh.go`) are the most likely source of merge conflicts; review diffs carefully after each upstream sync, and check fork-only files for indirect compile-time fallout (package switches, signature changes).
- The `adapters/` top-level directory (plural) is a legacy leftover from very early commits and is not used in the current build.

<!-- gitnexus:start -->
# GitNexus — Code Intelligence

This project is indexed by GitNexus as **mihomo** (14473 symbols, 47782 relationships, 300 execution flows). Use the GitNexus MCP tools to understand code, assess impact, and navigate safely.

> If any GitNexus tool warns the index is stale, run `npx gitnexus analyze` in terminal first.

## Always Do

- **MUST run impact analysis before editing any symbol.** Before modifying a function, class, or method, run `gitnexus_impact({target: "symbolName", direction: "upstream"})` and report the blast radius (direct callers, affected processes, risk level) to the user.
- **MUST run `gitnexus_detect_changes()` before committing** to verify your changes only affect expected symbols and execution flows.
- **MUST warn the user** if impact analysis returns HIGH or CRITICAL risk before proceeding with edits.
- When exploring unfamiliar code, use `gitnexus_query({query: "concept"})` to find execution flows instead of grepping. It returns process-grouped results ranked by relevance.
- When you need full context on a specific symbol — callers, callees, which execution flows it participates in — use `gitnexus_context({name: "symbolName"})`.

## When Debugging

1. `gitnexus_query({query: "<error or symptom>"})` — find execution flows related to the issue
2. `gitnexus_context({name: "<suspect function>"})` — see all callers, callees, and process participation
3. `READ gitnexus://repo/mihomo/process/{processName}` — trace the full execution flow step by step
4. For regressions: `gitnexus_detect_changes({scope: "compare", base_ref: "main"})` — see what your branch changed

## When Refactoring

- **Renaming**: MUST use `gitnexus_rename({symbol_name: "old", new_name: "new", dry_run: true})` first. Review the preview — graph edits are safe, text_search edits need manual review. Then run with `dry_run: false`.
- **Extracting/Splitting**: MUST run `gitnexus_context({name: "target"})` to see all incoming/outgoing refs, then `gitnexus_impact({target: "target", direction: "upstream"})` to find all external callers before moving code.
- After any refactor: run `gitnexus_detect_changes({scope: "all"})` to verify only expected files changed.

## Never Do

- NEVER edit a function, class, or method without first running `gitnexus_impact` on it.
- NEVER ignore HIGH or CRITICAL risk warnings from impact analysis.
- NEVER rename symbols with find-and-replace — use `gitnexus_rename` which understands the call graph.
- NEVER commit changes without running `gitnexus_detect_changes()` to check affected scope.

## Tools Quick Reference

| Tool | When to use | Command |
|------|-------------|---------|
| `query` | Find code by concept | `gitnexus_query({query: "auth validation"})` |
| `context` | 360-degree view of one symbol | `gitnexus_context({name: "validateUser"})` |
| `impact` | Blast radius before editing | `gitnexus_impact({target: "X", direction: "upstream"})` |
| `detect_changes` | Pre-commit scope check | `gitnexus_detect_changes({scope: "staged"})` |
| `rename` | Safe multi-file rename | `gitnexus_rename({symbol_name: "old", new_name: "new", dry_run: true})` |
| `cypher` | Custom graph queries | `gitnexus_cypher({query: "MATCH ..."})` |

## Impact Risk Levels

| Depth | Meaning | Action |
|-------|---------|--------|
| d=1 | WILL BREAK — direct callers/importers | MUST update these |
| d=2 | LIKELY AFFECTED — indirect deps | Should test |
| d=3 | MAY NEED TESTING — transitive | Test if critical path |

## Resources

| Resource | Use for |
|----------|---------|
| `gitnexus://repo/mihomo/context` | Codebase overview, check index freshness |
| `gitnexus://repo/mihomo/clusters` | All functional areas |
| `gitnexus://repo/mihomo/processes` | All execution flows |
| `gitnexus://repo/mihomo/process/{name}` | Step-by-step execution trace |

## Self-Check Before Finishing

Before completing any code modification task, verify:
1. `gitnexus_impact` was run for all modified symbols
2. No HIGH/CRITICAL risk warnings were ignored
3. `gitnexus_detect_changes()` confirms changes match expected scope
4. All d=1 (WILL BREAK) dependents were updated

## Keeping the Index Fresh

After committing code changes, the GitNexus index becomes stale. Re-run analyze to update it:

```bash
npx gitnexus analyze
```

If the index previously included embeddings, preserve them by adding `--embeddings`:

```bash
npx gitnexus analyze --embeddings
```

To check whether embeddings exist, inspect `.gitnexus/meta.json` — the `stats.embeddings` field shows the count (0 means no embeddings). **Running analyze without `--embeddings` will delete any previously generated embeddings.**

> Claude Code users: A PostToolUse hook handles this automatically after `git commit` and `git merge`.

## CLI

| Task | Read this skill file |
|------|---------------------|
| Understand architecture / "How does X work?" | `.claude/skills/gitnexus/gitnexus-exploring/SKILL.md` |
| Blast radius / "What breaks if I change X?" | `.claude/skills/gitnexus/gitnexus-impact-analysis/SKILL.md` |
| Trace bugs / "Why is X failing?" | `.claude/skills/gitnexus/gitnexus-debugging/SKILL.md` |
| Rename / extract / split / refactor | `.claude/skills/gitnexus/gitnexus-refactoring/SKILL.md` |
| Tools, resources, schema reference | `.claude/skills/gitnexus/gitnexus-guide/SKILL.md` |
| Index, status, clean, wiki CLI commands | `.claude/skills/gitnexus/gitnexus-cli/SKILL.md` |

<!-- gitnexus:end -->

## Skill routing

When the user's request matches a project skill, ALWAYS invoke it using the Skill tool first.
Do not answer release / sync workflow questions ad-hoc when a repo skill already covers the path.

Key routing rules for this repo:
- 普通发布、新版本、release、推 `v*` tag、版本号发布 → invoke `release`
- `Prerelease-Alpha`、刷新预发布、刷新 Alpha 预览版 → invoke `prerelease`
- 同步上游、合并上游、upstream sync、升级到上游新 tag → invoke `upstream-sync`

Routing priority:
- 只要明确命中 `Prerelease-Alpha` / 刷新预发布，优先走 `prerelease`
- `release` 仅用于新建 `v*` tag 的普通 release

If the user is specifically asking about blast radius, execution flow, or safe refactoring, prefer the matching GitNexus skill from the table above.

If the user asks for `Prerelease-Alpha`, do not create a new `v*` tag unless they explicitly also want a normal release.
If the user asks for a normal release, do not use `Prerelease-Alpha` as the release vehicle.
If the user asks for some other custom prerelease shape that is not `Prerelease-Alpha`, do not assume `prerelease` skill applies; clarify or handle manually.

Only fall back to manual tool orchestration when no project skill matches the request.
