//go:build windows

package agenthub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// launchDetachedProcess 以分离进程方式启动 BMA：
// 使用 DETACHED_PROCESS 让 bma-server 不属于金手指服务的进程组，
// 金手指重启或退出时不会把 BMA 一起带走。
func launchDetachedProcess(_ context.Context, executable string) error {
	lower := strings.ToLower(executable)
	var cmd *exec.Cmd
	if strings.HasSuffix(lower, ".ps1") {
		// 桌面版启动器：读取 app\current.json + .env，启动对应版本并等待健康检查。
		cmd = exec.Command("powershell.exe",
			"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
			"-File", executable, "-NoBrowser", "-Quiet")
		cmd.Dir = filepath.Dir(filepath.Dir(executable))
	} else {
		cmd = exec.Command(executable)
		dir := filepath.Dir(executable)
		// BMA 从自己目录读取 config/ 与 .env；如果可执行文件在 bin/ 下则上移一层。
		if _, err := os.Stat(filepath.Join(dir, ".env")); err != nil {
			dir = filepath.Dir(dir)
		}
		cmd.Dir = dir
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// 不等待退出：立即释放句柄，进程继续在后台运行。
	return cmd.Process.Release()
}
