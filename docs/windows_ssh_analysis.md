# Windows SSH Resolution Issues Analysis

## The Error: "\262\273\326..." (GBK Encoding)
The garbled string `\262\273\326\252\265\300\325\342\321\371\265\304\326\367\273\372\241\243` is the GBK encoding for **"不知道这样的主机。"** (Unknown host).
This string comes directly from the Windows OS network stack (via `ssh.exe`) when a DNS resolution fails. Go reads this GBK output from stderr and escapes it as invalid UTF-8.

## Why is it failing on Windows?

Our SSH outbound uses the system `ssh.exe` via `-W localhost:<port> <HostAlias>` to establish connections. If `ssh.exe` cannot resolve the alias, there are a few possible root causes:

### 1. Missing or Misplaced `~/.ssh/config` (Service Mode Context)
If Mihomo (or Clash Verge/Meta) is running as a **System Service** on Windows, it runs under the `SYSTEM` user account.
- `ssh.exe` will look for its config in the `SYSTEM` user's home directory (`C:\Windows\System32\config\systemprofile\.ssh\config`), **not** your personal user directory (`C:\Users\fa\.ssh\config`).
- Because it can't find the config file with the alias definitions (like `p2p.fa.internet.ssy`), it treats the alias as a regular domain name and tries to resolve it via DNS, which naturally fails.

### 2. The Context Deadline Timeout (`cpolar.fa.internet.company`)
```text
Attempt 1/3 for cpolar.fa.internet.company failed: context deadline exceeded, retry in 2s
```
This error is different. It means `ssh.exe` **did** find the host (DNS or alias resolved successfully) and launched the process, but the SSH handshake or TCP connection took more than 30 seconds to complete. This usually happens when:
- The target IP is unreachable or dropping packets.
- The connection is extremely slow.
- The SSH daemon is not responding on the other side.

### 3. Missing Environment Injection in `ssh -G`
In the Go code (`adapter/outbound/ssh_system.go`), we have an `applyEnv` function that injects environment variables.
However, `buildSshGCommand` (which runs `ssh -G` to preemptively parse host configs) does **not** currently use `applyEnv`. This means `ssh -G` might also be failing to read the `.ssh/config` file when running in isolated contexts.

## Suggested Solutions

### Solution 1: Use `ssh-user` explicitly
If you are running the core as a service, explicitly configure `ssh-user` in the proxy config to help resolve the correct path.

### Solution 2: Code Fixes in Mihomo Core (Implemented)
1. **Fix Environment Injection**: Refactored the code to ensure `buildSshGCommand` also receives the injected environment variables.
2. **Translate GBK to UTF-8**: Added an automatic GBK-to-UTF8 decoder for the `ssh.exe` stderr pipe on Windows so the logs are highly readable ("不知道这样的主机。") instead of the `\262\273` garble.
3. **Handle Double-Layer Identity**: Improved the identity loading logic to ensure the inner Go SSH client correctly expands `~` paths to your home folder.

Please let me know if you are running Mihomo as a system service, and if you want me to implement the core code fixes (Solution 2) directly!
