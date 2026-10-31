package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"goldenfinger/agent/internal/llm"
)

const (
	maxSystemReadBytes   = 8 << 20
	maxSystemWriteBytes  = 1 << 20
	maxSystemOutputBytes = 64 << 10
)

func systemWorkspace(tc *ToolContext) (string, error) {
	if tc != nil && tc.Runtime != nil && tc.Runtime.Tools != nil && tc.Runtime.Tools.Workspace != "" {
		return filepath.Abs(tc.Runtime.Tools.Workspace)
	}
	return os.Getwd()
}

func resolveSystemPath(tc *ToolContext, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path 不能为空")
	}
	if !filepath.IsAbs(path) {
		workspace, err := systemWorkspace(tc)
		if err != nil {
			return "", fmt.Errorf("确定项目目录：%w", err)
		}
		path = filepath.Join(workspace, path)
	}
	return filepath.Abs(filepath.Clean(path))
}

func systemError(err error) ToolResult {
	return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}
}

// ---- read ----

type readFileArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

type readFileTool struct{}

func (readFileTool) Spec() llm.ToolSpec {
	return toolSpec("read", "读取电脑上的文本文件。path 支持相对项目目录的路径或绝对路径；offset 从 1 开始，limit 默认 400、最大 2000。修改已有文件前先用它读取。",
		map[string]any{
			"path":   map[string]any{"type": "string", "description": "要读取的文本文件路径"},
			"offset": map[string]any{"type": "integer", "description": "可选：起始行，从 1 开始"},
			"limit":  map[string]any{"type": "integer", "description": "可选：最多读取多少行"},
		}, "path")
}

func (readFileTool) Execute(_ context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a readFileArgs
	if err := decodeArgs(args, &a); err != nil {
		return systemError(err), nil
	}
	path, err := resolveSystemPath(tc, a.Path)
	if err != nil {
		return systemError(err), nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return systemError(fmt.Errorf("读取文件：%w", err)), nil
	}
	if info.IsDir() {
		return systemError(errors.New("read 只能读取文件，不能读取文件夹")), nil
	}
	if info.Size() > maxSystemReadBytes {
		return systemError(fmt.Errorf("文件超过 %d MB，请用 bash 分段提取文本", maxSystemReadBytes/(1<<20))), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return systemError(fmt.Errorf("读取文件：%w", err)), nil
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return systemError(errors.New("read 只支持文本文件，检测到二进制内容")), nil
	}
	if a.Offset < 0 || a.Limit < 0 {
		return systemError(errors.New("offset 和 limit 不能为负数")), nil
	}
	offset := a.Offset
	if offset == 0 {
		offset = 1
	}
	limit := a.Limit
	if limit == 0 {
		limit = 400
	}
	if limit > 2000 {
		limit = 2000
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	start := offset - 1
	if start > len(lines) {
		start = len(lines)
	}
	end := start + limit
	if end > len(lines) {
		end = len(lines)
	}
	content := strings.Join(lines[start:end], "\n")
	truncated := end < len(lines)
	if len(content) > maxSystemOutputBytes {
		content = truncate(content, maxSystemOutputBytes)
		truncated = true
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{
		"path": path, "content": content, "offset": offset, "returned_lines": end - start,
		"total_lines": len(lines), "truncated": truncated,
	}}, nil
}

// ---- write ----

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type writeFileTool struct{}

func (writeFileTool) Spec() llm.ToolSpec {
	return toolSpec("write", "把完整文本写入电脑文件，会创建缺失的父目录；已有文件会被覆盖。修改已有文件前必须先 read，content 必须是完整内容而不是补丁。",
		map[string]any{
			"path":    map[string]any{"type": "string", "description": "相对项目目录或绝对的文件路径"},
			"content": map[string]any{"type": "string", "description": "要写入的完整文本，可以为空字符串"},
		}, "path", "content")
}

func (writeFileTool) Execute(_ context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a writeFileArgs
	if err := decodeArgs(args, &a); err != nil {
		return systemError(err), nil
	}
	if len(a.Content) > maxSystemWriteBytes {
		return systemError(fmt.Errorf("单次 write 不能超过 %d MB", maxSystemWriteBytes/(1<<20))), nil
	}
	path, err := resolveSystemPath(tc, a.Path)
	if err != nil {
		return systemError(err), nil
	}
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		return systemError(errors.New("write 的目标不能是文件夹")), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return systemError(fmt.Errorf("创建父目录：%w", err)), nil
	}
	perm := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		perm = info.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(a.Content), perm); err != nil {
		return systemError(fmt.Errorf("写入文件：%w", err)), nil
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{
		"path": path, "bytes_written": len(a.Content), "reply": "文件已写入",
	}}, nil
}

