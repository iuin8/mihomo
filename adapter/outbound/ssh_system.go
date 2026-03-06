package outbound

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
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

// resolveActualUser 按优先级解析实际用户名（ssh-user > SUDO_USER > current）
func (s *Ssh) resolveActualUser() string {
	if s.option.SshUser != "" {
		return s.option.SshUser
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
