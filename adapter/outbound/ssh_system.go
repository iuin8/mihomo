package outbound

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/metacubex/mihomo/log"
	"golang.org/x/crypto/ssh"
)

// ─── Host Config Cache ──────────────────────────────────────────────────────

var (
	hostConfigCache = make(map[string]*HostConfig)
	hostMutex       sync.RWMutex
)

// fetchSshHostConfig 通过 ssh -G 获取并缓存主机配置
func (s *Ssh) fetchSshHostConfig(ctx context.Context, actualUser, hostAlias string) (*HostConfig, error) {
	cacheKey := actualUser + ":" + hostAlias

	hostMutex.RLock()
	if c, ok := hostConfigCache[cacheKey]; ok {
		hostMutex.RUnlock()
		return c, nil
	}
	hostMutex.RUnlock()

	cmd := buildSshGCommand(ctx, actualUser, hostAlias)
	capturedEnv, _ := fetchUserEnv(ctx, actualUser)
	s.applyEnv(cmd, capturedEnv)

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ssh -G failed: %w", err)
	}

	cfg := parseSshGOutput(string(output))

	hostMutex.Lock()
	hostConfigCache[cacheKey] = cfg
	hostMutex.Unlock()

	return cfg, nil
}

// clearHostConfigCache 清除指定用户的配置缓存
func clearHostConfigCache(actualUser string) {
	hostMutex.Lock()
	defer hostMutex.Unlock()
	for k := range hostConfigCache {
		if strings.HasPrefix(k, actualUser+":") {
			delete(hostConfigCache, k)
		}
	}
}

// ─── System SSH Dialing ─────────────────────────────────────────────────────

// dialViaSystemSsh 使用系统 SSH 命令建立连接，返回 net.Conn
func (s *Ssh) dialViaSystemSsh(ctx context.Context, hostAlias string) (net.Conn, error) {
	actualUser := s.resolveActualUser()
	sshArgs := s.buildSshArgs(hostAlias)
	cmd := buildSshCommand(actualUser, sshArgs)

	// 注入用户环境变量
	capturedEnv, _ := fetchUserEnv(ctx, actualUser)
	s.applyEnv(cmd, capturedEnv)

	log.Debugln("[SSH] Command: %s %s", cmd.Path, strings.Join(cmd.Args[1:], " "))

	conn, err := s.startSshProcess(cmd, actualUser)
	if err != nil {
		return nil, err
	}

	log.Infoln("[SSH] Subprocess started for %s (PID: %d)", hostAlias, cmd.Process.Pid)
	return conn, nil
}

// buildSshArgs 构建 SSH 命令行参数
func (s *Ssh) buildSshArgs(hostAlias string) []string {
	port := s.option.Port
	if port == 0 {
		port = 22
	}

	// ControlMaster=no 是必须的：Mihomo 使用 os.Pipe() 接管 I/O，
	// 与 ControlMaster 的 fd 复用机制冲突，会导致管道断裂。
	targetAddr := fmt.Sprintf("localhost:%d", port)
	// -T: 禁用 TTY，避免交互挂起
	// StrictHostKeyChecking=no: 确保在非交互环境下不会因为未知 Host Key 导致阻塞
	// Tunnel=no: 强制禁用 SSH 自带的隧道功能，防止其尝试创建系统 utun 接口与 Mihomo 的 TUN 模式冲突
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "ControlMaster=no", "-o", "StrictHostKeyChecking=no", "-o", "Tunnel=no"}
	args = append(args, s.option.SshFlags...)
	args = append(args, "-W", targetAddr, hostAlias)
	return args
}

// applyEnv 为 SSH 进程注入环境变量
func (s *Ssh) applyEnv(cmd *exec.Cmd, capturedEnv []string) {
	if capturedEnv != nil {
		cmd.Env = capturedEnv
	} else {
		cmd.Env = os.Environ()
	}
}

// ─── Zero-Config Resolution ─────────────────────────────────────────────────

