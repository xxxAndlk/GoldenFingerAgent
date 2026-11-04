package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
)

func TestLongTermConfirmAndConflictAPI(t *testing.T) {
	srv, repos := newTestServer(t)
	h := srv.Handler()
	owner := &store.User{UserType: store.UserGeneral, Name: "gov-user", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	ownerID := owner.ID
	edge := &store.LongTermEdge{
		ID: uuid.NewString(), OwnerUserID: ownerID, Subject: "客户A", Relation: "地址",
		Object: "上海", Fact: "客户A住在上海", Status: store.MemoryInferred,
		SourceRefs: []string{"ep-1"}, Confidence: 0.6,
	}
	if err := repos.LongTerm.InsertEdge(context.Background(), edge); err != nil {
		t.Fatal(err)
	}
	rec := postJSON(t, h, "/api/memory/longterm/"+edge.ID+"/confirm", `{"expected_version":0}`)
	if rec.Code != 200 {
		t.Fatalf("confirm status=%d body=%s", rec.Code, rec.Body)
	}
	got, err := repos.LongTerm.GetEdge(context.Background(), ownerID, edge.ID)
	if err != nil || got.Status != store.MemoryConfirmed || got.ConfirmedBy != ownerID {
		t.Fatalf("edge=%+v err=%v", got, err)
	}
	rec = postJSON(t, h, "/api/memory/longterm/"+edge.ID+"/confirm", `{"expected_version":0}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale confirm must conflict: %d %s", rec.Code, rec.Body)
	}
	rec = getJSON(t, h, "/api/memory/longterm?status=confirmed")
	if rec.Code != 200 {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestPersonalPolicyAndDeviceAPI(t *testing.T) {
	srv, repos, _, _ := newWorkTaskServer(t)
	h := srv.Handler()
	ctx := context.Background()
	owner := &store.User{UserType: store.UserGeneral, Name: "policy-owner", TZ: "Asia/Shanghai"}
	if err := repos.Users.Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	ownerID := owner.ID

	rec := postJSON(t, h, "/api/personal-policies",
		`{"policy_type":"preference","name":"沟通风格","content":{"text":"先结论后证据"},"scope_type":"global","status":"active"}`)
	if rec.Code != 201 {
		t.Fatalf("policy create=%d %s", rec.Code, rec.Body)
	}
	body := decodeTaskResponse(t, rec)
	policy := body["policy"].(map[string]any)
	policyID := policy["id"].(string)
	rec = getJSON(t, h, "/api/personal-policies")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), policyID) {
		t.Fatalf("policy list=%d %s", rec.Code, rec.Body)
	}
	rec = postJSON(t, h, "/api/personal-policies/"+policyID+"/status",
		`{"status":"disabled","expected_version":0}`)
	if rec.Code != 200 {
		t.Fatalf("policy status=%d %s", rec.Code, rec.Body)
	}

	rec = postJSON(t, h, "/api/devices",
		`{"id":"dev-1","name":"手机","device_type":"android","online":true}`)
	if rec.Code != 201 {
		t.Fatalf("device create=%d %s", rec.Code, rec.Body)
	}
	rec = postJSON(t, h, "/api/devices/dev-1/capabilities",
		`{"tool_name":"tap","resource_scope":{"package":"com.example"}}`)
	if rec.Code != 201 {
		t.Fatalf("capability create=%d %s", rec.Code, rec.Body)
	}
	caps, err := repos.Devices.Capabilities(ctx, ownerID, "dev-1")
	if err != nil || len(caps) != 1 {
		t.Fatalf("caps=%+v err=%v", caps, err)
	}

	rec = postJSON(t, h, "/api/work-tasks", `{"title":"手机任务","goal":"点一下按钮","profile":"internal"}`)
	if rec.Code != 201 {
		t.Fatalf("task create=%d %s", rec.Code, rec.Body)
	}
	task := decodeTaskResponse(t, rec)["task"].(map[string]any)
	taskID := task["id"].(string)
	rec = postJSON(t, h, "/api/work-tasks/"+taskID+"/devices/dev-1",
		`{"capability_id":"`+caps[0].ID+`"}`)
	if rec.Code != 201 {
		t.Fatalf("bind=%d %s", rec.Code, rec.Body)
	}
	bindings, err := repos.Devices.TaskDevices(ctx, ownerID, taskID)
	if err != nil || len(bindings) != 1 {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	rec = postJSON(t, h, "/api/devices/dev-1/revoke", `{}`)
	if rec.Code != 200 {
		t.Fatalf("revoke=%d %s", rec.Code, rec.Body)
	}
	rec = postJSON(t, h, "/api/work-tasks/"+taskID+"/devices/dev-1", `{}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("revoked device bind must conflict: %d %s", rec.Code, rec.Body)
	}
}
