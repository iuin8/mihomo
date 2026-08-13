package outbound

import (
	"context"
	"os"
	"os/user"
	"runtime"
	"strings"
	"testing"
)

func TestParseLinuxSessionIDsSkipsLoginctlFooter(t *testing.T) {
	got := parseLinuxSessionIDs([]byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\n2 1000 alice seat0 111 user tty2 no -\n\n2 sessions listed.\n3 sessions listed.\n"))
	if len(got) != 1 || got[0] != "2" {
		t.Fatalf("parseLinuxSessionIDs() = %v, want [2]", got)
	}
}

func TestBuildEnvCommandTreatsNormalizedCurrentUserAsSameUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not use shell or sudo for env capture")
	}

	oldUserCurrentFunc := userCurrentFunc
	t.Cleanup(func() {
		userCurrentFunc = oldUserCurrentFunc
	})
	userCurrentFunc = func() (*user.User, error) {
		return &user.User{Username: "DOMAIN\\local-match-user"}, nil
	}

	cmd := buildEnvCommand(context.Background(), "local-match-user")
	if cmd == nil {
		t.Fatal("buildEnvCommand() = nil, want command")
	}
	if cmd.Args[0] == "sudo" {
		t.Fatalf("buildEnvCommand() launcher = %q, want non-sudo shell", cmd.Args[0])
	}
}

func TestBuildSshDCommandUsesDeterministicForwardingOptions(t *testing.T) {
	cmd := buildSshDCommand("", "host-alias", 1080, nil, "")
	args := strings.Join(cmd.Args, " ")

	for _, want := range []string{
		"-- host-alias",
		"-D 127.0.0.1:1080",
		"BatchMode=yes",
		"ExitOnForwardFailure=yes",
		"ConnectTimeout=5",
		"ConnectionAttempts=1",
		"ControlMaster=no",
		"ControlPath=none",
		"ControlPersist=no",
		"ForkAfterAuthentication=no",
		"ServerAliveInterval=15",
		"ServerAliveCountMax=2",
		"TCPKeepAlive=yes",
		"Tunnel=no",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("buildSshDCommand args = %q, want to contain %q", args, want)
		}
	}
}

func TestBuildSshDCommandLetsUserOverrideDefaultOptions(t *testing.T) {
	cmd := buildSshDCommand("", "host-alias", 1080, []string{"-o", "ConnectTimeout=30"}, "")
	args := strings.Join(cmd.Args, " ")

	if strings.Contains(args, "ConnectTimeout=5") {
		t.Fatalf("buildSshDCommand args = %q, should not contain default ConnectTimeout", args)
	}
	if !strings.Contains(args, "ConnectTimeout=30") {
		t.Fatalf("buildSshDCommand args = %q, want user ConnectTimeout", args)
	}
}

func TestBuildSshDCommandDoesNotLetUserOverrideManagedLifecycleOptions(t *testing.T) {
	cmd := buildSshDCommand("", "host-alias", 1080, []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=~/.ssh/control:%C",
		"-o", "ControlPersist=10m",
		"-o", "ForkAfterAuthentication=yes",
	}, "")
	args := strings.Join(cmd.Args, " ")

	for _, want := range []string{"ControlMaster=no", "ControlPath=none", "ControlPersist=no", "ForkAfterAuthentication=no"} {
		if !strings.Contains(args, want) {
			t.Fatalf("buildSshDCommand args = %q, want managed option %q", args, want)
		}
	}
	for _, unwanted := range []string{"ControlMaster=auto", "ControlPath=~/.ssh/control:%C", "ControlPersist=10m", "ForkAfterAuthentication=yes"} {
		if strings.Contains(args, unwanted) {
			t.Fatalf("buildSshDCommand args = %q, should not contain user lifecycle option %q", args, unwanted)
		}
	}
}

func TestBuildSshDCommandDropsManagedLifecycleFlags(t *testing.T) {
	cmd := buildSshDCommand("", "host-alias", 1080, []string{"-f", "-M", "-MN", "-fN", "-MS", "control-path", "-MO", "check", "-S", "control-path", "-O", "check", "-v"}, "")
	args := strings.Join(cmd.Args, " ")

	for _, unwanted := range []string{" -f ", " -M ", " -MN ", " -fN ", " -S ", "control-path", " -O ", " check "} {
		if strings.Contains(" "+args+" ", unwanted) {
			t.Fatalf("buildSshDCommand args = %q, should not contain managed lifecycle flag %q", args, unwanted)
		}
	}
	if !strings.Contains(args, " -v ") {
		t.Fatalf("buildSshDCommand args = %q, want unrelated flag -v", args)
	}
}

func TestBuildSshDCommandRecognizesOpenSSHSpaceSeparatedOptions(t *testing.T) {
	cmd := buildSshDCommand("", "host-alias", 1080, []string{"-o", "StrictHostKeyChecking yes", "-oConnectTimeout=30"}, "")
	args := strings.Join(cmd.Args, " ")

	for _, unwanted := range []string{"StrictHostKeyChecking=accept-new", "ConnectTimeout=5"} {
		if strings.Contains(args, unwanted) {
			t.Fatalf("buildSshDCommand args = %q, should not contain default %q", args, unwanted)
		}
	}
}

func TestBuildSshDCommandSeparatesDestinationFromOptions(t *testing.T) {
	cmd := buildSshDCommand("", "-looks-like-option", 1080, nil, "")
	got := cmd.Args[len(cmd.Args)-2:]
	want := []string{"--", "-looks-like-option"}
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("buildSshDCommand destination args = %v, want %v", got, want)
	}
}

func TestBuildSshDCommandAddsFWhenSshConfigPathGiven(t *testing.T) {
	// create a temp file so the existence check passes
	f, err := os.CreateTemp("", "ssh_config_test_*.conf")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	cmd := buildSshDCommand("", "host-alias", 1080, nil, path)
	args := strings.Join(cmd.Args, " ")

	if !strings.Contains(args, "-F "+path) {
		t.Fatalf("buildSshDCommand args = %q, want -F %s", args, path)
	}
	// -F must come before -D
	fIdx := strings.Index(args, "-F")
	dIdx := strings.Index(args, "-D")
	if fIdx < 0 || dIdx < 0 || fIdx >= dIdx {
		t.Fatalf("buildSshDCommand args = %q, want -F before -D", args)
	}
}

func TestBuildSshDCommandOmitsFWhenSshConfigFileMissing(t *testing.T) {
	cmd := buildSshDCommand("", "host-alias", 1080, nil, "/nonexistent/path/ssh.conf")
	args := strings.Join(cmd.Args, " ")

	if strings.Contains(args, "-F") {
		t.Fatalf("buildSshDCommand args = %q, should not contain -F when file is missing", args)
	}
}

func TestBuildSshDCommandOmitsFWhenSshConfigPathEmpty(t *testing.T) {
	cmd := buildSshDCommand("", "host-alias", 1080, nil, "")
	args := strings.Join(cmd.Args, " ")

	if strings.Contains(args, "-F") {
		t.Fatalf("buildSshDCommand args = %q, should not contain -F", args)
	}
}
