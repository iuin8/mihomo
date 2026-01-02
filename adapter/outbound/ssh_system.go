package outbound

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/log"
)

var (
	userEnvCache = make(map[string][]string)
	envMutex     sync.RWMutex
)

// fetchUserEnv 获取指定用户的全量登录环境变量
func fetchUserEnv(ctx context.Context, actualUser string) ([]string, error) {
	currentUser, _ := user.Current()
	if actualUser == "" || (currentUser != nil && currentUser.Username == actualUser) {
		return os.Environ(), nil
	}

	envMutex.RLock()
	if cached, ok := userEnvCache[actualUser]; ok {
		envMutex.RUnlock()
		return cached, nil
	}
	envMutex.RUnlock()

	log.Debugln("[SSH] Capturing full login environment for user: %s", actualUser)

	// 使用 sudo -n -u <user> -H -i env 抓取
	// -i 保证是登录 Shell，能加载 .zshrc/.bashrc 等
	cmd := exec.CommandContext(ctx, "sudo", "-n", "-u", actualUser, "-H", "-i", "env")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to capture user env: %w", err)
	}

	var env []string
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 简单验证是否是有效的 KEY=VALUE 格式，过滤掉可能的 MOTD 信息
		if strings.Contains(line, "=") && !strings.HasPrefix(line, "Last login:") {
			env = append(env, line)
		}
	}

	envMutex.Lock()
	userEnvCache[actualUser] = env
	envMutex.Unlock()

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
	log.Warnln("[SSH] Cleared environment cache for user: %s due to failure", actualUser)
}

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
	}

	// 方法1: 从配置中的 ssh-user-home 提取
	if actualUser == "" && s.option.SshUserHome != "" {
		// 从路径提取用户名: /Users/fa -> fa
		parts := strings.Split(s.option.SshUserHome, "/")
		if len(parts) >= 3 && parts[1] == "Users" {
			actualUser = parts[2]
		}
	}

	// 方法2: 从环境变量获取
	if actualUser == "" {
		actualUser = os.Getenv("SUDO_USER")
	}

	// 方法3: 检查/Users目录（macOS）- 跳过隐藏目录
	if actualUser == "" {
		entries, err := os.ReadDir("/Users")
		if err == nil {
			for _, entry := range entries {
				name := entry.Name()
				if entry.IsDir() && !strings.HasPrefix(name, ".") &&
					name != "Shared" && name != "Guest" {
					actualUser = name
					break
				}
			}
		}
	}

	targetAddr := fmt.Sprintf("localhost:%d", port)

	sshArgs := []string{"-o", "BatchMode=yes", "-W", targetAddr, hostAlias}
	var cmd *exec.Cmd

	// 检查当前进程用户
	currentUser, _ := user.Current()
	if actualUser != "" && (currentUser == nil || currentUser.Username != actualUser) {
		cmdArgs = append([]string{"-n", "-u", actualUser, "-H", "ssh"}, sshArgs...)
		cmd = exec.CommandContext(ctx, "sudo", cmdArgs...)
		log.Infoln("[SSH] Dialing as user: %s via sudo", actualUser)
	} else {
		cmd = exec.CommandContext(ctx, "ssh", sshArgs...)
		log.Infoln("[SSH] Dialing as current user: %s", actualUser)
	}

	// 抓取并注入全量环境变量，确保 ProxyCommand (如 cloudflared) 的 Context/Token 完整
	env, err := fetchUserEnv(ctx, actualUser)
	if err != nil {
		log.Warnln("[SSH] Failed to capture full environment: %v, falling back to basic env", err)
		env = os.Environ()
	}
	cmd.Env = env

	// 设置 HOME 环境变量（如果用户指定，且 env 中没有或需要覆盖）
	if s.option.SshUserHome != "" {
		cmd.Env = append(cmd.Env, "HOME="+s.option.SshUserHome)
	}

	log.Debugln("[SSH] Command: %s %s", cmd.Path, strings.Join(cmd.Args[1:], " "))

	// 手动创建 Pipe 以获得 *os.File，从而支持 SetDeadline
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW

	// 获取 stderr pipe 用于实时日志
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, fmt.Errorf("failed to start ssh: %w", err)
	}

	// 关闭子进程使用的那一端，保留当前进程使用的这一端
	_ = stdinR.Close()
	_ = stdoutW.Close()

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
			clearUserEnv(actualUser)
		}
	}()

	// 返回包装的连接
	return &sshCmdConn{
		stdin:  stdinW,
		stdout: stdoutR,
		cmd:    cmd,
	}, nil
}

// sshCmdConn 实现 net.Conn 接口
type sshCmdConn struct {
	stdin  *os.File
	stdout *os.File
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
	_ = c.stdin.SetDeadline(t)
	return c.stdout.SetDeadline(t)
}

func (c *sshCmdConn) SetReadDeadline(t time.Time) error {
	return c.stdout.SetReadDeadline(t)
}

func (c *sshCmdConn) SetWriteDeadline(t time.Time) error {
	return c.stdin.SetWriteDeadline(t)
}
