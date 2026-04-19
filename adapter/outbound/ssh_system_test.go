package outbound

import (
	"fmt"
	"os"
	"os/user"
	"testing"
)

func TestResolveActualUser(t *testing.T) {
	oldUserCurrentFunc := userCurrentFunc
	oldActiveLoginUserFunc := activeLoginUserFunc
	t.Cleanup(func() {
		userCurrentFunc = oldUserCurrentFunc
		activeLoginUserFunc = oldActiveLoginUserFunc
		_ = os.Unsetenv("SUDO_USER")
	})

	t.Run("prefers explicit ssh user", func(t *testing.T) {
		userCurrentFunc = func() (*user.User, error) {
			return &user.User{Username: "root"}, nil
		}
		activeLoginUserFunc = func() (string, error) {
			return "desktop-user", nil
		}
		_ = os.Setenv("SUDO_USER", "sudo-user")

		s := &Ssh{option: &SshOption{SshUser: "config-user"}}
		if got := s.resolveActualUser(); got != "config-user" {
			t.Fatalf("resolveActualUser() = %q, want %q", got, "config-user")
		}
	})

	t.Run("prefers sudo user when explicit ssh user is empty", func(t *testing.T) {
		userCurrentFunc = func() (*user.User, error) {
			return &user.User{Username: "root"}, nil
		}
		activeLoginUserFunc = func() (string, error) {
			return "desktop-user", nil
		}
		_ = os.Setenv("SUDO_USER", "sudo-user")

		s := &Ssh{option: &SshOption{}}
		if got := s.resolveActualUser(); got != "sudo-user" {
			t.Fatalf("resolveActualUser() = %q, want %q", got, "sudo-user")
		}
	})

	t.Run("uses current user when it is not a service account", func(t *testing.T) {
		_ = os.Unsetenv("SUDO_USER")
		userCurrentFunc = func() (*user.User, error) {
			return &user.User{Username: "DESKTOP\\alice"}, nil
		}
		activeLoginUserFunc = func() (string, error) {
			return "desktop-user", nil
		}

		s := &Ssh{option: &SshOption{}}
		if got := s.resolveActualUser(); got != "alice" {
			t.Fatalf("resolveActualUser() = %q, want %q", got, "alice")
		}
	})

	t.Run("uses active login user when current user is root", func(t *testing.T) {
		_ = os.Unsetenv("SUDO_USER")
		userCurrentFunc = func() (*user.User, error) {
			return &user.User{Username: "root"}, nil
		}
		activeLoginUserFunc = func() (string, error) {
			return "desktop-user", nil
		}

		s := &Ssh{option: &SshOption{}}
		if got := s.resolveActualUser(); got != "desktop-user" {
			t.Fatalf("resolveActualUser() = %q, want %q", got, "desktop-user")
		}
	})

	t.Run("falls back to current user when active login user lookup fails", func(t *testing.T) {
		_ = os.Unsetenv("SUDO_USER")
		userCurrentFunc = func() (*user.User, error) {
			return &user.User{Username: "root"}, nil
		}
		activeLoginUserFunc = func() (string, error) {
			return "", os.ErrNotExist
		}

		s := &Ssh{option: &SshOption{}}
		if got := s.resolveActualUser(); got != "root" {
			t.Fatalf("resolveActualUser() = %q, want %q", got, "root")
		}
	})
}

