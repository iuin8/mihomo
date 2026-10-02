# Draft upstream issue: easytier outbound takes down the core in macOS service mode

> 目标仓库：`MetaCubeX/mihomo`（内核侧）/ `clash-verge-rev/clash-verge-rev`（服务侧）
> 本地分支：`fa/easytier-tun`，相关背景见 `docs/easytier_home_gateway_sop.md` §6.2–6.4

## Summary

On macOS, with Clash Verge Rev running its core through the privileged service
(`verge-mihomo-alpha -d <service runtime> -f <config>`), **the first connection
that actually traverses an `easytier` outbound takes the core down about 3 s
later**. The process is SIGKILLed, no crash report is written for our
(interpreter) build, and the service's own log records
`Killed by OOM killer or admin (SIGKILL) (code: None)`.

The same core binary, with the same outbound, is stable in every user-mode
configuration we could construct.

## Environment

* macOS 15 (arm64), Clash Verge Rev 2.5.6 with the privileged service installed
  (service bundle uptime hours; it never restarted during the failures).
* Core: fork build `CGO_ENABLED=0 -tags with_gvisor`, Go 1.26.8, alpha channel
  (vendored `easytier-go` with `wazero.NewRuntimeConfigInterpreter()` — see the
  interpreter note below).
* TUN enabled (`stack: gvisor`, `auto-route: true`, `auto-detect-interface: true`,
  `dns-hijack: ['any:53']`, `route-exclude-address: [<rendezvous>/32]`), mixed
  port 7897.
* Outbound under test: `type: easytier` with `peers: [tcp://<rendezvous>:11010]`,
  `no-listener: true`, `disable-p2p: true`, `interface-name: en0`.

## Reproduction (minimum)

1. Put an `easytier` outbound in the profile and a rule that routes a test
   subnet to it, e.g. `IP-CIDR,10.0.99.0/24,et-direct`.
2. Apply the config; the core starts, the instance reaches
   `[EasyTier](et-direct) instance <id> running`.
3. Send one connection through it: `curl --noproxy '*' http://10.0.99.1/`.
4. Within ~3 s the core's pid changes (service restart) and the app log shows
   `service restarted the core (N restarts so far); last exit: Killed by OOM
   killer or admin (SIGKILL) (code: None)`.

`GET /proxies/et-direct/delay?...` through the core's own API reproduces it
identically, so it is not specific to the traffic path.

## What it is NOT (all measured)

| Hypothesis | Measurement |
| --- | --- |
| OOM | RSS peaks at ~174 MB when killed; the same build reaches 259 MB and survives elsewhere. No jetsam entries. |
| Panic in the core | No `panic`/`fatal error`/`goroutine` dump in the core's captured stdout/stderr. |
| JIT / code signing | Interpreter build used; crash reports stay frozen (the JIT build produces `CODESIGNING/Invalid Page` SIGKILLs instead — a separate, understood failure). |
| launchd resource limits | Installed service plist has no `SoftResourceLimits`/`HardResourceLimits`. |
| Core API stalls | Five endpoints polled every 0.25 s across the trigger: max 0.05 s, failures only once the process is gone. |
| Delay-path specifics | Traffic path kills too. |
| The desktop app | Enabled `app_log_level: debug`: the app logs no recovery/restart decision before or at the kill; it only observes a broken IPC connection (`hyper::Error(Shutdown, Os { code: 57, ... })`) and then reads the death from the service. |
| Instance merely running | With a prewarmed instance (`running`), the core is stable for 7+ minutes and dozens of service `/status` polls; only an actual connection through the overlay triggers the kill. |

## What it IS

* Trigger: the first connection that traverses the overlay (both entry points).
* Actor: the privileged service (root, long-lived, the only component that both
  owns the child and reports `SIGKILL`). Its own logging is compiled off
  (`ENABLE_LOGGING = false`), and its IPC requires signed requests, so its
  decision path is not observable from outside.
* Scope: only when the core runs under the service **with TUN + auto-route**.
  The identical build and config are fine as a root process without TUN, and as
  a root process with a TUN device as long as `auto-route: false`.

## Workaround in production here

Run the same overlay in a **separate, user-mode mihomo** and consume it from the
service-mode core through a `socks5` outbound (rules for the home subnets point
at that group). That path has been stable for hours, carries TCP and UDP, and
needs no changes to the service or the OS. Cost: one extra background process
(and ICMP is not carried by SOCKS5).

## Ask

Confirm whether the service intentionally stops the core in response to some
observation (and if so, which one), or whether the core is dying on its own
before the service notices. If it is the former, the decision needs a log line
and a tolerance knob; if the latter, we can instrument the easytier/WASI host
path further on request.

## Decisive follow-up: no graceful stop is attempted

Reading the core's own `/logs` stream (no root needed: `curl -N --unix-socket
/var/run/clash-verge-service/users/<uid>/verge-mihomo.sock 'http://localhost/logs?level=info'`)
while triggering the connection shows the stream working normally and then
**no shutdown output at all** - not the signal line, not a single cleanup step.
The process is replaced ~3s after the first connection traverses the overlay.

So the supervisor does not ask the core to stop and then escalate; it kills
outright. SIGKILL cannot be caught, which means the core side has no remedy left:
only the supervisor's reason can be fixed. That reason is currently unobservable
(its logging is compiled off, and its IPC rejects unsigned requests), so the
next step would be a locally built helper with `ENABLE_LOGGING` enabled and the
protocol version matched to the installed app.
