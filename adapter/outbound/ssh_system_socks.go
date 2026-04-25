package outbound

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"
)

type systemSocksExt struct {
	port        int
	cmd         *exec.Cmd
	processDone <-chan struct{}
	inUse       bool
	pinned      bool
	waitReady   chan struct{}
	lastErr     error
}

type sshOption struct {
	name  string
	value string
}

func (s *Ssh) setupSystemSocks(ctx context.Context) error {
	return s.setupSystemSocksForOS(ctx, runtime.GOOS)
}

func (s *Ssh) setupSystemSocksForOS(ctx context.Context, goos string) error {
	s.cMutex.Lock()
	if s.socksExt == nil || !s.socksExt.inUse {
		s.cMutex.Unlock()
		return nil
	}
	if s.socksExt.cmd != nil && s.socksExt.cmd.Process != nil && s.socksExt.waitReady == nil {
		select {
		case <-s.socksExt.processDone:
			s.socksExt.cmd = nil
			s.socksExt.processDone = nil
			s.socks = nil
			if !s.socksExt.pinned {
				s.socksExt.port = 0
			}
		default:
			s.cMutex.Unlock()
			return nil
		}
	}
	waitChan := s.socksExt.waitReady
	if waitChan == nil {
		waitChan = make(chan struct{})
		s.socksExt.waitReady = waitChan
		s.socksExt.lastErr = nil
		actualUser := s.resolveActualUser()
		if err := requireSystemSshUserForOS(actualUser, goos); err != nil {
			s.socksExt.lastErr = err
			close(waitChan)
			s.socksExt.waitReady = nil
			s.cMutex.Unlock()
			return err
		}
		port := s.socksExt.port
		if port == 0 {
			p, err := LookForFreePort()
			if err != nil {
				s.socksExt.lastErr = fmt.Errorf("find free port: %w", err)
				close(waitChan)
				s.socksExt.waitReady = nil
				s.cMutex.Unlock()
				return s.socksExt.lastErr
			}
			port = p
		}
		go s.startSystemSocks(actualUser, port, waitChan)
	}
	s.cMutex.Unlock()
	return s.waitSystemSocks(ctx, waitChan)
}

func (s *Ssh) waitSystemSocks(ctx context.Context, waitChan <-chan struct{}) error {
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

func (s *Ssh) startSystemSocks(actualUser string, port int, waitReady chan struct{}) {
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStartup()

	s.cMutex.Lock()
	if s.closed || s.socksExt == nil || s.socksExt.waitReady != waitReady {
		s.finishSystemSocksStartLocked(waitReady, fmt.Errorf("ssh adapter is closed"), nil)
		s.cMutex.Unlock()
		return
	}
	cmd := buildSshDCommand(actualUser, s.option.Server, port, s.option.SshFlags)
	capturedEnv, _ := fetchUserEnv(startupCtx, actualUser)
	applyEnv(cmd, capturedEnv)

	cmd.Stdout = io.Discard
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		s.finishSystemSocksStartLocked(waitReady, fmt.Errorf("stderr pipe: %w", err), nil)
		s.cMutex.Unlock()
		return
	}

	if err := cmd.Start(); err != nil {
		s.finishSystemSocksStartLocked(waitReady, fmt.Errorf("start ssh -D: %w", err), nil)
		s.cMutex.Unlock()
		return
	}
	go logSystemSocksStderr(stderrPipe)

	processDone := make(chan struct{})
	s.socksExt.cmd = cmd
	s.socksExt.processDone = processDone
	s.cMutex.Unlock()

	log.Infoln("[SSH] System SOCKS5 tunnel starting on 127.0.0.1:%d (PID: %d)", port, cmd.Process.Pid)

	go s.waitSystemSocksProcess(cmd, processDone)

	probeErr := waitSystemSocksReady(startupCtx, port)

	s.cMutex.Lock()
	defer s.cMutex.Unlock()
	if probeErr != nil {
		s.finishSystemSocksStartLocked(waitReady, fmt.Errorf("ssh -D socks %d failed: %w", port, probeErr), cmd)
		return
	}
	if s.closed {
		s.finishSystemSocksStartLocked(waitReady, fmt.Errorf("ssh adapter is closed"), cmd)
		return
	}
	if s.socksExt != nil && s.socksExt.cmd == cmd && s.socksExt.waitReady == waitReady {
		s.socksExt.lastErr = nil
		s.socks = s.newSystemSocksAdapter(port)
		close(waitReady)
		s.socksExt.waitReady = nil
	}
}

