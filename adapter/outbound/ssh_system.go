package outbound

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
	"strings"
)

var (
	userCurrentFunc     = user.Current
	activeLoginUserFunc = detectActiveLoginUser
)

// posixUserNameRe 限制用户名以字母或下划线起首、仅含 [a-z0-9_-]，并且最长 32 字符。
// 起首不允许 '-' 是防止用户名被 sudo / ssh 当作 flag（例如 "--" 或 "-Eroot"）。
// 长度上限对齐 POSIX/Linux LOGIN_NAME_MAX 常见值。
var posixUserNameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// isPOSIXUserName 校验用户名是否安全可作为 sudo/ssh 的 `-u <user>` 实参。
// Windows 不调用此函数，因为 Windows 不走 sudo 切换用户。
func isPOSIXUserName(name string) bool {
	return posixUserNameRe.MatchString(name)
}

func applyEnv(cmd *exec.Cmd, capturedEnv []string) {
	if capturedEnv != nil {
		cmd.Env = capturedEnv
		return
	}
	cmd.Env = os.Environ()
}

func (s *Ssh) resolveActualUser() string {
	return s.resolveActualUserForOS(runtime.GOOS)
}

func (s *Ssh) resolveActualUserForOS(goos string) string {
	if s.option.SshUser != "" {
		return explicitSshUserName(s.option.SshUser)
	}
	if u := realUserName(os.Getenv("SUDO_USER")); u != "" && !isServiceAccount(u) {
		return u
	}
	if cur, _ := userCurrentFunc(); cur != nil {
		name := realUserName(cur.Username)
		if name != "" && !isServiceAccount(name) {
			return name
		}
		if activeUser, err := activeLoginUserFunc(); err == nil && activeUser != "" {
			if name := realUserName(activeUser); name != "" && !isServiceAccount(name) {
				return name
			}
		}
		if goos == "linux" {
			return name
		}
		return ""
	}
	if activeUser, err := activeLoginUserFunc(); err == nil && activeUser != "" {
		if name := realUserName(activeUser); name != "" && !isServiceAccount(name) {
			return name
		}
	}
	return ""
}

func requireSystemSshUser(actualUser string) error {
	return requireSystemSshUserForOS(actualUser, runtime.GOOS)
}

func requireSystemSshUserForOS(actualUser, goos string) error {
	if actualUser == "" {
		return fmt.Errorf("system ssh requires ssh-user when the process user is a service account and no active login user can be detected")
	}
	if goos == "windows" {
		cur, _ := userCurrentFunc()
		if cur != nil && sameWindowsUser(cur.Username, actualUser) {
			return nil
		}
		return fmt.Errorf("system ssh cannot switch users on windows; run mihomo as %s so OpenSSH reads that user's config", actualUser)
	}
	// 非 Windows 必须满足 POSIX 安全用户名，防止被 sudo / ssh 当作 flag 注入
	if !isPOSIXUserName(actualUser) {
		return fmt.Errorf("system ssh refused unsafe user name %q (must match %s)", actualUser, posixUserNameRe.String())
	}
	return nil
}

func explicitSshUserName(name string) string {
	name = strings.TrimSpace(name)
	if isServiceAccount(name) {
		return ""
	}
	// 非 Windows 平台需立即过滤掉不可作 sudo/ssh `-u` 实参的用户名，避免后续路径以为已验证。
	// Windows 用户名允许大小写与 '\'，不在此检查范围内；其单独依赖 sameWindowsUser 判定。
	if runtime.GOOS != "windows" && !isPOSIXUserName(name) {
		return ""
	}
	return name
}

func realUserName(name string) string {
	name = normalizeLocalUserName(name)
	if strings.EqualFold(name, "system") {
		return ""
	}
	return name
}

func sameWindowsUser(currentUser, actualUser string) bool {
	currentUser = strings.TrimSpace(currentUser)
	actualUser = strings.TrimSpace(actualUser)
	if strings.Contains(actualUser, "\\") {
		return strings.EqualFold(currentUser, actualUser)
	}
	return strings.EqualFold(normalizeLocalUserName(currentUser), actualUser)
}

func normalizeLocalUserName(name string) string {
	if idx := strings.LastIndex(name, "\\"); idx != -1 {
		name = name[idx+1:]
	}
	return strings.TrimSpace(name)
}

func isServiceAccount(name string) bool {
	switch strings.ToLower(normalizeLocalUserName(name)) {
	case "", "root", "system":
		return true
	default:
		return false
	}
}

func detectActiveLoginUser() (string, error) {
	return detectActiveLoginUserWith(runtime.GOOS, func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	})
}