func TestDetectActiveLoginUserWith(t *testing.T) {
	t.Run("macos returns console user", func(t *testing.T) {
		run := func(name string, args ...string) ([]byte, error) {
			if name != "scutil" || len(args) != 2 || args[0] != "show" || args[1] != "State:/Users/ConsoleUser" {
				t.Fatalf("unexpected command: %s %v", name, args)
			}
			return []byte("Name : alice\nUID : 501\n"), nil
		}

		got, err := detectActiveLoginUserWith("darwin", run)
		if err != nil {
			t.Fatalf("detectActiveLoginUserWith() error = %v", err)
		}
		if got != "alice" {
			t.Fatalf("detectActiveLoginUserWith() = %q, want %q", got, "alice")
		}
	})

	t.Run("macos returns command error", func(t *testing.T) {
		run := func(name string, args ...string) ([]byte, error) {
			return nil, fmt.Errorf("boom")
		}

		if _, err := detectActiveLoginUserWith("darwin", run); err == nil {
			t.Fatal("detectActiveLoginUserWith() error = nil, want non-nil")
		}
	})

	t.Run("linux returns active session user", func(t *testing.T) {
		run := func(name string, args ...string) ([]byte, error) {
			switch {
			case name == "loginctl" && len(args) == 1 && args[0] == "list-sessions":
				return []byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\n2 1000 alice seat0 111 user tty2 no -\n3 1001 bob seat0 222 user tty3 no -\n"), nil
			case name == "loginctl" && len(args) == 2 && args[0] == "show-session" && args[1] == "2":
				return []byte("Active=yes\nName=alice\nRemote=no\nState=active\n"), nil
			case name == "loginctl" && len(args) == 2 && args[0] == "show-session" && args[1] == "3":
				return []byte("Active=no\nName=bob\nRemote=no\nState=inactive\n"), nil
			default:
				t.Fatalf("unexpected command: %s %v", name, args)
				return nil, nil
			}
		}

		got, err := detectActiveLoginUserWith("linux", run)
		if err != nil {
			t.Fatalf("detectActiveLoginUserWith() error = %v", err)
		}
		if got != "alice" {
			t.Fatalf("detectActiveLoginUserWith() = %q, want %q", got, "alice")
		}
	})

	t.Run("linux returns error when no eligible session exists", func(t *testing.T) {
		run := func(name string, args ...string) ([]byte, error) {
			switch {
			case name == "loginctl" && len(args) == 1 && args[0] == "list-sessions":
				return []byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\n2 1000 alice seat0 111 user tty2 no -\n"), nil
			case name == "loginctl" && len(args) == 2 && args[0] == "show-session" && args[1] == "2":
				return []byte("Active=no\nName=alice\nRemote=no\nState=closing\n"), nil
			default:
				t.Fatalf("unexpected command: %s %v", name, args)
				return nil, nil
			}
		}

		if _, err := detectActiveLoginUserWith("linux", run); err == nil {
			t.Fatal("detectActiveLoginUserWith() error = nil, want non-nil")
		}
	})
}

func TestParseMacOSConsoleUser(t *testing.T) {
	got, err := parseMacOSConsoleUser([]byte("Name : alice\nUID : 501\n"))
	if err != nil {
		t.Fatalf("parseMacOSConsoleUser() error = %v", err)
	}
	if got != "alice" {
		t.Fatalf("parseMacOSConsoleUser() = %q, want %q", got, "alice")
	}

	if _, err := parseMacOSConsoleUser([]byte("Name : loginwindow\nUID : 0\n")); err == nil {
		t.Fatal("parseMacOSConsoleUser() error = nil, want non-nil")
	}
}

func TestParseLinuxLoginctlOutput(t *testing.T) {
	listOutput := []byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\n2 1000 alice seat0 111 user tty2 no -\n3 1001 bob seat0 222 user tty3 no -\n")
	sessionOutput := map[string][]byte{
		"2": []byte("Active=yes\nName=alice\nRemote=no\nState=active\n"),
		"3": []byte("Active=no\nName=bob\nRemote=no\nState=inactive\n"),
	}

	got, err := parseLinuxLoginctlOutput(listOutput, func(sessionID string) ([]byte, error) {
		output, ok := sessionOutput[sessionID]
		if !ok {
			return nil, fmt.Errorf("unknown session %s", sessionID)
		}
		return output, nil
	})
	if err != nil {
		t.Fatalf("parseLinuxLoginctlOutput() error = %v", err)
	}
	if got != "alice" {
		t.Fatalf("parseLinuxLoginctlOutput() = %q, want %q", got, "alice")
	}
}

func TestParseLinuxLoginctlOutputNoEligibleSession(t *testing.T) {
	listOutput := []byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\n2 1000 alice seat0 111 user tty2 no -\n")

	if _, err := parseLinuxLoginctlOutput(listOutput, func(sessionID string) ([]byte, error) {
		return []byte("Active=no\nName=alice\nRemote=no\nState=closing\n"), nil
	}); err == nil {
		t.Fatal("parseLinuxLoginctlOutput() error = nil, want non-nil")
	}
}

func TestParseLinuxLoginctlOutputAcceptsConsoleStyleSessionID(t *testing.T) {
	listOutput := []byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\nc1 1000 alice seat0 111 user tty2 no -\n")

	got, err := parseLinuxLoginctlOutput(listOutput, func(sessionID string) ([]byte, error) {
		if sessionID != "c1" {
			return nil, fmt.Errorf("unexpected session %s", sessionID)
		}
		return []byte("Active=yes\nName=alice\nRemote=no\nClass=user\nState=active\n"), nil
	})
	if err != nil {
		t.Fatalf("parseLinuxLoginctlOutput() error = %v", err)
	}
	if got != "alice" {
		t.Fatalf("parseLinuxLoginctlOutput() = %q, want %q", got, "alice")
	}
}

