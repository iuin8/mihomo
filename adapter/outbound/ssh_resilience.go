package outbound

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/log"
	"golang.org/x/crypto/ssh"
)

// ─── Constants ───────────────────────────────────────────────────────────────

const (
	envCacheTTL            = 30 * time.Minute // 环境变量缓存自动过期
	sshHealthCheckInterval = 30 * time.Second // Keepalive 探活间隔
	sshMaxRetries          = 3                // 最大重试次数
	sshReconnectBaseDelay  = 2 * time.Second  // 指数退避基础延迟
)

// ─── Environment Cache ───────────────────────────────────────────────────────

type envCacheEntry struct {
	env        []string
	capturedAt time.Time
}

var (
	userEnvCache = make(map[string]*envCacheEntry)
	envMutex     sync.RWMutex
)

// fetchUserEnv 抓取指定用户的最新登录环境变量（带 TTL 缓存）。
// Long-Running 进程的 os.Environ() 可能包含过期的 SSH_AUTH_SOCK，因此始终通过 Shell 重新抓取。
func fetchUserEnv(ctx context.Context, actualUser string) ([]string, error) {
	envMutex.RLock()
	if e, ok := userEnvCache[actualUser]; ok && time.Since(e.capturedAt) < envCacheTTL {
		envMutex.RUnlock()
		log.Debugln("[SSH] Env cache hit for %s (age: %v)", actualUser, time.Since(e.capturedAt).Round(time.Second))
		return e.env, nil
	}
	envMutex.RUnlock()

	log.Infoln("[SSH] Capturing fresh environment for user: %s", actualUser)
	cmd := buildEnvCommand(ctx, actualUser)
	if cmd == nil {
		log.Infoln("[SSH] Env capture skipped, using process environment for %s", actualUser)
		return os.Environ(), nil
	}

	output, err := cmd.Output()
	if err != nil {
		log.Warnln("[SSH] Env capture failed (%s): %v, falling back to process env", cmd.Path, err)
		return os.Environ(), nil
	}

	env, sockPath := parseEnvOutput(string(output))
	logEnvDiff(actualUser, sockPath)

	envMutex.Lock()
	userEnvCache[actualUser] = &envCacheEntry{env: env, capturedAt: time.Now()}
	envMutex.Unlock()

	return env, nil
}

// clearUserEnv 清除指定用户的环境 + 主机配置缓存
func clearUserEnv(actualUser string) {
	if actualUser == "" {
		return
	}
	envMutex.Lock()
	delete(userEnvCache, actualUser)
	envMutex.Unlock()

	hostMutex.Lock()
	for k := range hostConfigCache {
		if strings.HasPrefix(k, actualUser+":") {
			delete(hostConfigCache, k)
		}
	}
	hostMutex.Unlock()

	log.Warnln("[SSH] Cleared all caches for user: %s", actualUser)
}

// ─── Connection Lifecycle ────────────────────────────────────────────────────

