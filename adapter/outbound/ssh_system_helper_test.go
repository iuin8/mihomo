package outbound

import (
	"context"
	"os/user"
	"runtime"
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

func TestBuildSshGCommandTreatsNormalizedCurrentUserAsSameUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not use sudo for system ssh helpers")
	}

	oldUserCurrentFunc := userCurrentFunc
	t.Cleanup(func() {
		userCurrentFunc = oldUserCurrentFunc
	})
	userCurrentFunc = func() (*user.User, error) {
		return &user.User{Username: "DOMAIN\\local-match-user"}, nil
	}

	cmd := buildSshGCommand(context.Background(), "local-match-user", "host-alias")
	if cmd.Args[0] != "ssh" {
		t.Fatalf("buildSshGCommand() launcher = %q, want %q", cmd.Args[0], "ssh")
	}
}

func TestBuildSshCommandTreatsNormalizedCurrentUserAsSameUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows does not use sudo for system ssh helpers")
	}

	oldUserCurrentFunc := userCurrentFunc
	t.Cleanup(func() {
		userCurrentFunc = oldUserCurrentFunc
	})
	userCurrentFunc = func() (*user.User, error) {
		return &user.User{Username: "DOMAIN\\local-match-user"}, nil
	}

	cmd := buildSshCommand("local-match-user", []string{"-N", "host-alias"})
	if cmd.Args[0] != "ssh" {
		t.Fatalf("buildSshCommand() launcher = %q, want %q", cmd.Args[0], "ssh")
	}
}
