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
	return exec.CommandContext(ctx, "sudo", "-n", "-u", actualUser, "-H", "-i", "env")
}
