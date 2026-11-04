package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/store"
)

func gatewayHarness(t *testing.T) (*Session, *Runtime, *store.Repos, *store.User, context.Context, string) {
	t.Helper()
	sess, rt, u := testHarness(t)
	workspace := t.TempDir()
	rt.Registry = NewRegistry(append(DefaultTools(), SystemTools()...)...)
	rt.Tools.Workspace = workspace
	rt.ToolProfile = "read"
	rt.WorkTaskID = "task-gate"
	rt.JobID = "job-gate"
	return sess, rt, rt.Tools.Repos, u, context.Background(), workspace
}

func addGrant(t *testing.T, repos *store.Repos, ownerID, action string, scope string,
	remaining int, expires *time.Time) *store.ActionGrant {
	t.Helper()
	grant := &store.ActionGrant{
		OwnerUserID: ownerID, TaskID: "task-gate", ActionType: action,
		ResourceScope: json.RawMessage(scope), RiskLevel: store.GrantSensitive,
		RemainingUses: remaining, ExpiresAt: expires,
	}
	if err := repos.Grants.Create(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	return grant
}

func TestToolGatewayProfileAndWorkspace(t *testing.T) {
	sess, rt, repos, u, ctx, workspace := gatewayHarness(t)

	for _, spec := range rt.Registry.SpecsFor("read") {
		if spec.Name == "write" || spec.Name == "bash" || spec.Name == "delegate_agent" {
			t.Fatalf("read profile must not expose %s", spec.Name)
		}
	}
	writeCall := llm.ToolCall{ID: "c1", Name: "write",
		Arguments: json.RawMessage(`{"path":"note.txt","content":"hello"}`)}
	res, err := rt.Registry.Execute(ctx, writeCall, &ToolContext{Session: sess, Runtime: rt})
	if err != nil || res.Status != StatusError {
		t.Fatalf("read write must be denied: %+v err=%v", res, err)
	}
	invocations, err := repos.ToolInvocations.ListByTask(ctx, u.ID, "task-gate", 10)
	if err != nil || len(invocations) != 1 || invocations[0].Status != store.ToolDenied {
		t.Fatalf("denied invocation=%+v err=%v res=%+v", invocations, err, res)
	}

	rt.ToolProfile = "internal"
	inside, err := rt.Registry.Execute(ctx, llm.ToolCall{ID: "c2", Name: "write",
		Arguments: json.RawMessage(`{"path":"note.txt","content":"hello"}`)},
		&ToolContext{Session: sess, Runtime: rt})
	if err != nil || inside.Status != StatusOK {
		t.Fatalf("internal workspace write: %+v err=%v", inside, err)
	}
	outside, err := rt.Registry.Execute(ctx, llm.ToolCall{ID: "c3", Name: "write",
		Arguments: json.RawMessage(`{"path":"../escape.txt","content":"bad"}`)},
		&ToolContext{Session: sess, Runtime: rt})
	if err != nil || outside.Status != StatusError {
		t.Fatalf("write outside workspace must be denied: %+v err=%v", outside, err)
	}
	_ = workspace
}

func TestToolGatewayGrantExpiryRevocationAndHash(t *testing.T) {
	sess, rt, repos, u, ctx, workspace := gatewayHarness(t)
	rt.ToolProfile = "internal"
	args := json.RawMessage(`{"command":"echo ok","work_dir":"` + strings.ReplaceAll(workspace, `\`, `\\`) + `"}`)
	hash := hashToolArgs(args)
	grant := addGrant(t, repos, u.ID, "bash", `{"args_hash":"`+hash+`"}`, 1, nil)

	modified := json.RawMessage(`{"command":"echo changed","work_dir":"` + strings.ReplaceAll(workspace, `\`, `\\`) + `"}`)
	res, err := rt.Registry.Execute(ctx, llm.ToolCall{ID: "c1", Name: "bash", Arguments: modified},
		&ToolContext{Session: sess, Runtime: rt})
	if err != nil || res.Status != StatusError {
		t.Fatalf("hash mismatch must deny: %+v err=%v", res, err)
	}

	res, err = rt.Registry.Execute(ctx, llm.ToolCall{ID: "c2", Name: "bash", Arguments: args},
		&ToolContext{Session: sess, Runtime: rt})
	if err != nil || res.Status != StatusOK {
		t.Fatalf("valid grant must allow: %+v err=%v", res, err)
	}
	got, err := repos.Grants.Get(ctx, u.ID, grant.ID)
	if err != nil || got.RemainingUses != 0 || got.Status != store.GrantExhausted {
		t.Fatalf("grant must be exhausted: %+v err=%v", got, err)
	}
	res, _ = rt.Registry.Execute(ctx, llm.ToolCall{ID: "c3", Name: "bash", Arguments: args},
		&ToolContext{Session: sess, Runtime: rt})
	if res.Status != StatusError {
		t.Fatalf("exhausted grant must deny: %+v", res)
	}

	expired := time.Now().Add(-time.Minute)
	addGrant(t, repos, u.ID, "bash", `{}`, 1, &expired)
	res, _ = rt.Registry.Execute(ctx, llm.ToolCall{ID: "c4", Name: "bash", Arguments: args},
		&ToolContext{Session: sess, Runtime: rt})
	if res.Status != StatusError {
		t.Fatalf("expired grant must deny: %+v", res)
	}

	fresh := addGrant(t, repos, u.ID, "bash", `{}`, 1, nil)
	if _, err := repos.Grants.Revoke(ctx, u.ID, fresh.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, _ = rt.Registry.Execute(ctx, llm.ToolCall{ID: "c5", Name: "bash", Arguments: args},
		&ToolContext{Session: sess, Runtime: rt})
	if res.Status != StatusError {
		t.Fatalf("revoked grant must deny: %+v", res)
	}
}

func TestToolInvocationRedactsSecrets(t *testing.T) {
	sess, rt, repos, u, ctx, workspace := gatewayHarness(t)
	rt.ToolProfile = "internal"
	args := json.RawMessage(`{"command":"echo ok","work_dir":"` +
		strings.ReplaceAll(workspace, `\`, `\\`) + `","password":"super-secret"}`)
	addGrant(t, repos, u.ID, "bash", `{}`, 1, nil)
	res, err := rt.Registry.Execute(ctx, llm.ToolCall{ID: "c1", Name: "bash", Arguments: args},
		&ToolContext{Session: sess, Runtime: rt})
	if err != nil || res.Status != StatusOK {
		t.Fatalf("bash: %+v err=%v", res, err)
	}
	items, err := repos.ToolInvocations.ListByTask(ctx, u.ID, "task-gate", 10)
	if err != nil || len(items) == 0 {
		t.Fatalf("invocations=%+v err=%v", items, err)
	}
	for _, item := range items {
		if strings.Contains(string(item.ArgsRedacted), "super-secret") {
			t.Fatalf("secret leaked into invocation: %s", item.ArgsRedacted)
		}
	}
}