func (s *Ssh) finishSystemSocksStartLocked(waitReady chan struct{}, err error, cmd *exec.Cmd) {
	if s.socksExt == nil || s.socksExt.waitReady != waitReady {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return
	}
	s.socksExt.lastErr = err
	if cmd != nil && s.socksExt.cmd == cmd {
		_ = cmd.Process.Kill()
		s.socksExt.cmd = nil
		s.socksExt.processDone = nil
	}
	if !s.socksExt.pinned {
		s.socksExt.port = 0
	}
	s.socks = nil
	close(waitReady)
	s.socksExt.waitReady = nil
}

func logSystemSocksStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		log.Warnln("[SSH-SOCKS-STDERR] %s", scanner.Text())
	}
}

func (s *Ssh) waitSystemSocksProcess(cmd *exec.Cmd, processDone chan<- struct{}) {
	err := cmd.Wait()
	close(processDone)
	s.cMutex.Lock()
	defer s.cMutex.Unlock()
	if s.socksExt != nil && s.socksExt.cmd == cmd {
		if s.socksExt.waitReady != nil {
			s.socksExt.lastErr = fmt.Errorf("ssh -D exited before ready: %w", err)
			close(s.socksExt.waitReady)
			s.socksExt.waitReady = nil
		}
		s.socksExt.cmd = nil
		s.socksExt.processDone = nil
		s.socks = nil
		if !s.socksExt.pinned {
			s.socksExt.port = 0
		}
		if !s.closed {
			log.Errorln("[SSH] System SOCKS5 tunnel process (PID: %d) exited: %v", cmd.Process.Pid, err)
		}
	}
}