func detectActiveLoginUserWith(goos string, run func(name string, args ...string) ([]byte, error)) (string, error) {
	switch goos {
	case "darwin":
		output, statErr := run("/usr/bin/stat", "-f", "%Su", "/dev/console")
		if name := parseMacOSConsoleOwner(output); name != "" {
			return name, nil
		}
		output, err := run("/usr/sbin/scutil", "show", "State:/Users/ConsoleUser")
		if err != nil {
			if statErr != nil {
				return "", fmt.Errorf("read macos console owner: %w; read macos console user: %w", statErr, err)
			}
			return "", fmt.Errorf("read macos console user: %w", err)
		}
		return parseMacOSConsoleUser(output)
	case "linux":
		output, err := run("loginctl", "list-sessions")
		if err != nil {
			return "", fmt.Errorf("list linux sessions: %w", err)
		}
		return parseLinuxLoginctlOutput(output, func(sessionID string) ([]byte, error) {
			sessionOutput, err := run("loginctl", "show-session", sessionID)
			if err != nil {
				return nil, fmt.Errorf("show linux session %s: %w", sessionID, err)
			}
			return sessionOutput, nil
		})
	case "windows":
		return "", fmt.Errorf("active login user detection is not supported on windows")
	default:
		return "", fmt.Errorf("active login user detection is not implemented for %s", goos)
	}
}

func parseMacOSConsoleOwner(output []byte) string {
	name := realUserName(string(output))
	if strings.EqualFold(name, "loginwindow") || isServiceAccount(name) {
		return ""
	}
	return name
}

func parseMacOSConsoleUser(output []byte) (string, error) {
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Name") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "Name" {
			continue
		}
		name := normalizeLocalUserName(value)
		if name == "" {
			return "", fmt.Errorf("macos console user is empty")
		}
		if strings.EqualFold(name, "loginwindow") || isServiceAccount(name) {
			return "", fmt.Errorf("macos console user %q is not a real desktop user", name)
		}
		return name, nil
	}
	return "", fmt.Errorf("macos console user not found")
}

func parseLinuxLoginctlOutput(listOutput []byte, sessionLookup func(sessionID string) ([]byte, error)) (string, error) {
	if sessionLookup == nil {
		return "", fmt.Errorf("linux session lookup is nil")
	}

	sessionIDs := parseLinuxSessionIDs(listOutput)
	if len(sessionIDs) == 0 {
		return "", fmt.Errorf("no linux sessions found")
	}

	var lastErr error
	for _, sessionID := range sessionIDs {
		output, err := sessionLookup(sessionID)
		if err != nil {
			lastErr = err
			continue
		}
		if name, ok := parseLinuxSessionProperties(output); ok {
			return name, nil
		}
		lastErr = fmt.Errorf("no eligible active local linux session found")
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no eligible active local linux session found")
}

func parseLinuxSessionIDs(output []byte) []string {
	lines := strings.Split(string(output), "\n")
	sessionIDs := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		sessionID := strings.TrimSpace(fields[0])
		if sessionID == "" || strings.EqualFold(sessionID, "SESSION") {
			continue
		}
		if !isLikelyLinuxSessionID(sessionID) || !isLikelyLinuxSessionUID(fields[1]) {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		sessionIDs = append(sessionIDs, sessionID)
	}
	return sessionIDs
}

func isLikelyLinuxSessionID(sessionID string) bool {
	for _, r := range sessionID {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return sessionID != ""
}

func isLikelyLinuxSessionUID(uid string) bool {
	if uid == "" {
		return false
	}
	for _, r := range uid {
		if r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func parseLinuxSessionProperties(output []byte) (string, bool) {
	values := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		normalizedValue := strings.ToLower(strings.TrimSpace(value))
		values[normalizedKey] = normalizedValue
		if strings.EqualFold(strings.TrimSpace(key), "Name") {
			values["name"] = normalizeLocalUserName(value)
		}
	}

	name := values["name"]
	if name == "" || isServiceAccount(name) || isDisplayManagerAccount(name) {
		return "", false
	}
	if values["remote"] != "no" {
		return "", false
	}
	if sessionClass := values["class"]; sessionClass != "" && sessionClass != "user" {
		return "", false
	}

	active := values["active"]
	state := values["state"]
	if active == "yes" {
		if state == "" || state == "active" {
			return name, true
		}
		return "", false
	}
	if active != "" {
		return "", false
	}
	if state == "active" {
		return name, true
	}
	return "", false
}

func isDisplayManagerAccount(name string) bool {
	switch strings.ToLower(normalizeLocalUserName(name)) {
	case "gdm", "sddm", "lightdm", "greetd":
		return true
	default:
		return false
	}
}
