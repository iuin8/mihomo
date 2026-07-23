package outbound

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/log"

	"github.com/metacubex/ssh"
)

// ─── Constants ───────────────────────────────────────────────────────────────

const (
	envCacheTTL            = 30 * time.Minute
	sshHealthCheckInterval = 30 * time.Second
)

// ─── Environment Cache ───────────────────────────────────────────────────────

type envCacheEntry struct {
	env        []string
	capturedAt time.Time
}

var (
	userEnvCache = make(map[string]*envCacheEntry)
	envMutex     sync.Mutex // 全程持有：避免两个 goroutine 同时通过 stale check 后重复跑 shell capture
)

// fetchUserEnv 抓取指定用户的最新登录环境变量（带 TTL 缓存）。
// Long-Running 进程 of os.Environ() 可能包含过期的 SSH_AUTH_SOCK，因此始终通过 Shell 重新抓取。
// 持锁跨 shell capture：env capture 不频繁（默认 30 分钟一次），同时锁住不同用户的 capture
// 比单飞依赖更简单且对实际并发量足够。
func fetchUserEnv(ctx context.Context, actualUser string) ([]string, error) {
	envMutex.Lock()
	defer envMutex.Unlock()

	if e, ok := userEnvCache[actualUser]; ok && time.Since(e.capturedAt) < envCacheTTL {
		log.Debugln("[SSH] Env cache hit for %s (age: %v)", actualUser, time.Since(e.capturedAt).Round(time.Second))
		return e.env, nil
	}

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

	userEnvCache[actualUser] = &envCacheEntry{env: env, capturedAt: time.Now()}
	return env, nil
}

// clearUserEnv 清除指定用户的环境缓存
func clearUserEnv(actualUser string) {
	if actualUser == "" {
		return
	}
	envMutex.Lock()
	delete(userEnvCache, actualUser)
	envMutex.Unlock()

	log.Warnln("[SSH] Cleared environment cache for user: %s", actualUser)
}

// ─── Connection Lifecycle ────────────────────────────────────────────────────

// startHealthCheck 周期检查 Go SSH client 是否存活，并在 Close() 触发的 s.closed 上自动退出。
// 仅用于内置 Go SSH 客户端路径；系统 SOCKS 模式不会进入这里（DialContext 提前分流）。
func (s *Ssh) startHealthCheck(client *ssh.Client) {
	ticker := time.NewTicker(sshHealthCheckInterval)
	defer ticker.Stop()

	for range ticker.C {
		if s.isClosed() {
			_ = client.Close()
			return
		}
		if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
			log.Warnln("[SSH] Health check failed for %s: %v", s.option.Name, err)
			_ = client.Close()
			return
		}
		log.Debugln("[SSH] Health check OK for %s", s.option.Name)
	}
}

// isClosed 在持锁状态下读取 s.closed，供 startHealthCheck 等后台 goroutine 提前退出。
func (s *Ssh) isClosed() bool {
	s.cMutex.Lock()
	defer s.cMutex.Unlock()
	return s.closed
}

// ─── Internal Helpers ────────────────────────────────────────────────────────

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

// logEnvDiff 对比进程内和新抓取的 SSH_AUTH_SOCK。
// 仅在 Debug 级输出 socket 路径，避免 Info 日志被 SIEM/聚合后泄漏 agent socket 位置。
func logEnvDiff(actualUser, newSock string) {
	oldSock := os.Getenv("SSH_AUTH_SOCK")
	if newSock != oldSock {
		log.Debugln("[SSH] SSH_AUTH_SOCK changed for %s: %s -> %s", actualUser, oldSock, newSock)
	} else {
		log.Debugln("[SSH] SSH_AUTH_SOCK unchanged for %s: %s", actualUser, newSock)
	}
}
