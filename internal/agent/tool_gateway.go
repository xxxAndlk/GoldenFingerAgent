package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/store"
)

type toolAuthorization struct {
	invocation *store.ToolInvocation
	db         *store.DB
}

func authorizeTool(ctx context.Context, call llm.ToolCall, tc *ToolContext, desc ToolDescriptor) (*toolAuthorization, *ToolResult) {
	if tc == nil || tc.Runtime == nil || tc.Runtime.ToolProfile == "" || tc.Runtime.WorkTaskID == "" {
		return nil, nil
	}
	ownerID := ""
	if tc.Session != nil {
		ownerID = strings.TrimSpace(tc.Session.UserID)
	}
	if ownerID == "" {
		result := deniedToolResult("缺少用户身份", "")
		return nil, &result
	}
	repos := toolRepos(tc)
	if repos == nil || repos.DB == nil {
		result := deniedToolResult("工具门禁缺少数据库", "")
		return nil, &result
	}
	if !profileAllows(tc.Runtime.ToolProfile, desc) {
		return nil, denyWithRecord(ctx, repos, tc, desc, call, "当前 Profile 不允许工具 "+call.Name)
	}
	if err := checkWorkspaceScope(tc.Runtime.ToolProfile, call, tc); err != nil {
		return nil, denyWithRecord(ctx, repos, tc, desc, call, err.Error())
	}
	if err := checkDeviceCapability(ctx, repos, ownerID, tc.Runtime.WorkTaskID, call.Name); err != nil {
		return nil, denyWithRecord(ctx, repos, tc, desc, call, err.Error())
	}
	argsHash := hashToolArgs(call.Arguments)
	protected := desc.RequiresGrant || desc.Risk == RiskExternalWrite || desc.Risk == RiskSensitive
	grantID := ""
	if protected {
		grant, err := findGrant(ctx, repos, ownerID, tc.Runtime.WorkTaskID, call.Name, argsHash, call.Arguments)
		if err != nil {
			return nil, denyWithRecord(ctx, repos, tc, desc, call, err.Error())
		}
		if grant == nil {
			return nil, denyWithRecord(ctx, repos, tc, desc, call, "没有覆盖该工具与内容 hash 的有效授权")
		}
		grantID = grant.ID
	}
	invocation := &store.ToolInvocation{
		OwnerUserID: ownerID, TaskID: tc.Runtime.WorkTaskID, JobID: tc.Runtime.JobID,
		SessionID: sessionID(tc), ToolCallID: call.ID, ToolName: call.Name,
		RiskLevel: string(desc.Risk), ArgsRedacted: redactToolArgs(call.Arguments),
		ArgsHash: argsHash, GrantID: grantID,
		IdempotencyKey: toolInvocationKey(tc.Runtime, call, argsHash),
		Status:         store.ToolApproved, StartedAt: timePtr(time.Now()),
	}
	if protected {
		consumed, err := repos.Grants.Consume(ctx, ownerID, grantID, time.Now())
		if err != nil || !consumed {
			return nil, denyWithRecord(ctx, repos, tc, desc, call, "授权已过期、撤销或次数耗尽")
		}
	}
	if err := repos.ToolInvocations.Create(ctx, invocation); err != nil {
		if errors.Is(err, store.ErrConflict) {
			existing, getErr := repos.ToolInvocations.GetByKey(ctx, ownerID, invocation.IdempotencyKey)
			if getErr == nil && existing.Status == store.ToolSuccess {
				result := deniedToolResult("相同工具调用已执行，禁止重复副作用", existing.ID)
				return nil, &result
			}
		} else {
			result := deniedToolResult("记录工具调用失败："+err.Error(), "")
			return nil, &result
		}
	}
	return &toolAuthorization{invocation: invocation, db: repos.DB}, nil
}

func denyWithRecord(ctx context.Context, repos *store.Repos, tc *ToolContext, desc ToolDescriptor,
	call llm.ToolCall, reason string) *ToolResult {
	argsHash := hashToolArgs(call.Arguments)
	invocation := &store.ToolInvocation{
		OwnerUserID: tc.Session.UserID, TaskID: tc.Runtime.WorkTaskID, JobID: tc.Runtime.JobID,
		SessionID: sessionID(tc), ToolCallID: call.ID, ToolName: call.Name,
		RiskLevel: string(desc.Risk), ArgsRedacted: redactToolArgs(call.Arguments), ArgsHash: argsHash,
		IdempotencyKey: toolInvocationKey(tc.Runtime, call, argsHash) + ":denied",
		Status:         store.ToolDenied, ResultSummary: truncate(reason, 1000),
	}
	recordErr := repos.ToolInvocations.Create(ctx, invocation)
	result := deniedToolResult(reason, invocation.ID)
	if recordErr != nil {
		if data, ok := result.Data.(map[string]any); ok {
			data["record_error"] = recordErr.Error()
		}
	}
	return &result
}

func (a *toolAuthorization) finish(ctx context.Context, desc ToolDescriptor, res ToolResult, execErr error) {
	if a == nil || a.invocation == nil || a.db == nil {
		return
	}
	repos := store.NewRepos(a.db)
	status := store.ToolSuccess
	if execErr != nil || res.Status == StatusError || res.Status == StatusLimit {
		status = store.ToolFailed
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) &&
		(desc.Risk == RiskExternalWrite || desc.Risk == RiskSensitive) {
		status = store.ToolUnknown
	}
	summary := execErrSummary(res, execErr)
	receipt := mustJSON(map[string]any{
		"tool": a.invocation.ToolName, "risk": desc.Risk, "status": status,
		"args_hash": a.invocation.ArgsHash, "at": time.Now().UTC().Format(time.RFC3339Nano),
	})
	_, _ = repos.ToolInvocations.UpdateStatus(ctx, a.invocation.OwnerUserID, a.invocation.ID,
		status, summary, receipt, time.Now())
}