// ---- bash ----

type bashArgs struct {
	Command        string `json:"command"`
	WorkDir        string `json:"work_dir"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type bashTool struct{}

func (bashTool) Spec() llm.ToolSpec {
	return toolSpec("bash", "在电脑上执行一条 shell 命令并返回输出和退出码。work_dir 默认当前项目；timeout_seconds 默认 120、最长 300。Windows 使用 cmd.exe，其他系统使用 bash。不要启动需要长期驻留或交互输入的命令。",
		map[string]any{
			"command":         map[string]any{"type": "string", "description": "完整命令"},
			"work_dir":        map[string]any{"type": "string", "description": "可选：命令工作目录"},
			"timeout_seconds": map[string]any{"type": "integer", "description": "可选：1 到 300 秒"},
		}, "command")
}

type cappedOutput struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	truncated bool
}

func (b *cappedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := maxSystemOutputBytes - b.buf.Len()
	if remaining > 0 {
		part := p
		if len(part) > remaining {
			part = part[:remaining]
		}
		_, _ = b.buf.Write(part)
	}
	if len(p) > remaining {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedOutput) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String(), b.truncated
}

func (bashTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a bashArgs
	if err := decodeArgs(args, &a); err != nil {
		return systemError(err), nil
	}
	command := strings.TrimSpace(a.Command)
	if command == "" {
		return systemError(errors.New("command 不能为空")), nil
	}
	if len(command) > 20000 {
		return systemError(errors.New("command 不能超过 20000 字节")), nil
	}
	workDir := a.WorkDir
	if strings.TrimSpace(workDir) == "" {
		var err error
		workDir, err = systemWorkspace(tc)
		if err != nil {
			return systemError(err), nil
		}
	} else {
		var err error
		workDir, err = resolveSystemPath(tc, workDir)
		if err != nil {
			return systemError(err), nil
		}
	}
	info, err := os.Stat(workDir)
	if err != nil || !info.IsDir() {
		return systemError(errors.New("work_dir 必须是可访问的文件夹")), nil
	}
	timeout := a.TimeoutSeconds
	if timeout == 0 {
		timeout = 120
	}
	if timeout < 1 || timeout > 300 {
		return systemError(errors.New("timeout_seconds 必须在 1 到 300 之间")), nil
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(runCtx, "cmd.exe", "/D", "/S", "/C", command)
	} else {
		cmd = exec.CommandContext(runCtx, "bash", "-lc", command)
	}
	cmd.Dir = workDir
	output := &cappedOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	runErr := cmd.Run()
	text, truncated := output.snapshot()
	data := map[string]any{
		"work_dir": workDir, "exit_code": 0, "output": strings.TrimSpace(text), "truncated": truncated,
	}
	if runCtx.Err() != nil {
		data["exit_code"] = -1
		data["timed_out"] = true
		data["error"] = "命令已取消或超过时间限制"
		return ToolResult{Status: StatusError, Data: data}, nil
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			data["exit_code"] = exitErr.ExitCode()
			return ToolResult{Status: StatusOK, Data: data}, nil
		}
		data["exit_code"] = -1
		data["error"] = runErr.Error()
		return ToolResult{Status: StatusError, Data: data}, nil
	}
	return ToolResult{Status: StatusOK, Data: data}, nil
}

// SystemTools 是仅面向桌面/电脑服务的 Pi 风格基础工具集。
func SystemTools() []Tool {
	return []Tool{readFileTool{}, bashTool{}, writeFileTool{}}
}
