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
func fetchSshHostConfig(ctx context.Context, actualUser, hostAlias string) (*HostConfig, error) {
	cacheKey := actualUser + ":" + hostAlias

	hostMutex.RLock()
	if c, ok := hostConfigCache[cacheKey]; ok {
		hostMutex.RUnlock()
		return c, nil
	}
	hostMutex.RUnlock()

	cmd := buildSshGCommand(ctx, actualUser, hostAlias)
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
	args := []string{"-o", "BatchMode=yes", "-o", "ControlMaster=no"}
	args = append(args, s.option.SshFlags...)
	args = append(args, "-W", targetAddr, hostAlias)
	return args
}

// applyEnv 为 SSH 进程注入环境变量
func (s *Ssh) applyEnv(cmd *exec.Cmd, capturedEnv []string) {
	if runtime.GOOS == "windows" {
		// Windows 上，如果 fetchUserEnv 返回的是基础环境，我们尽量不手动设置 cmd.Env
		// 避免 Go 在处理系统环境变量（如 SYSTEMROOT）时出现微妙的缺失导致 ssh 无法解析主机名
		if s.option.SshUserHome != "" {
			cmd.Env = os.Environ()
			cmd.Env = append(cmd.Env, "HOME="+s.option.SshUserHome)
			cmd.Env = append(cmd.Env, "USERPROFILE="+s.option.SshUserHome)
		}
		return
	}

	if capturedEnv != nil {
		cmd.Env = capturedEnv
	} else {
		cmd.Env = os.Environ()
	}

	if s.option.SshUserHome != "" {
		cmd.Env = append(cmd.Env, "HOME="+s.option.SshUserHome)
	}
}

// ─── Zero-Config Resolution ─────────────────────────────────────────────────

// resolveActualUser 按优先级解析实际用户名（ssh-user > ssh-user-home > SUDO_USER > current）
func (s *Ssh) resolveActualUser() string {
	if s.option.SshUser != "" {
		return s.option.SshUser
	}
	if s.option.SshUserHome != "" {
		// Use filepath.Base to cross-platform extract the user's folder name from their home dir
		return filepath.Base(s.option.SshUserHome)
	}
	if u := os.Getenv("SUDO_USER"); u != "" {
		return u
	}
	if cur, _ := user.Current(); cur != nil {
		name := cur.Username
		if idx := strings.LastIndex(name, "\\"); idx != -1 {
			name = name[idx+1:]
		}
		return name
	}
	return ""
}

// prepareSshConfig 自动填充缺失的 User/Port/Key（Zero-Config）
func (s *Ssh) prepareSshConfig(ctx context.Context) (string, error) {
	actualUser := s.resolveActualUser()
	hostCfg, err := fetchSshHostConfig(ctx, actualUser, s.option.Server)
	if err != nil {
		log.Warnln("[SSH] Host config resolution failed for %s: %v", s.option.Server, err)
		return net.JoinHostPort(s.option.Server, strconv.Itoa(s.option.Port)), nil
	}

	s.updateFromHostConfig(hostCfg, actualUser)
	return net.JoinHostPort(s.option.Server, strconv.Itoa(s.option.Port)), nil
}

// updateFromHostConfig 根据主机配置更新 SSH 选项
func (s *Ssh) updateFromHostConfig(cfg *HostConfig, actualUser string) {
	if s.option.UserName == "" && cfg.User != "" {
		s.config.User = cfg.User
	}
	if s.option.Port == 0 && cfg.Port != 0 {
		s.option.Port = cfg.Port
	}
	if s.option.PrivateKey == "" && len(cfg.IdentityFiles) > 0 {
		s.loadIdentityFiles(cfg.IdentityFiles, actualUser)
	}
}

// loadIdentityFiles 自动加载私钥文件，尝试列表直到成功
func (s *Ssh) loadIdentityFiles(paths []string, actualUser string) {
	for _, path := range paths {
		if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
			home := s.resolveUserHome(actualUser)
			path = filepath.Join(home, path[2:])
		}

		b, err := os.ReadFile(path)
		if err != nil {
			log.Debugln("[SSH] Skipping identity %s: %v", path, err)
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
	log.Warnln("[SSH] All identity files failed to load for %s", s.option.Server)
}


