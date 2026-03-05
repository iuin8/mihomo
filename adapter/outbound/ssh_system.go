package outbound

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/log"
	"golang.org/x/crypto/ssh"
)

// ─── Host Config Cache ──────────────────────────────────────────────────────

type HostConfig struct {
	User         string
	Port         int
	IdentityFile string
}

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

// ─── System SSH Dialing ─────────────────────────────────────────────────────

// dialViaSystemSsh 使用系统 SSH 命令建立连接，返回 net.Conn
func (s *Ssh) dialViaSystemSsh(ctx context.Context, hostAlias string) (net.Conn, error) {
	port := s.option.Port
	if port == 0 {
		port = 22
	}
	actualUser := s.resolveActualUser()

	// 构建 SSH 参数
	// ControlMaster=no 是必须的：Mihomo 使用 os.Pipe() 接管 I/O，
	// 与 ControlMaster 的 fd 复用机制冲突，会导致管道断裂。
	targetAddr := fmt.Sprintf("localhost:%d", port)
	sshArgs := []string{"-o", "BatchMode=yes", "-o", "ControlMaster=no"}
	sshArgs = append(sshArgs, s.option.SshFlags...)
	sshArgs = append(sshArgs, "-W", targetAddr, hostAlias)

	cmd := buildSshCommand(actualUser, sshArgs)

	// 注入用户环境变量
	env, err := fetchUserEnv(ctx, actualUser)
	if err != nil {
		env = os.Environ()
	}
	cmd.Env = env
	if s.option.SshUserHome != "" {
		cmd.Env = append(cmd.Env, "HOME="+s.option.SshUserHome)
	}

	log.Debugln("[SSH] Command: %s %s", cmd.Path, strings.Join(cmd.Args[1:], " "))

	conn, err := startSshProcess(cmd, actualUser)
	if err != nil {
		return nil, err
	}

	log.Infoln("[SSH] Subprocess started for %s (PID: %d)", hostAlias, cmd.Process.Pid)
	return conn, nil
}

// ─── Zero-Config Resolution ─────────────────────────────────────────────────

// resolveActualUser 按优先级解析实际用户名（ssh-user > ssh-user-home > SUDO_USER）
func (s *Ssh) resolveActualUser() string {
	if s.option.SshUser != "" {
		return s.option.SshUser
	}
	if s.option.SshUserHome != "" {
		if parts := strings.Split(s.option.SshUserHome, "/"); len(parts) >= 3 && parts[1] == "Users" {
			return parts[2]
		}
	}
	return os.Getenv("SUDO_USER")
}

// prepareSshConfig 自动填充缺失的 User/Port/Key（Zero-Config）
func (s *Ssh) prepareSshConfig(ctx context.Context) (string, error) {
	actualUser := s.resolveActualUser()
	hostCfg, err := fetchSshHostConfig(ctx, actualUser, s.option.Server)
	if err != nil {
		log.Warnln("[SSH] Host config resolution failed for %s: %v", s.option.Server, err)
	} else {
		if s.option.UserName == "" && hostCfg.User != "" {
			s.config.User = hostCfg.User
		}
		if s.option.Port == 0 && hostCfg.Port != 0 {
			s.option.Port = hostCfg.Port
		}
		if s.option.PrivateKey == "" && hostCfg.IdentityFile != "" {
			s.loadIdentityFile(hostCfg.IdentityFile, actualUser)
		}
	}
	return net.JoinHostPort(s.option.Server, strconv.Itoa(s.option.Port)), nil
}

// loadIdentityFile 自动加载私钥文件
func (s *Ssh) loadIdentityFile(path, actualUser string) {
	if strings.HasPrefix(path, "~/") {
		home := s.option.SshUserHome
		if home == "" && actualUser != "" {
			home = "/Users/" + actualUser
		}
		path = strings.Replace(path, "~", home, 1)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		log.Warnln("[SSH] Cannot read key %s: %v", path, err)
		return
	}
	pKey, err := ssh.ParsePrivateKey(b)
	if err != nil {
		log.Warnln("[SSH] Cannot parse key %s: %v", path, err)
		return
	}
	s.config.Auth = append(s.config.Auth, ssh.PublicKeys(pKey))
	log.Infoln("[SSH] Auto-loaded key for %s", s.option.Server)
}

// ─── sshCmdConn: net.Conn over SSH subprocess pipes ─────────────────────────

