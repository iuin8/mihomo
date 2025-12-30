package outbound

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/metacubex/mihomo/log"
)

// dialViaSystemSsh 使用系统 SSH 命令建立连接
func (s *Ssh) dialViaSystemSsh(ctx context.Context, hostAlias string) (net.Conn, error) {
	log.Infoln("[SSH] Using system SSH with config alias: %s", hostAlias)

	port := s.option.Port
	if port == 0 {
		port = 22
	}

	// 获取实际用户（非root）
	var actualUser string
	var cmdArgs []string

	// 方法0: 用户显式指定（最高优先级）
	if s.option.SshUser != "" {
		actualUser = s.option.SshUser
		log.Infoln("[SSH] Using configured user: %s", actualUser)
	}

	// 方法1: 从配置中的 ssh-user-home 提取
	if actualUser == "" && s.option.SshUserHome != "" {
		// 从路径提取用户名: /Users/fa -> fa
		parts := strings.Split(s.option.SshUserHome, "/")
		if len(parts) >= 3 && parts[1] == "Users" {
			actualUser = parts[2]
			log.Infoln("[SSH] Detected user from ssh-user-home: %s", actualUser)
		}
	}

	// 方法2: 从环境变量获取
	if actualUser == "" {
		actualUser = os.Getenv("SUDO_USER")
		if actualUser != "" {
			log.Infoln("[SSH] Detected user from SUDO_USER: %s", actualUser)
		}
	}

	// 方法3: 检查/Users目录（macOS）- 跳过隐藏目录
	if actualUser == "" {
		entries, err := os.ReadDir("/Users")
		if err == nil {
			for _, entry := range entries {
				name := entry.Name()
				// 跳过隐藏目录(.开头)、系统目录
				if entry.IsDir() && !strings.HasPrefix(name, ".") &&
					name != "Shared" && name != "Guest" {
					actualUser = name
					log.Infoln("[SSH] Auto-detected user from /Users: %s", actualUser)
					break
				}
			}
		}
	}

	targetAddr := fmt.Sprintf("localhost:%d", port)

	if actualUser != "" {
		// 以实际用户身份执行: sudo -n -u username -H ssh -o BatchMode=yes -W ...
		// -n: 非交互模式，如果需要密码则立即失败
		// -H: 设置 HOME 环境变量为目标用户的主目录
		// -o BatchMode=yes: 禁止 ssh 询问密码或确认主机密钥
		cmdArgs = []string{"-n", "-u", actualUser, "-H", "ssh", "-o", "BatchMode=yes", "-W", targetAddr, hostAlias}
		log.Infoln("[SSH] Running as user: %s (non-interactive)", actualUser)
		log.Infoln("[SSH] Command: sudo %s", strings.Join(cmdArgs, " "))
	} else {
		// 回退：直接执行（BatchMode 确保不挂起）
		cmdArgs = []string{"-o", "BatchMode=yes", "-W", targetAddr, hostAlias}
		log.Warnln("[SSH] Could not determine actual user, running ssh directly")
		log.Infoln("[SSH] Command: ssh %s", strings.Join(cmdArgs, " "))
	}

	var cmd *exec.Cmd
	if actualUser != "" {
		cmd = exec.CommandContext(ctx, "sudo", cmdArgs...)
	} else {
		cmd = exec.CommandContext(ctx, "ssh", cmdArgs...)
	}

	// 设置 HOME 环境变量（如果用户指定）
	if s.option.SshUserHome != "" {
		cmd.Env = append(cmd.Environ(), "HOME="+s.option.SshUserHome)
		log.Infoln("[SSH] Setting HOME=%s for SSH command", s.option.SshUserHome)
	}

	// 获取 stdin/stdout
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	// 获取 stderr pipe 用于实时日志
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start ssh: %w", err)
	}

	log.Infoln("[SSH] SSH subprocess started, PID: %d", cmd.Process.Pid)

	// 实时转发 stderr 到日志
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			log.Warnln("[SSH-STDERR] %s", scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			log.Debugln("[SSH-STDERR] pipe error: %v", err)
		}
	}()

	// 监控进程退出
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Errorln("[SSH] SSH process exited with error: %v", err)
		} else {
			log.Infoln("[SSH] SSH process exited normally")
		}
	}()

	// 返回包装的连接
	return &sshCmdConn{
		stdin:  stdin,
		stdout: stdout,
		cmd:    cmd,
	}, nil
}

// sshCmdConn 实现 net.Conn 接口
type sshCmdConn struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	cmd    *exec.Cmd
}

func (c *sshCmdConn) Read(b []byte) (n int, err error) {
	return c.stdout.Read(b)
}

func (c *sshCmdConn) Write(b []byte) (n int, err error) {
	return c.stdin.Write(b)
}

func (c *sshCmdConn) Close() error {
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}

func (c *sshCmdConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4zero, Port: 0}
}

func (c *sshCmdConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4zero, Port: 0}
}

func (c *sshCmdConn) SetDeadline(t time.Time) error {
	return nil
}

func (c *sshCmdConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (c *sshCmdConn) SetWriteDeadline(t time.Time) error {
	return nil
}
