package outbound

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"time"

	"github.com/metacubex/mihomo/log"
	N "github.com/metacubex/mihomo/common/net"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/transport/socks5"
)

type systemSocksExt struct {
	port      int
	cmd       *exec.Cmd
	inUse     bool
	waitReady chan struct{} // 当 SOCKS5 隧道就绪时关闭此通道
	lastErr   error
}

func (s *Ssh) setupSystemSocks(ctx context.Context) error {
	s.cMutex.Lock()
	if s.socksExt == nil || !s.socksExt.inUse {
		s.cMutex.Unlock()
		return nil
	}

	// 1. 如果已经在运行且已就绪 (waitReady == nil 表示不在启动中且已成功过)
	if s.socksExt.cmd != nil && s.socksExt.cmd.Process != nil && s.socksExt.waitReady == nil {
		s.cMutex.Unlock()
		return nil
	}

	// 2. 如果正在启动中，等待就绪
	if s.socksExt.waitReady != nil {
		waitChan := s.socksExt.waitReady
		s.cMutex.Unlock()
		select {
		case <-waitChan:
			s.cMutex.Lock()
			defer s.cMutex.Unlock()
			if s.socksExt == nil {
				return fmt.Errorf("ssh proxy closed during wait")
			}
			return s.socksExt.lastErr
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
			return fmt.Errorf("timeout waiting for ssh tunnel ready")
		}
	}

	// 3. 开始启动流程
	s.socksExt.waitReady = make(chan struct{})
	s.socksExt.lastErr = nil
	actualUser := s.resolveActualUser()

	// 确定端口
	if s.socksExt.port == 0 {
		p, err := LookForFreePort()
		if err != nil {
			s.socksExt.lastErr = fmt.Errorf("find free port: %w", err)
			close(s.socksExt.waitReady)
			s.socksExt.waitReady = nil
			s.cMutex.Unlock()
			return s.socksExt.lastErr
		}
		s.socksExt.port = p
	}

	// 执行预解析 (ssh -G) - 释放锁以防死锁
	s.cMutex.Unlock()

	if _, err := s.prepareSshConfig(ctx); err != nil {
		log.Warnln("[SSH] prepareSshConfig failed: %v", err)
	}

	// 重新加锁以准备启动进程
	s.cMutex.Lock()
	cmd := buildSshDCommand(context.Background(), actualUser, s.option.Server, s.socksExt.port, s.option.SshFlags)
	capturedEnv, _ := fetchUserEnv(ctx, actualUser)
	s.applyEnv(cmd, capturedEnv)

	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	
	stderrPipe, _ := cmd.StderrPipe()
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			log.Warnln("[SSH-SOCKS-STDERR] %s", scanner.Text())
		}
	}()

	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		s.socksExt.lastErr = fmt.Errorf("start ssh -D: %w", err)
		close(s.socksExt.waitReady)
		s.socksExt.waitReady = nil
		s.cMutex.Unlock()
		return s.socksExt.lastErr
	}

	_ = stdinR.Close()
	_ = stdinW.Close()
	go func() {
		_, _ = io.Copy(io.Discard, stdoutR)
		_ = stdoutR.Close()
		_ = stdoutW.Close()
	}()

	s.socksExt.cmd = cmd
	s.cMutex.Unlock() // 释放锁，允许其他并发请求进入等待逻辑

	log.Infoln("[SSH] System SOCKS5 tunnel starting on 127.0.0.1:%d (PID: %d)", s.socksExt.port, cmd.Process.Pid)

	// 监控进程退出
	go func() {
		err := cmd.Wait()
		s.cMutex.Lock()
		defer s.cMutex.Unlock()
		if s.socksExt != nil && s.socksExt.cmd == cmd {
			s.socksExt.cmd = nil
			s.socksExt.waitReady = nil // 允许下次 Dial 时重新触发启动
			if !s.closed {
				log.Errorln("[SSH] System SOCKS5 tunnel process (PID: %d) exited: %v", cmd.Process.Pid, err)
			}
		}
	}()

	// 轮询等待端口就绪
	var portErr error
	for i := 0; i < 20; i++ {
		// 增加重试次数到 20 次 (总计约 5-7 秒)，确保慢速 SSH 也能对齐
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.socksExt.port), 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			portErr = nil
			break
		}
		portErr = err
		time.Sleep(250 * time.Millisecond)
	}

	s.cMutex.Lock()
	defer s.cMutex.Unlock()
	
	if portErr != nil {
		s.socksExt.lastErr = fmt.Errorf("ssh -D port %d failed: %v", s.socksExt.port, portErr)
		if s.socksExt.cmd == cmd {
			_ = cmd.Process.Kill()
			s.socksExt.cmd = nil
		}
	}

	// 关闭等待信号并清理状态
	close(s.socksExt.waitReady)
	s.socksExt.waitReady = nil
	return s.socksExt.lastErr
}

// buildSshDCommand 构建 ssh -N -D 命令（动态端口转发）
func buildSshDCommand(ctx context.Context, actualUser, hostAlias string, localPort int, extraFlags []string) *exec.Cmd {
	// -T: 禁用 TTY，避免交互挂起
	// -o Tunnel=no: 防止 SSH 尝试创建系统 utun 接口冲突
	// -o ServerAlive*: 保持底层 TCP 活跃
	sshArgs := []string{
		"-T", "-N", "-D", strconv.Itoa(localPort),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "Tunnel=no",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
	}
	sshArgs = append(sshArgs, extraFlags...)
	sshArgs = append(sshArgs, hostAlias)

	if runtime.GOOS != "windows" {
		cur, _ := user.Current()
		if actualUser != "" && (cur == nil || cur.Username != actualUser) {
			args := append([]string{"-n", "-u", actualUser, "-H", "ssh"}, sshArgs...)
			return exec.Command("sudo", args...)
		}
	}
	return exec.Command("ssh", sshArgs...)
}

// LookForFreePort 寻找一个可用的本地 TCP 端口
func LookForFreePort() (int, error) {
	// 注意：此处必须使用 127.0.0.1 而不是 localhost，
	// 因为 Mihomo 会 hook 默认 resolver 并 panic 掉任何主机名解析（包括 localhost）
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// dialSystemSocks 执行单层 SOCKS5 拨号逻辑
func (s *Ssh) dialSystemSocks(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	if s.socksExt == nil || !s.socksExt.inUse {
		return nil, fmt.Errorf("socks extension not initialized")
	}
	if err := s.setupSystemSocks(ctx); err != nil {
		return nil, err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(s.socksExt.port))
	c, err := s.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial local socks: %w", err)
	}

	fail := true
	defer func() {
		if fail {
			_ = c.Close()
		}
	}()

	if ctx.Done() != nil {
		done := N.SetupContextForConn(ctx, c)
		defer done(&err)
	}

	target := socks5.ParseAddr(metadata.RemoteAddress())
	if _, err := socks5.ClientHandshake(c, target, socks5.CmdConnect, nil); err != nil {
		return nil, fmt.Errorf("socks5 handshake: %w", err)
	}

	fail = false
	return NewConn(c, s), nil
}

// cleanupSocks 清理 SOCKS5 进程
func (s *Ssh) cleanupSocks() {
	if s.socksExt != nil && s.socksExt.cmd != nil && s.socksExt.cmd.Process != nil {
		log.Infoln("[SSH] Cleaning up system SOCKS5 tunnel (PID: %d)", s.socksExt.cmd.Process.Pid)
		_ = s.socksExt.cmd.Process.Kill()
	}
}
