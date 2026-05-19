package outbound

import (
	"context"
	"os"
	"os/exec"
	"runtime"
)

func buildEnvCommand(ctx context.Context, actualUser string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return nil
	}

	cur, _ := userCurrentFunc()
	isSameUser := cur != nil && normalizeLocalUserName(cur.Username) == normalizeLocalUserName(actualUser)
	if isSameUser {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/zsh"
		}
		return exec.CommandContext(ctx, shell, "-ilc", "env")
	}
	// 切换用户走 sudo：调用方应当已经过 requireSystemSshUserForOS 校验，
	// 此处再做一次以阻断未来新增的调用路径意外引入未校验用户名。
	if !isPOSIXUserName(actualUser) {
		return nil
	}
	return exec.CommandContext(ctx, "sudo", "-n", "-u", actualUser, "-H", "-i", "env")
}
