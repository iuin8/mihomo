package outbound

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/log"
)

// ─── Host Config ────────────────────────────────────────────────────────────

type HostConfig struct {
	User          string
	Port          int
	IdentityFiles []string
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
	if runtime.GOOS != "windows" {
		cur, _ := user.Current()
		if actualUser != "" && (cur == nil || cur.Username != actualUser) {
			return exec.CommandContext(ctx, "sudo", "-n", "-u", actualUser, "-H", "ssh", "-G", hostAlias)
		}
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
			if !strings.Contains(parts[1], "none") {
				cfg.IdentityFiles = append(cfg.IdentityFiles, parts[1])
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
	if runtime.GOOS != "windows" {
		cur, _ := user.Current()
		if actualUser != "" && (cur == nil || cur.Username != actualUser) {
			args := append([]string{"-n", "-u", actualUser, "-H", "ssh"}, sshArgs...)
			log.Infoln("[SSH] Dialing as user: %s via sudo", actualUser)
			return exec.Command("sudo", args...)
		}
	}
	log.Infoln("[SSH] Dialing as current user: %s", actualUser)
	return exec.Command("ssh", sshArgs...)
}

// startSshProcess 启动 SSH 子进程，设置 pipe 并返回 net.Conn
func (s *Ssh) startSshProcess(cmd *exec.Cmd, actualUser string) (*sshCmdConn, error) {
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

	intentionalClose := &atomic.Bool{}
	s.monitorProcess(cmd, actualUser, intentionalClose)

	return &sshCmdConn{
		stdin:            stdinW,
		stdout:           stdoutR,
		cmd:              cmd,
		intentionalClose: intentionalClose,
	}, nil
}

// monitorProcess 在后台等待进程退出并清理环境
func (s *Ssh) monitorProcess(cmd *exec.Cmd, actualUser string, intentionalClose *atomic.Bool) {
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
}

// resolveUserHome 获取用户的主目录
func (s *Ssh) resolveUserHome(actualUser string) string {
	if s.option.SshUserHome != "" {
		return s.option.SshUserHome
	}
	if actualUser != "" {
		if u, err := user.Lookup(actualUser); err == nil {
			return u.HomeDir
		}
	}
	home, _ := os.UserHomeDir()
	return home
}
