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
	port  int
	cmd   *exec.Cmd
	inUse bool
}

func (s *Ssh) setupSystemSocks(ctx context.Context) error {
	s.cMutex.Lock()
	// 注意：此处不再使用 defer s.cMutex.Unlock()，因为我们在进程启动后需要提前释放锁以防止死锁

	if s.socksExt == nil || !s.socksExt.inUse {
		s.cMutex.Unlock()
		return nil
	}

	if s.socksExt.cmd != nil && s.socksExt.cmd.Process != nil {
		s.cMutex.Unlock()
		return nil // 已经启动
	}

	actualUser := s.resolveActualUser()

	// 1. 确定端口
	if s.socksExt.port == 0 {
		p, err := LookForFreePort()
		if err != nil {
			s.cMutex.Unlock()
			return fmt.Errorf("find free port: %w", err)
		}
		s.socksExt.port = p
	}

	// 1.5 确保 SSH 配置已加载 (执行 ssh -G 预解析)
	if _, err := s.prepareSshConfig(ctx); err != nil {
		log.Warnln("[SSH] prepareSshConfig failed: %v", err)
	}

	// 2. 启动进程
	// 使用 context.Background() 确保进程生命周期不随某次 Dial 请求而结束
	cmd := buildSshDCommand(context.Background(), actualUser, s.option.Server, s.socksExt.port, s.option.SshFlags)
	capturedEnv, _ := fetchUserEnv(ctx, actualUser)
	s.applyEnv(cmd, capturedEnv)

	// 模仿 startSshProcess 重定向 I/O 以获得更好的隔离性
	stdinR, stdinW, _ := os.Pipe()
	stdoutR, stdoutW, _ := os.Pipe()
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	defer func() {
		_ = stdinR.Close()
		_ = stdoutW.Close()
	}()

	// 处理 stderr 以便记录日志
	stderrPipe, _ := cmd.StderrPipe()
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			log.Warnln("[SSH-SOCKS-STDERR] %s", scanner.Text())
		}
	}()

	if err := cmd.Start(); err != nil {
		_ = stdinW.Close()
		_ = stdoutR.Close()
		s.socksExt.cmd = nil
		s.cMutex.Unlock()
		return fmt.Errorf("start ssh -D: %w", err)
	}

	// 关闭不需要的管道端
	_ = stdinW.Close()
	go func() {
		_, _ = io.Copy(io.Discard, stdoutR)
		_ = stdoutR.Close()
	}()

	s.socksExt.cmd = cmd
	s.cMutex.Unlock() // <<< 关键：在等待端口就绪前释放锁，防止递归拨号导致死锁

	log.Infoln("[SSH] System SOCKS5 tunnel started on localhost:%d (PID: %d)", s.socksExt.port, cmd.Process.Pid)

	// 监控进程退出 (不需要锁，或者在内部加锁)
	go func() {
		err := cmd.Wait()
		s.cMutex.Lock()
		defer s.cMutex.Unlock()
		if s.socksExt != nil && s.socksExt.cmd == cmd {
			s.socksExt.cmd = nil
			if !s.closed {
				log.Errorln("[SSH] System SOCKS5 tunnel process (PID: %d) exited: %v", cmd.Process.Pid, err)
			}
		}
	}()

	// 等待端口就绪 (不持锁)
	for i := 0; i < 15; i++ {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", s.socksExt.port), 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	return fmt.Errorf("ssh -D port %d failed to listen in time", s.socksExt.port)
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