// resolveActualUser 按优先级解析实际用户名（ssh-user > SUDO_USER > current/active login user）
func (s *Ssh) resolveActualUser() string {
	if s.option.SshUser != "" {
		return s.option.SshUser
	}
	if u := os.Getenv("SUDO_USER"); u != "" {
		return normalizeLocalUserName(u)
	}
	if cur, _ := userCurrentFunc(); cur != nil {
		name := normalizeLocalUserName(cur.Username)
		if !isServiceAccount(name) {
			return name
		}
		if activeUser, err := activeLoginUserFunc(); err == nil && activeUser != "" {
			return normalizeLocalUserName(activeUser)
		}
		return name
	}
	if activeUser, err := activeLoginUserFunc(); err == nil && activeUser != "" {
		return normalizeLocalUserName(activeUser)
	}
	return ""
}

// prepareSshConfig 自动填充缺失的 User/Port/Key（Zero-Config）
func (s *Ssh) prepareSshConfig(ctx context.Context) (string, error) {
	actualUser := s.resolveActualUser()
	hostCfg, err := s.fetchSshHostConfig(ctx, actualUser, s.option.Server)
	if err != nil {
		log.Warnln("[SSH] Host config resolution failed for %s: %v", s.option.Server, err)
		return net.JoinHostPort(s.option.Server, strconv.Itoa(s.option.Port)), nil
	}

	log.Infoln("[SSH] ssh -G resolved %s -> hostname=%s port=%d user=%s identities=%d",
		s.option.Server, hostCfg.HostName, hostCfg.Port, hostCfg.User, len(hostCfg.IdentityFiles))

	s.updateFromHostConfig(hostCfg, actualUser)
	return net.JoinHostPort(s.option.Server, strconv.Itoa(s.option.Port)), nil
}

// updateFromHostConfig 根据主机配置更新 SSH 选项
var (
	userCurrentFunc     = user.Current
	activeLoginUserFunc = detectActiveLoginUser
)

func normalizeLocalUserName(name string) string {
	if idx := strings.LastIndex(name, "\\"); idx != -1 {
		name = name[idx+1:]
	}
	return strings.TrimSpace(name)
}

func isServiceAccount(name string) bool {
	switch strings.ToLower(normalizeLocalUserName(name)) {
	case "", "root", "system":
		return true
	default:
		return false
	}
}

func detectActiveLoginUser() (string, error) {
	return detectActiveLoginUserWith(runtime.GOOS, func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	})
}

func detectActiveLoginUserWith(goos string, run func(name string, args ...string) ([]byte, error)) (string, error) {
	switch goos {
	case "darwin":
		output, err := run("scutil", "show", "State:/Users/ConsoleUser")
		if err != nil {
			return "", fmt.Errorf("read macos console user: %w", err)
		}
		return parseMacOSConsoleUser(output)
	case "linux":
		output, err := run("loginctl", "list-sessions")
		if err != nil {
			return "", fmt.Errorf("list linux sessions: %w", err)
		}
		return parseLinuxLoginctlOutput(output, func(sessionID string) ([]byte, error) {
			sessionOutput, err := run("loginctl", "show-session", sessionID)
			if err != nil {
				return nil, fmt.Errorf("show linux session %s: %w", sessionID, err)
			}
			return sessionOutput, nil
		})
	case "windows":
		return "", fmt.Errorf("active login user detection is not supported on windows")
	default:
		return "", fmt.Errorf("active login user detection is not implemented for %s", goos)
	}
}

func parseMacOSConsoleUser(output []byte) (string, error) {
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Name") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "Name" {
			continue
		}
		name := normalizeLocalUserName(value)
		if name == "" {
			return "", fmt.Errorf("macos console user is empty")
		}
		if strings.EqualFold(name, "loginwindow") || isServiceAccount(name) {
			return "", fmt.Errorf("macos console user %q is not a real desktop user", name)
		}
		return name, nil
	}
	return "", fmt.Errorf("macos console user not found")
}