func TestParseLinuxLoginctlOutputSkipsGreeterSession(t *testing.T) {
	listOutput := []byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\n2 120 gdm seat0 111 greeter tty1 no -\n3 1000 alice seat0 222 user tty2 no -\n")

	got, err := parseLinuxLoginctlOutput(listOutput, func(sessionID string) ([]byte, error) {
		switch sessionID {
		case "2":
			return []byte("Active=yes\nName=gdm\nRemote=no\nClass=greeter\nState=active\n"), nil
		case "3":
			return []byte("Active=yes\nName=alice\nRemote=no\nClass=user\nState=active\n"), nil
		default:
			return nil, fmt.Errorf("unexpected session %s", sessionID)
		}
	})
	if err != nil {
		t.Fatalf("parseLinuxLoginctlOutput() error = %v", err)
	}
	if got != "alice" {
		t.Fatalf("parseLinuxLoginctlOutput() = %q, want %q", got, "alice")
	}
}

func TestParseLinuxSessionPropertiesRejectsGreeterClass(t *testing.T) {
	if _, ok := parseLinuxSessionProperties([]byte("Active=yes\nName=gdm\nRemote=no\nClass=greeter\nState=active\n")); ok {
		t.Fatal("parseLinuxSessionProperties() ok = true, want false")
	}
}

func TestParseLinuxSessionPropertiesAcceptsUserClass(t *testing.T) {
	got, ok := parseLinuxSessionProperties([]byte("Active=yes\nName=alice\nRemote=no\nClass=user\nState=active\n"))
	if !ok {
		t.Fatal("parseLinuxSessionProperties() ok = false, want true")
	}
	if got != "alice" {
		t.Fatalf("parseLinuxSessionProperties() = %q, want %q", got, "alice")
	}
}

func TestDetectActiveLoginUserWithLinuxAcceptsConsoleStyleSessionID(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "loginctl" && len(args) == 1 && args[0] == "list-sessions":
			return []byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\nc1 1000 alice seat0 111 user tty2 no -\n"), nil
		case name == "loginctl" && len(args) == 2 && args[0] == "show-session" && args[1] == "c1":
			return []byte("Active=yes\nName=alice\nRemote=no\nClass=user\nState=active\n"), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s %v", name, args)
		}
	}

	got, err := detectActiveLoginUserWith("linux", run)
	if err != nil {
		t.Fatalf("detectActiveLoginUserWith() error = %v", err)
	}
	if got != "alice" {
		t.Fatalf("detectActiveLoginUserWith() = %q, want %q", got, "alice")
	}
}

func TestDetectActiveLoginUserWithLinuxSkipsGreeterSession(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "loginctl" && len(args) == 1 && args[0] == "list-sessions":
			return []byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\n2 120 gdm seat0 111 greeter tty1 no -\n3 1000 alice seat0 222 user tty2 no -\n"), nil
		case name == "loginctl" && len(args) == 2 && args[0] == "show-session" && args[1] == "2":
			return []byte("Active=yes\nName=gdm\nRemote=no\nClass=greeter\nState=active\n"), nil
		case name == "loginctl" && len(args) == 2 && args[0] == "show-session" && args[1] == "3":
			return []byte("Active=yes\nName=alice\nRemote=no\nClass=user\nState=active\n"), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s %v", name, args)
		}
	}

	got, err := detectActiveLoginUserWith("linux", run)
	if err != nil {
		t.Fatalf("detectActiveLoginUserWith() error = %v", err)
	}
	if got != "alice" {
		t.Fatalf("detectActiveLoginUserWith() = %q, want %q", got, "alice")
	}
}

