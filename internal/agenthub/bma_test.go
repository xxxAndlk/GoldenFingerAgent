package agenthub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBMANativeDiscoveryAndDelegate(t *testing.T) {
	var createRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/sessions":
			if r.Method != http.MethodPost {
				t.Errorf("method = %s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&createRequest); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"session-native-1","status":"completed","result":"BMA 原生任务已完成"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager := testManager(t)
	manager.BMAURL = server.URL
	agents := manager.Scan(context.Background())
	var bma Agent
	for _, agent := range agents {
		if agent.ID == "bma" {
			bma = agent
		}
	}
	if !bma.Running || bma.Endpoint != server.URL+"/api" || !canCall(bma, "delegate") || !canCall(bma, "project") {
		t.Fatalf("BMA = %+v", bma)
	}
	result, err := manager.Delegate(context.Background(), "bma", "检查当前项目")
	if err != nil {
		t.Fatal(err)
	}
	if result.TaskID != "session-native-1" || result.State != "completed" || result.Output != "BMA 原生任务已完成" {
		t.Fatalf("result = %+v", result)
	}
	if createRequest["goal"] != "检查当前项目" || createRequest["work_dir"] != manager.Workspace || createRequest["gear"] != "daily" {
		t.Fatalf("request = %+v", createRequest)
	}
}

func TestBMAUnhealthyDoesNotAdvertiseDelegate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	manager := testManager(t)
	manager.BMAURL = server.URL
	agent := manager.discoverBMA(context.Background(), Agent{ID: "bma", Kind: "bma", Capabilities: []string{"scan", "project"}})
	if agent.Running || canCall(agent, "delegate") || agent.Endpoint != "" {
		t.Fatalf("BMA = %+v", agent)
	}
}