func parseLinuxLoginctlOutput(listOutput []byte, sessionLookup func(sessionID string) ([]byte, error)) (string, error) {
	if sessionLookup == nil {
		return "", fmt.Errorf("linux session lookup is nil")
	}

	sessionIDs := parseLinuxSessionIDs(listOutput)
	if len(sessionIDs) == 0 {
		return "", fmt.Errorf("no linux sessions found")
	}

	var lastErr error
	for _, sessionID := range sessionIDs {
		output, err := sessionLookup(sessionID)
		if err != nil {
			lastErr = err
			continue
		}
		if name, ok := parseLinuxSessionProperties(output); ok {
			return name, nil
		}
		lastErr = fmt.Errorf("no eligible active local linux session found")
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no eligible active local linux session found")
}

func parseLinuxSessionIDs(output []byte) []string {
	lines := strings.Split(string(output), "\n")
	sessionIDs := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sessionID := strings.TrimSpace(fields[0])
		if sessionID == "" || strings.EqualFold(sessionID, "SESSION") {
			continue
		}
		if !isLikelyLinuxSessionID(sessionID) || !isLikelyLinuxSessionUID(fields[1]) {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		sessionIDs = append(sessionIDs, sessionID)
	}
	return sessionIDs
}

func isLikelyLinuxSessionID(sessionID string) bool {
	for _, r := range sessionID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return sessionID != ""
}

func isLikelyLinuxSessionUID(uid string) bool {
	if uid == "" {
		return false
	}
	for _, r := range uid {
		if r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func parseLinuxSessionProperties(output []byte) (string, bool) {
	values := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		normalizedValue := strings.ToLower(strings.TrimSpace(value))
		values[normalizedKey] = normalizedValue
		if strings.EqualFold(strings.TrimSpace(key), "Name") {
			values["name"] = normalizeLocalUserName(value)
		}
	}

	name := values["name"]
	if name == "" || isServiceAccount(name) || isDisplayManagerAccount(name) {
		return "", false
	}
	if values["remote"] != "no" {
		return "", false
	}
	if sessionClass := values["class"]; sessionClass != "" && sessionClass != "user" {
		return "", false
	}

	active := values["active"]
	state := values["state"]
	if active == "yes" {
		if state == "" || state == "active" {
			return name, true
		}
		return "", false
	}
	if active != "" {
		return "", false
	}
	if state == "active" {
		return name, true
	}
	return "", false
}

func isDisplayManagerAccount(name string) bool {
	switch strings.ToLower(normalizeLocalUserName(name)) {
	case "gdm", "sddm", "lightdm", "greetd":
		return true
	default:
		return false
	}
}

func (s *Ssh) updateFromHostConfig(cfg *HostConfig, actualUser string) {
	if s.option.UserName == "" && cfg.User != "" {
		s.config.User = cfg.User
	}
	// 仅在用户未指定端口时（port: 0），才使用从 ssh -G 自动探测到的服务器端口。
	// 否则尊重用户手动指定的 port (作为 -W 隧道的 Target Port)。
	if s.option.Port == 0 && cfg.Port != 0 {
		s.option.Port = cfg.Port
	}
	// 只有当配置中没有私钥 且 尚未加载过认证方式时，才尝试自动加载。
	if s.option.PrivateKey == "" && len(s.config.Auth) == 0 && len(cfg.IdentityFiles) > 0 {
		s.loadIdentityFiles(cfg.IdentityFiles, actualUser)
	}
}

// loadIdentityFiles 自动加载私钥文件，尝试列表直到成功
func (s *Ssh) loadIdentityFiles(paths []string, actualUser string) {
	home := s.resolveUserHome(actualUser)

	for _, path := range paths {
		if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
			path = filepath.Join(home, path[2:])
		}
		path = filepath.Clean(path)

		b, err := os.ReadFile(path)
		if err != nil {
			// 对于默认路径，如果不存在，我们只打 Debug 日志
			if os.IsNotExist(err) {
				log.Debugln("[SSH] Identity file not found: %s", path)
			} else {
				log.Warnln("[SSH] Failed to read identity %s: %v", path, err)
			}
			continue
		}

		pKey, err := ssh.ParsePrivateKey(b)
		if err != nil {
			log.Warnln("[SSH] Cannot parse key %s: %v", path, err)
			continue
		}
		s.config.Auth = append(s.config.Auth, ssh.PublicKeys(pKey))
		log.Infoln("[SSH] Auto-loaded key %s for %s", path, s.option.Server)
		return // 成功加载一个即可用
	}

	if s.useSystemSsh {
		// system SSH 模式下，即便 Go 层加载失败，外层进程仍可能成功，所以仅做 Debug 提示
		log.Debugln("[SSH] No valid local identity files loaded for %s (system ssh will use its own auth)", s.option.Server)
	} else if len(s.config.Auth) == 0 {
		log.Warnln("[SSH] All local identity files failed to load for %s", s.option.Server)
	}
}
