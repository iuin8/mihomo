package outbound

import (
	"reflect"
	"strings"
	"testing"
)

// ─── isPOSIXUserName ─────────────────────────────────────────────────────────

func TestIsPOSIXUserName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"valid lowercase", "alice", true},
		{"valid with underscore", "_systemd", true},
		{"valid with digit and dash", "user-1", true},
		{"valid 32 chars", strings.Repeat("a", 32), true},

		{"empty", "", false},
		{"too long 33 chars", strings.Repeat("a", 33), false},
		{"leading dash", "-alice", false},
		{"double dash", "--", false},
		{"sudo flag style", "-Eroot", false},
		{"sudo short flag", "-s", false},
		{"semicolon injection", "alice;rm", false},
		{"dollar paren", "$(whoami)", false},
		{"backtick", "`id`", false},
		{"space", "alice bob", false},
		{"slash", "domain\\alice", false},
		{"uppercase", "Alice", false},
		{"unicode", "用户", false},
		{"control char", "alice\nbob", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPOSIXUserName(tc.in); got != tc.want {
				t.Fatalf("isPOSIXUserName(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ─── explicitSshUserName: 非 Windows 平台必须挡掉不安全名字 ─────────────────

func TestExplicitSshUserNameRejectsUnsafeNames(t *testing.T) {
	// 仅 Linux/macOS 走 sudo 切换用户，所以这条防线在这两个平台必须生效。
	// Windows 上 explicitSshUserName 保留原值，由 sameWindowsUser 判定。
	if got := explicitSshUserName("--"); got != "" {
		t.Fatalf("explicitSshUserName(--) = %q, want empty", got)
	}
	if got := explicitSshUserName("-Eroot"); got != "" {
		t.Fatalf("explicitSshUserName(-Eroot) = %q, want empty", got)
	}
	if got := explicitSshUserName("alice;rm -rf /"); got != "" {
		t.Fatalf("explicitSshUserName(alice;rm) = %q, want empty", got)
	}
	if got := explicitSshUserName("alice"); got != "alice" {
		t.Fatalf("explicitSshUserName(alice) = %q, want alice", got)
	}
}

// ─── requireSystemSshUserForOS: 非 Windows 兜底校验 ───────────────────────

func TestRequireSystemSshUserRejectsUnsafeNamesOnUnix(t *testing.T) {
	unsafe := []string{"--", "-Eroot", "alice;rm", "$(id)", "Alice"}
	for _, name := range unsafe {
		for _, goos := range []string{"darwin", "linux"} {
			if err := requireSystemSshUserForOS(name, goos); err == nil {
				t.Fatalf("requireSystemSshUserForOS(%q, %q) = nil, want error", name, goos)
			}
		}
	}
}

func TestRequireSystemSshUserAcceptsSafeName(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		if err := requireSystemSshUserForOS("alice", goos); err != nil {
			t.Fatalf("requireSystemSshUserForOS(alice, %q) = %v, want nil", goos, err)
		}
	}
}

// ─── filterManagedSshFlags: ssh-flags 过滤 ────────────────────────────────

func TestFilterManagedSshFlagsBansNewOptions(t *testing.T) {
	// 历史上只过滤 ControlMaster/ControlPath/ControlPersist/ForkAfterAuthentication；
	// 现在新增 ProxyCommand/LocalCommand/PermitLocalCommand/LocalForward/
	// RemoteForward/DynamicForward 以堵掉 RCE 与监听偏离。
	bannedOptions := []string{
		"ProxyCommand=evil",
		"LocalCommand=evil",
		"PermitLocalCommand=yes",
		"LocalForward=1080 host:22",
		"RemoteForward=2222 host:22",
		"DynamicForward=1080",
		// 历史名单回归测试，确保未被破坏
		"ControlMaster=yes",
		"ControlPath=/tmp/c",
		"ControlPersist=10m",
		"ForkAfterAuthentication=yes",
	}
	for _, opt := range bannedOptions {
		t.Run("-o_"+opt, func(t *testing.T) {
			got := filterManagedSshFlags([]string{"-o", opt})
			if len(got) != 0 {
				t.Fatalf("filterManagedSshFlags(-o %q) = %v, want []", opt, got)
			}
		})
		t.Run("-o"+opt, func(t *testing.T) {
			got := filterManagedSshFlags([]string{"-o" + opt})
			if len(got) != 0 {
				t.Fatalf("filterManagedSshFlags(-o%q) = %v, want []", opt, got)
			}
		})
	}
}

func TestFilterManagedSshFlagsCaseInsensitive(t *testing.T) {
	cases := [][]string{
		{"-o", "proxycommand=evil"},
		{"-o", "PROXYCOMMAND=evil"},
		{"-o", "ProxyCommand evil"},   // 空格分隔
		{"-oPROXYCOMMAND=evil"},        // 无空格连写
	}
	for _, c := range cases {
		if got := filterManagedSshFlags(c); len(got) != 0 {
			t.Fatalf("filterManagedSshFlags(%v) = %v, want []", c, got)
		}
	}
}

func TestFilterManagedSshFlagsDropsBareDoubleDash(t *testing.T) {
	// 用户提供的 "--" 应被丢弃：buildSshDCommand 末尾会强制追加 `--`，
	// 用户的 `--` 既无用，又会让"-- 之后视作 positional"的语义提前生效，
	// 从而让 ssh 跳过对 banned 短标志的解析（语义上的过滤绕过）。
	got := filterManagedSshFlags([]string{"--", "-f", "-v"})
	want := []string{"-v"} // -- 丢, -f 也丢（managed 短标志）
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterManagedSshFlags(--/-f/-v) = %v, want %v", got, want)
	}
}

func TestFilterManagedSshFlagsRejectsControlChars(t *testing.T) {
	// 防 ssh_config 解析侧的换行注入：-o "BatchMode=yes\nProxyCommand=evil"。
	// 含控制字符的 token 单独丢弃；不会株连同行其它安全 token。
	dropAll := [][]string{
		{"-o", "BatchMode=yes\nProxyCommand=evil"},
		{"-oBatchMode=yes\nProxyCommand=evil"},
		{"arg\x00null"},
		{"-o", "Key=val\x7f"}, // DEL 算控制字符
	}
	for _, c := range dropAll {
		if got := filterManagedSshFlags(c); len(got) != 0 {
			t.Fatalf("filterManagedSshFlags(%v) = %v, want []", c, got)
		}
	}

	// 安全 token 与含控制字符 token 混在一起时，只丢后者。
	got := filterManagedSshFlags([]string{"-v", "arg\rwith\rCR"})
	want := []string{"-v"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterManagedSshFlags(-v + CR arg) = %v, want %v", got, want)
	}
}

func TestFilterManagedSshFlagsKeepsSafeOptions(t *testing.T) {
	in := []string{
		"-v",
		"-J", "jump-host",
		"-o", "BatchMode=yes",
		"-oConnectTimeout=5",
		"-i", "/home/alice/.ssh/id_ed25519",
	}
	got := filterManagedSshFlags(in)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("filterManagedSshFlags(safe) = %v, want %v", got, in)
	}
}