type sshCmdConn struct {
	stdin            *os.File
	stdout           *os.File
	cmd              *exec.Cmd
	intentionalClose *atomic.Bool
}

func (c *sshCmdConn) Read(b []byte) (int, error)  { return c.stdout.Read(b) }
func (c *sshCmdConn) Write(b []byte) (int, error) { return c.stdin.Write(b) }

func (c *sshCmdConn) Close() error {
	c.intentionalClose.Store(true)
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	// 不在此处调用 c.cmd.Wait()，因为 startSshProcess 的后台 goroutine 已经在 Wait
	return nil
}

func (c *sshCmdConn) LocalAddr() net.Addr  { return &net.TCPAddr{IP: net.IPv4zero} }
func (c *sshCmdConn) RemoteAddr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero} }

func (c *sshCmdConn) SetDeadline(t time.Time) error {
	_ = c.stdin.SetDeadline(t)
	return c.stdout.SetDeadline(t)
}
func (c *sshCmdConn) SetReadDeadline(t time.Time) error  { return c.stdout.SetReadDeadline(t) }
func (c *sshCmdConn) SetWriteDeadline(t time.Time) error { return c.stdin.SetWriteDeadline(t) }

// ─── Internal Helpers ───────────────────────────────────────────────────────

// buildSshGCommand 构建 ssh -G 命令
func buildSshGCommand(ctx context.Context, actualUser, hostAlias string) *exec.Cmd {
	cur, _ := user.Current()
	if actualUser != "" && (cur == nil || cur.Username != actualUser) {
		return exec.CommandContext(ctx, "sudo", "-n", "-u", actualUser, "-H", "ssh", "-G", hostAlias)
	}
	return exec.CommandContext(ctx, "ssh", "-G", hostAlias)
}

// parseSshGOutput 从 ssh -G 输出中提取 User/Port/IdentityFile
func parseSshGOutput(output string) *HostConfig {
	cfg := &HostConfig{}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) < 2 {
			continue
		}
		switch strings.ToLower(parts[0]) {
		case "user":
			cfg.User = parts[1]
		case "port":
			cfg.Port, _ = strconv.Atoi(parts[1])
		case "identityfile":
			if cfg.IdentityFile == "" && parts[1] != "~/.ssh/id_rsa" && !strings.Contains(parts[1], "none") {
				cfg.IdentityFile = parts[1]
			}
		}
	}
	return cfg
}

// buildSshCommand 构建 SSH 命令（需要 sudo 时自动包装）
// 注意：不要使用 exec.CommandContext(ctx, ...)，因为传入的 ctx 通常是 DialContext，
// 带有很短的超时时间（如 5s）。如果连接比较慢，ctx 会取消并发送 SIGKILL 杀掉 SSH 进程，
// 导致整个长连接隧道崩溃。我们通过内部的 os.Pipe() 和 client.Close() 自己管理生命周期。
func buildSshCommand(actualUser string, sshArgs []string) *exec.Cmd {
	cur, _ := user.Current()
	if actualUser != "" && (cur == nil || cur.Username != actualUser) {
		args := append([]string{"-n", "-u", actualUser, "-H", "ssh"}, sshArgs...)
		log.Infoln("[SSH] Dialing as user: %s via sudo", actualUser)
		return exec.Command("sudo", args...)
	}
	log.Infoln("[SSH] Dialing as current user: %s", actualUser)
	return exec.Command("ssh", sshArgs...)
}

// startSshProcess 启动 SSH 子进程，设置 pipe 并返回 net.Conn
func startSshProcess(cmd *exec.Cmd, actualUser string) (*sshCmdConn, error) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("start ssh: %w", err)
	}

	_ = stdinR.Close()
	_ = stdoutW.Close()

	// stderr → log
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			log.Warnln("[SSH-STDERR] %s", scanner.Text())
		}
	}()

	// 监控进程退出
	intentionalClose := &atomic.Bool{}
	go func() {
		err := cmd.Wait()
		if intentionalClose.Load() {
			log.Debugln("[SSH] Process exited normally after Close() for %s", actualUser)
			return
		}
		if err != nil {
			log.Errorln("[SSH] Process exited with error: %v", err)
			clearUserEnv(actualUser)
		}
	}()

	return &sshCmdConn{
		stdin:            stdinW,
		stdout:           stdoutR,
		cmd:              cmd,
		intentionalClose: intentionalClose,
	}, nil
}