func TestParseLinuxSessionIDsAcceptsConsoleStyleSessionID(t *testing.T) {
	got := parseLinuxSessionIDs([]byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\nc1 1000 alice seat0 111 user tty2 no -\n"))
	if len(got) != 1 || got[0] != "c1" {
		t.Fatalf("parseLinuxSessionIDs() = %v, want [c1]", got)
	}
}

func TestParseLinuxSessionIDsRejectsPunctuationOnlyTokens(t *testing.T) {
	got := parseLinuxSessionIDs([]byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\n- 1000 alice seat0 111 user tty2 no -\nc1 1000 alice seat0 111 user tty2 no -\n"))
	if len(got) != 1 || got[0] != "c1" {
		t.Fatalf("parseLinuxSessionIDs() = %v, want [c1]", got)
	}
}

func TestParseLinuxSessionPropertiesRejectsGreeterAccountWithoutClass(t *testing.T) {
	if _, ok := parseLinuxSessionProperties([]byte("Active=yes\nName=gdm\nRemote=no\nState=active\n")); ok {
		t.Fatal("parseLinuxSessionProperties() ok = true, want false")
	}
}

func TestParseLinuxSessionPropertiesAcceptsUserWithoutClassWhenNameLooksReal(t *testing.T) {
	got, ok := parseLinuxSessionProperties([]byte("Active=yes\nName=alice\nRemote=no\nState=active\n"))
	if !ok {
		t.Fatal("parseLinuxSessionProperties() ok = false, want true")
	}
	if got != "alice" {
		t.Fatalf("parseLinuxSessionProperties() = %q, want %q", got, "alice")
	}
}

func TestDetectActiveLoginUserWithLinuxSkipsGreeterAccountWithoutClass(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "loginctl" && len(args) == 1 && args[0] == "list-sessions":
			return []byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\n2 120 gdm seat0 111 greeter tty1 no -\n3 1000 alice seat0 222 user tty2 no -\n"), nil
		case name == "loginctl" && len(args) == 2 && args[0] == "show-session" && args[1] == "2":
			return []byte("Active=yes\nName=gdm\nRemote=no\nState=active\n"), nil
		case name == "loginctl" && len(args) == 2 && args[0] == "show-session" && args[1] == "3":
			return []byte("Active=yes\nName=alice\nRemote=no\nState=active\n"), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s %v", name, args)
		}
	}

	got, err := detectActiveLoginUserWith("linux", run)
	if err != nil {
		t.Fatalf("detectActiveLoginUserWith() error = %v", err)
	}
	if got != "alice" {
		t.Fatalf("detectActiveLoginUserWith() = %q, want %q", got, "alice")
	}
}

func TestParseLinuxLoginctlOutputSkipsGreeterAccountWithoutClass(t *testing.T) {
	listOutput := []byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\n2 120 gdm seat0 111 greeter tty1 no -\n3 1000 alice seat0 222 user tty2 no -\n")

	got, err := parseLinuxLoginctlOutput(listOutput, func(sessionID string) ([]byte, error) {
		switch sessionID {
		case "2":
			return []byte("Active=yes\nName=gdm\nRemote=no\nState=active\n"), nil
		case "3":
			return []byte("Active=yes\nName=alice\nRemote=no\nState=active\n"), nil
		default:
			return nil, fmt.Errorf("unexpected session %s", sessionID)
		}
	})
	if err != nil {
		t.Fatalf("parseLinuxLoginctlOutput() error = %v", err)
	}
	if got != "alice" {
		t.Fatalf("parseLinuxLoginctlOutput() = %q, want %q", got, "alice")
	}
}

func TestParseLinuxSessionIDsAcceptsMixedAlphaNumericSessionID(t *testing.T) {
	got := parseLinuxSessionIDs([]byte("SESSION UID USER SEAT LEADER CLASS TTY IDLE SINCE\nseat01 1000 alice seat0 111 user tty2 no -\n"))
	if len(got) != 1 || got[0] != "seat01" {
		t.Fatalf("parseLinuxSessionIDs() = %v, want [seat01]", got)
	}
}

func TestParseLinuxSessionPropertiesRejectsDisplayManagerAccounts(t *testing.T) {
	for _, name := range []string{"gdm", "sddm", "lightdm", "greetd"} {
		if _, ok := parseLinuxSessionProperties([]byte("Active=yes\nName=" + name + "\nRemote=no\nState=active\n")); ok {
			t.Fatalf("parseLinuxSessionProperties(%q) ok = true, want false", name)
		}
	}
}

func TestParseLinuxSessionPropertiesAcceptsUserClassEvenForGreeterNamedLookingUser(t *testing.T) {
	got, ok := parseLinuxSessionProperties([]byte("Active=yes\nName=alice\nRemote=no\nClass=user\nState=active\n"))
	if !ok {
		t.Fatal("parseLinuxSessionProperties() ok = false, want true")
	}
	if got != "alice" {
		t.Fatalf("parseLinuxSessionProperties() = %q, want %q", got, "alice")
	}
}

func TestParseLinuxSessionIDsRejectsEmptyOutput(t *testing.T) {
	got := parseLinuxSessionIDs(nil)
	if len(got) != 0 {
		t.Fatalf("parseLinuxSessionIDs() = %v, want []", got)
	}
}
