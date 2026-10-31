//go:build !windows

package agenthub

import (
	"context"
	"os/exec"
)

// launchDetachedProcess 在非 Windows 平台用普通后台进程启动 BMA。
func launchDetachedProcess(_ context.Context, executable string) error {
	cmd := exec.Command(executable)
	cmd.Dir = parentDir(parentDir(executable))
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