func TestFilterManagedSshFlagsDropsCombinedShortBannedFlags(t *testing.T) {
	// 组合短标志：-fN、-MN、-MS path、-MO command 等
	got := filterManagedSshFlags([]string{"-fN", "-MN", "-MS", "/tmp/sock", "-MO", "stop"})
	if len(got) != 0 {
		t.Fatalf("filterManagedSshFlags(combined banned) = %v, want []", got)
	}
}

// ─── isManagedSshOption: 单独覆盖新名单 ───────────────────────────────────

func TestIsManagedSshOptionExpandedBanList(t *testing.T) {
	cases := map[string]bool{
		"controlmaster=yes":         true,
		"controlpath=/tmp/c":        true,
		"controlpersist=10m":        true,
		"forkafterauthentication=yes": true,
		"proxycommand=evil":         true,
		"localcommand=evil":         true,
		"permitlocalcommand=yes":    true,
		"localforward=1080 h:22":    true,
		"remoteforward=2222 h:22":   true,
		"dynamicforward=1080":       true,

		"batchmode=yes":     false,
		"connecttimeout=5":  false,
		"serveralive=15":    false,
	}
	for opt, want := range cases {
		if got := isManagedSshOption(opt); got != want {
			t.Fatalf("isManagedSshOption(%q) = %v, want %v", opt, got, want)
		}
	}
}

// ─── containsControlChars ────────────────────────────────────────────────

func TestContainsControlChars(t *testing.T) {
	cases := map[string]bool{
		"normal":                       false,
		"with\tab":                     false, // tab 允许
		"alice":                        false,
		"":                             false,
		"with\nnewline":                true,
		"with\rcr":                     true,
		"with\x00null":                 true,
		"with\x01soh":                  true,
		"with\x7fdel":                  true,
	}
	for s, want := range cases {
		if got := containsControlChars(s); got != want {
			t.Fatalf("containsControlChars(%q) = %v, want %v", s, got, want)
		}
	}
}