// connectWithRetry 包装 connect，支持指数退避重试
// 注意：不要直接使用上游的 ctx，它通常带有很短的拨号超时（例如 5 秒）。
// 如果建立长连接耗时超过 5 秒，ctx 取消会导致所有后续重试瞬间失败。
// 构建 SSH 隧道是一个独立的后台长生命周期事件，应使用独立的 context。
func (s *Ssh) connectWithRetry(originalCtx context.Context, addr string) (*ssh.Client, error) {
	var lastErr error
	for attempt := 0; attempt <= sshMaxRetries; attempt++ {
		if attempt > 0 {
			delay := sshReconnectBaseDelay * time.Duration(1<<uint(attempt-1))
			log.Warnln("[SSH] Attempt %d/%d for %s failed: %v, retry in %v",
				attempt, sshMaxRetries, s.option.Name, lastErr, delay)
			time.Sleep(delay) // 直接 Sleep 退避，不依赖 originalCtx，保证隧道一定能建立
		}
		
		// 每次重试给 30 秒超时，足够建立复杂的代理甚至跳板机连接
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		client, err := s.connect(ctx, addr)
		cancel()

		if err == nil {
			return client, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// startHealthCheck 独立于上游 goroutine 运行：周期探活 + 断线后主动重连。
// 清理逻辑（s.client = nil）由 ssh.go 中的原始 goroutine 负责。
func (s *Ssh) startHealthCheck(client *ssh.Client) {
	dead := make(chan struct{})
	go func() {
		_ = client.Wait()
		close(dead)
	}()

	ticker := time.NewTicker(sshHealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				log.Warnln("[SSH] Health check failed for %s: %v", s.option.Name, err)
				_ = client.Close() // 触发上游 goroutine 清理
				<-dead
			} else {
				log.Debugln("[SSH] Health check OK for %s", s.option.Name)
			}

		case <-dead:
			// 上游 goroutine 负责 s.client = nil，我们只管重连
			s.cMutex.Lock()
			intentional := s.closed
			s.cMutex.Unlock()

			if intentional {
				return
			}
			log.Warnln("[SSH] Connection lost for %s, starting proactive reconnect", s.option.Name)
			s.reconnectWithBackoff()
			return
		}
	}
}

// reconnectWithBackoff 后台指数退避重连
func (s *Ssh) reconnectWithBackoff() {
	for attempt := 1; attempt <= sshMaxRetries; attempt++ {
		delay := sshReconnectBaseDelay * time.Duration(1<<uint(attempt-1))
		log.Infoln("[SSH] Reconnect %d/%d for %s in %v", attempt, sshMaxRetries, s.option.Name, delay)
		time.Sleep(delay)

		s.cMutex.Lock()
		if s.closed {
			s.cMutex.Unlock()
			log.Infoln("[SSH] Reconnect aborted (closed) for %s", s.option.Name)
			return
		}
		s.cMutex.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := s.connect(ctx, s.addr)
		cancel()

		if err == nil {
			log.Infoln("[SSH] Reconnect succeeded for %s", s.option.Name)
			return
		}
		log.Warnln("[SSH] Reconnect %d/%d failed for %s: %v", attempt, sshMaxRetries, s.option.Name, err)
	}
	log.Errorln("[SSH] All reconnect attempts failed for %s", s.option.Name)
}

// ─── Internal Helpers ────────────────────────────────────────────────────────

// buildEnvCommand 构建抓取环境变量的命令（同用户用 Shell，跨用户用 sudo）
func buildEnvCommand(ctx context.Context, actualUser string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		// Windows 上暂不支持跨用户抓取环境（没有通用的 sudo），直接返回空让 fetchUserEnv 使用 os.Environ()
		return nil
	}

	cur, _ := user.Current()
	// 简单检查用户名（忽略 Windows 可能的域名/机器名前缀）
	isSameUser := false
	if cur != nil {
		curName := cur.Username
		if idx := strings.LastIndex(curName, "\\"); idx != -1 {
			curName = curName[idx+1:]
		}
		isSameUser = (curName == actualUser)
	}

	if isSameUser {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		return exec.CommandContext(ctx, shell, "-ilc", "env")
	}
	return exec.CommandContext(ctx, "sudo", "-n", "-u", actualUser, "-H", "-i", "env")
}

// parseEnvOutput 解析 env 输出，返回环境变量列表和 SSH_AUTH_SOCK 值
func parseEnvOutput(output string) (env []string, sockPath string) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") || strings.HasPrefix(line, "Last login:") {
			continue
		}
		env = append(env, line)
		if strings.HasPrefix(line, "SSH_AUTH_SOCK=") {
			sockPath = strings.TrimPrefix(line, "SSH_AUTH_SOCK=")
		}
	}
	return
}

// logEnvDiff 对比进程内和新抓取的 SSH_AUTH_SOCK
func logEnvDiff(actualUser, newSock string) {
	oldSock := os.Getenv("SSH_AUTH_SOCK")
	if newSock != oldSock {
		log.Infoln("[SSH] SSH_AUTH_SOCK changed for %s: %s -> %s", actualUser, oldSock, newSock)
	} else {
		log.Debugln("[SSH] SSH_AUTH_SOCK unchanged for %s: %s", actualUser, newSock)
	}
}
