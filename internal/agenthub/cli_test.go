package agenthub

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLookCLIPathScansPackageManagerDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PNPM_HOME", root)
	executable := filepath.Join(root, "agent-for-test.cmd")
	if err := os.WriteFile(executable, []byte("@echo off\r\necho 1.0.0\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := lookCLIPath("agent-for-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(path, executable) {
		t.Fatalf("path = %q, want %q", path, executable)
	}
}

func TestKimiDesktopExecutableIsNotKimiCodeCLI(t *testing.T) {
	entry := catalogForTest(t, "kimi-code")
	processes := []Process{{Name: "Kimi", Path: `D:\app\KimiWork\Kimi.exe`}}
	if usableCLIPath(entry, `D:\app\KimiWork\Kimi.exe`, processes) {
		t.Fatal("KimiWork desktop executable must not be advertised as Kimi Code CLI")
	}
	if !usableCLIPath(entry, `C:\Users\test\AppData\Roaming\npm\kimi.cmd`, processes) {
		t.Fatal("independent Kimi Code CLI should remain usable")
	}
}

func TestDelegateRejectsFileAsWorkDir(t *testing.T) {
	manager := testManager(t)
	file := filepath.Join(t.TempDir(), "not-a-directory.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.DelegateAt(context.Background(), "bma", "测试", file); err == nil || !strings.Contains(err.Error(), "必须是文件夹") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteWindowsCmdUsesArgumentsAndWorkDir(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows cmd integration")
	}
	dir := t.TempDir()
	executable := filepath.Join(dir, "fake-agent.cmd")
	content := "@echo off\r\necho CLI_OK:%1\r\necho WORK_DIR:%CD%\r\n"
	if err := os.WriteFile(executable, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := executeCLI(context.Background(), executable, []string{"hello"}, dir)
	if err != nil {
		t.Fatalf("executeCLI: %v: %s", err, output)
	}
	text := string(output)
	if !strings.Contains(text, "CLI_OK:hello") || !strings.Contains(text, "WORK_DIR:") || !strings.Contains(text, filepath.Base(dir)) {
		t.Fatalf("output = %q", text)
	}
}
