package agenthub

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCCSwitchUsageAndSafeClaudeSwitch(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, ".cc-switch", "cc-switch.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	schema := []string{
		`CREATE TABLE providers (id TEXT,app_type TEXT,name TEXT,settings_config TEXT,sort_index INTEGER,limit_daily_usd TEXT,limit_monthly_usd TEXT,is_current BOOLEAN,PRIMARY KEY(id,app_type))`,
		`CREATE TABLE provider_health (provider_id TEXT,app_type TEXT,is_healthy INTEGER,PRIMARY KEY(provider_id,app_type))`,
		`CREATE TABLE usage_daily_rollups (date TEXT,app_type TEXT,provider_id TEXT,total_cost_usd TEXT)`,
		`CREATE TABLE proxy_config (app_type TEXT,proxy_enabled INTEGER)`,
	}
	for _, statement := range schema {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = db.Exec(`INSERT INTO providers VALUES ('p1','claude','旧密钥','{"env":{"ANTHROPIC_AUTH_TOKEN":"old"}}',1,'5','50',1),('p2','claude','新密钥','{"env":{"ANTHROPIC_AUTH_TOKEN":"new","ANTHROPIC_BASE_URL":"https://example.com"}}',2,'8','80',0),('h1','hermes','Hermes','{}',1,NULL,NULL,0)`)
	_, _ = db.Exec(`INSERT INTO usage_daily_rollups VALUES ('2026-10-29','claude','p2','1.25'),('2026-10-01','claude','p2','2.75')`)
	_ = db.Close()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	old := `{"env":{"ANTHROPIC_AUTH_TOKEN":"old","KEEP":"yes"},"hooks":{"enabled":true}}`
	if err := os.WriteFile(settingsPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewCCSwitch(dbPath, home)
	provider, err := store.SwitchClaude(context.Background(), "p2")
	if err != nil {
		t.Fatal(err)
	}
	if !provider.Current || provider.Name != "新密钥" {
		t.Fatalf("provider = %+v", provider)
	}
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var live map[string]any
	if err := json.Unmarshal(raw, &live); err != nil {
		t.Fatal(err)
	}
	env := live["env"].(map[string]any)
	if env["ANTHROPIC_AUTH_TOKEN"] != "new" || env["KEEP"] != "yes" || live["hooks"] == nil {
		t.Fatalf("live = %+v", live)
	}
	backups, err := filepath.Glob(settingsPath + ".goldenfinger-*.bak")
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v, err = %v", backups, err)
	}
	providers, err := store.Providers(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 || !providers[0].Current {
		t.Fatalf("providers = %+v", providers)
	}
	serialized, _ := json.Marshal(providers)
	if strings.Contains(string(serialized), "new") || strings.Contains(string(serialized), "old") {
		t.Fatal("provider response leaked key material")
	}
}