func buildSshDCommand(actualUser, hostAlias string, localPort int, extraFlags []string) *exec.Cmd {
	sshArgs := []string{
		"-T",
		"-N",
		"-D", net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)),
	}

	defaultOpts := []sshOption{
		{name: "BatchMode", value: "yes"},
		{name: "ConnectTimeout", value: "5"},
		{name: "ConnectionAttempts", value: "1"},
		{name: "ExitOnForwardFailure", value: "yes"},
		{name: "ServerAliveCountMax", value: "2"},
		{name: "ServerAliveInterval", value: "15"},
		{name: "TCPKeepAlive", value: "yes"},
		{name: "Tunnel", value: "no"},
	}
	managedOpts := []sshOption{
		{name: "ControlMaster", value: "no"},
		{name: "ControlPath", value: "none"},
		{name: "ControlPersist", value: "no"},
		{name: "ForkAfterAuthentication", value: "no"},
	}

	extraFlags = filterManagedSshFlags(extraFlags)
	userConfigured := make(map[string]bool)
	for i := 0; i < len(extraFlags); i++ {
		opt := ""
		switch {
		case extraFlags[i] == "-o" && i+1 < len(extraFlags):
			opt = extraFlags[i+1]
			i++
		case strings.HasPrefix(extraFlags[i], "-o"):
			opt = strings.TrimPrefix(extraFlags[i], "-o")
		}
		if key := sshOptionName(opt); key != "" {
			userConfigured[key] = true
		}
	}

	for _, opt := range defaultOpts {
		if !userConfigured[strings.ToLower(opt.name)] {
			sshArgs = append(sshArgs, "-o", opt.name+"="+opt.value)
		}
	}
	for _, opt := range managedOpts {
		sshArgs = append(sshArgs, "-o", opt.name+"="+opt.value)
	}

	sshArgs = append(sshArgs, extraFlags...)
	sshArgs = append(sshArgs, "--", hostAlias)

	if runtime.GOOS != "windows" {
		cur, _ := userCurrentFunc()
		if actualUser != "" && (cur == nil || normalizeLocalUserName(cur.Username) != normalizeLocalUserName(actualUser)) {
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

// dialSystemSocks delegates system SSH traffic to the ordinary SOCKS5 outbound data plane.
func (s *Ssh) dialSystemSocks(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	if s.socksExt == nil || !s.socksExt.inUse {
		return nil, fmt.Errorf("socks extension not initialized")
	}
	if err := s.setupSystemSocks(ctx); err != nil {
		return nil, err
	}

	s.cMutex.Lock()
	socks := s.socks
	s.cMutex.Unlock()
	if socks == nil {
		return nil, fmt.Errorf("system socks adapter not initialized")
	}

	return socks.DialContext(ctx, metadata)
}

func (s *Ssh) newSystemSocksAdapter(port int) *Socks5 {
	adapter, err := NewSocks5(Socks5Option{
		BasicOption: BasicOption{
			ProviderName: s.option.ProviderName,
		},
		Name:   s.Name(),
		Server: "127.0.0.1",
		Port:   port,
	})
	if err != nil {
		return nil
	}
	adapter.Base.tp = C.Ssh
	return adapter
}

func filterManagedSshFlags(flags []string) []string {
	filtered := make([]string, 0, len(flags))
	for i := 0; i < len(flags); i++ {
		flag := flags[i]
		switch {
		case isManagedSshShortFlag(flag):
			if (strings.Contains(flag, "S") || strings.Contains(flag, "O")) && i+1 < len(flags) {
				i++
			}
			continue
		case flag == "-o" && i+1 < len(flags):
			if isManagedSshOption(flags[i+1]) {
				i++
				continue
			}
			filtered = append(filtered, flag)
		case strings.HasPrefix(flag, "-o"):
			if isManagedSshOption(strings.TrimPrefix(flag, "-o")) {
				continue
			}
			filtered = append(filtered, flag)
		default:
			filtered = append(filtered, flag)
		}
	}
	return filtered
}

func isManagedSshOption(opt string) bool {
	switch sshOptionName(opt) {
	case "controlmaster", "controlpath", "controlpersist", "forkafterauthentication":
		return true
	default:
		return false
	}
}

func isManagedSshShortFlag(flag string) bool {
	if !strings.HasPrefix(flag, "-") || strings.HasPrefix(flag, "--") || strings.HasPrefix(flag, "-o") {
		return false
	}
	for _, r := range flag[1:] {
		switch r {
		case 'f', 'M', 'S', 'O':
			return true
		}
	}
	return false
}

func sshOptionName(opt string) string {
	opt = strings.TrimSpace(opt)
	if opt == "" {
		return ""
	}
	if idx := strings.Index(opt, "="); idx > 0 {
		return strings.ToLower(strings.TrimSpace(opt[:idx]))
	}
	fields := strings.Fields(opt)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(fields[0])
}

func waitSystemSocksReady(ctx context.Context, port int) error {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		if err := probeSystemSocks(ctx, port); err == nil {
			return nil
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return lastErr
		case <-ticker.C:
		}
	}
}

func probeSystemSocks(ctx context.Context, port int) (err error) {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(probeCtx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer conn.Close()

	deadline := time.Now().Add(500 * time.Millisecond)
	if probeDeadline, ok := probeCtx.Deadline(); ok && probeDeadline.Before(deadline) {
		deadline = probeDeadline
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	if probeCtx.Done() != nil {
		done := setupContextForProbe(probeCtx, conn)
		defer done(&err)
	}
	_, err = conn.Write([]byte{5, 1, 0})
	if err != nil {
		return err
	}
	buf := []byte{0, 0}
	if _, err = io.ReadFull(conn, buf); err != nil {
		return err
	}
	if buf[0] != 5 || buf[1] != 0 {
		return fmt.Errorf("unexpected socks5 probe response: %v", buf)
	}
	return nil
}

func setupContextForProbe(ctx context.Context, conn net.Conn) func(*error) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-done:
		}
	}()
	return func(err *error) {
		close(done)
		if *err != nil && ctx.Err() != nil {
			*err = ctx.Err()
		}
	}
}

// cleanupSocks 清理 SOCKS5 进程
func (s *Ssh) cleanupSocks() {
	if s.socksExt != nil {
		if s.socksExt.cmd != nil && s.socksExt.cmd.Process != nil {
			log.Infoln("[SSH] Cleaning up system SOCKS5 tunnel (PID: %d)", s.socksExt.cmd.Process.Pid)
			_ = s.socksExt.cmd.Process.Kill()
		}
		s.socksExt.cmd = nil
		s.socksExt.processDone = nil
		if !s.socksExt.pinned {
			s.socksExt.port = 0
		}
		s.socksExt.lastErr = fmt.Errorf("ssh adapter is closed")
		if s.socksExt.waitReady != nil {
			close(s.socksExt.waitReady)
			s.socksExt.waitReady = nil
		}
	}
	s.socks = nil
}