func toolRepos(tc *ToolContext) *store.Repos {
	if tc == nil || tc.Runtime == nil || tc.Runtime.Tools == nil {
		return nil
	}
	return tc.Runtime.Tools.Repos
}

func findGrant(ctx context.Context, repos *store.Repos, ownerID, taskID, toolName, argsHash string,
	args json.RawMessage) (*store.ActionGrant, error) {
	grants, err := repos.Grants.ActiveForTask(ctx, ownerID, taskID, time.Now())
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &parsed)
	}
	for i := range grants {
		if grantMatches(&grants[i], toolName, argsHash, parsed) {
			return &grants[i], nil
		}
	}
	return nil, nil
}

func grantMatches(g *store.ActionGrant, toolName, argsHash string, args map[string]any) bool {
	if g == nil {
		return false
	}
	action := strings.TrimSpace(g.ActionType)
	if action != "*" && action != toolName && action != "tool:"+toolName {
		return false
	}
	if len(g.ResourceScope) == 0 || string(g.ResourceScope) == "{}" {
		return true
	}
	var scope map[string]any
	if err := json.Unmarshal(g.ResourceScope, &scope); err != nil {
		return false
	}
	for key, want := range scope {
		switch key {
		case "tool":
			if fmt.Sprint(want) != toolName {
				return false
			}
		case "args_hash", "content_hash":
			if hash, ok := want.(string); !ok || hash != argsHash {
				return false
			}
		case "work_dir", "path", "target", "channel", "agent_id", "resource":
			got := ""
			if args != nil {
				if value, ok := args[key]; ok {
					got = fmt.Sprint(value)
				}
			}
			if got == "" || !strings.EqualFold(filepath.ToSlash(got), filepath.ToSlash(fmt.Sprint(want))) {
				return false
			}
		default:
			if args == nil || fmt.Sprint(args[key]) != fmt.Sprint(want) {
				return false
			}
		}
	}
	return true
}

func checkWorkspaceScope(profile string, call llm.ToolCall, tc *ToolContext) error {
	if profile != "internal" && profile != "external" {
		return nil
	}
	if call.Name != "write" && call.Name != "bash" {
		return nil
	}
	var args struct {
		Path    string `json:"path"`
		WorkDir string `json:"work_dir"`
	}
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return fmt.Errorf("工具参数无效：%w", err)
		}
	}
	workspace := ""
	if tc != nil && tc.Runtime != nil && tc.Runtime.Tools != nil {
		workspace = tc.Runtime.Tools.Workspace
	}
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	target := args.Path
	if call.Name == "bash" {
		target = args.WorkDir
	}
	if strings.TrimSpace(target) == "" {
		return nil
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(workspace, target)
	}
	target, err = filepath.Abs(filepath.Clean(target))
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(workspace, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s 超出授权工作目录", call.Name)
	}
	return nil
}

func checkDeviceCapability(ctx context.Context, repos *store.Repos, ownerID, taskID, toolName string) error {
	switch toolName {
	case "screen_observe", "tap", "input_text", "swipe", "back", "open_app", "wait":
	default:
		return nil
	}
	bindings, err := repos.Devices.TaskDevices(ctx, ownerID, taskID)
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}
	for _, binding := range bindings {
		device, err := repos.Devices.Get(ctx, ownerID, binding.DeviceID)
		if err != nil || device.RevokedAt != nil || !device.Online {
			continue
		}
		capabilities, err := repos.Devices.Capabilities(ctx, ownerID, binding.DeviceID)
		if err != nil {
			continue
		}
		for _, capability := range capabilities {
			if capability.ToolName == "*" || capability.ToolName == toolName {
				return nil
			}
		}
	}
	return fmt.Errorf("绑定设备已撤销、离线或缺少工具 %s 的能力", toolName)
}

func hashToolArgs(raw json.RawMessage) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func toolInvocationKey(runtime *Runtime, call llm.ToolCall, argsHash string) string {
	part := call.ID
	if strings.TrimSpace(part) == "" {
		part = argsHash
	}
	return strings.Join([]string{"tool", runtime.WorkTaskID, runtime.JobID, call.Name, part}, ":")
}

func redactToolArgs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return json.RawMessage(`{"redacted":"invalid json"}`)
	}
	redacted := redactValue(value)
	out, err := json.Marshal(redacted)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return out
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "passwd") ||
				strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
				strings.Contains(lower, "cookie") || strings.Contains(lower, "authorization") ||
				strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") {
				out[key] = "***"
				continue
			}
			out[key] = redactValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactValue(item)
		}
		return out
	default:
		return value
	}
}

func deniedToolResult(reason, invocationID string) ToolResult {
	data := map[string]any{"error": reason, "denied": true}
	if invocationID != "" {
		data["tool_invocation_id"] = invocationID
	}
	return ToolResult{Status: StatusError, Data: data, Trace: "tool gate → denied"}
}

func execErrSummary(res ToolResult, err error) string {
	if err != nil {
		return truncate(err.Error(), 1000)
	}
	raw, marshalErr := json.Marshal(res.Data)
	if marshalErr != nil {
		return string(res.Status)
	}
	return truncate(string(raw), 1000)
}

func sessionID(tc *ToolContext) string {
	if tc == nil || tc.Session == nil {
		return ""
	}
	return tc.Session.ID
}

func timePtr(at time.Time) *time.Time { return &at }

// mustJSON 与 superagent 中的同名函数语义一致，但避免跨包依赖。
func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
