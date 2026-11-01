package gosvc

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type started struct {
	addr  string
	token string
}

// parseStarted 解析 StartServer 返回的连接信息。
func parseStarted(t *testing.T, info string) started {
	t.Helper()
	var parsed struct {
		Addr     string `json:"addr"`
		Token    string `json:"token"`
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal([]byte(info), &parsed); err != nil {
		t.Fatalf("info json: %v (%s)", err, info)
	}
	if parsed.Addr == "" || parsed.Token == "" || parsed.DeviceID == "" {
		t.Fatalf("incomplete info: %s", info)
	}
	if !strings.HasPrefix(parsed.Addr, "127.0.0.1:") {
		t.Fatalf("must bind loopback only, got %s", parsed.Addr)
	}
	return started{addr: parsed.Addr, token: parsed.Token}
}

// startForTest 在临时目录启动真实服务并返回连接信息。
func startForTest(t *testing.T) started {
	t.Helper()
	dir := t.TempDir()
	info, err := StartServer(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	t.Cleanup(func() { StopServer() })
	return parseStarted(t, info)
}

// TestSettingsModelsPersistAcrossRestart 验证 Android filesDir 中的
// settings.json 在进程重启后仍保留模型方案。
func TestSettingsModelsPersistAcrossRestart(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	info, err := StartServer(dataDir)
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	t.Cleanup(func() { StopServer() })
	first := parseStarted(t, info)
	code, body := first.do(t, http.MethodPost, "/api/settings/models",
		`{"name":"手机重启测试","base_url":"https://api.deepseek.com","model":"deepseek-chat","api_key":"test-key","active":false}`)
	if code != http.StatusOK {
		t.Fatalf("save model=%d body=%s", code, body)
	}
	StopServer()

	info, err = StartServer(dataDir)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	second := parseStarted(t, info)
	code, body = second.do(t, http.MethodGet, "/api/settings", "")
	if code != http.StatusOK || !strings.Contains(string(body), "手机重启测试") {
		t.Fatalf("model lost after restart: %d %s", code, body)
	}
}

func (s started) do(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, "http://"+s.addr+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Token", s.token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// TestRequiresToken 验证所有请求都必须带 X-Token（手机端唯一的鉴权边界）。
func TestRequiresToken(t *testing.T) {
	s := startForTest(t)
	resp, err := http.Get("http://" + s.addr + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d, want 401", resp.StatusCode)
	}
	if code, _ := s.do(t, http.MethodGet, "/api/me", ""); code != http.StatusOK {
		t.Fatalf("with token status=%d", code)
	}
}

// TestInboxEndpointsAreWired 覆盖手机上最容易漏装配的部分：
// 通知监听上报的事件必须真的入库，而不是返回 503。
func TestInboxEndpointsAreWired(t *testing.T) {
	s := startForTest(t)
	code, body := s.do(t, http.MethodPost, "/api/inbox/events", `{"events":[
		{"source_pkg":"com.cainiao","app_name":"菜鸟","title":"包裹到站","text":"取件码 8823-4，请及时领取","posted_at":"2026-10-30T09:00:00+08:00"}
	]}`)
	if code != http.StatusOK {
		t.Fatalf("ingest=%d body=%s", code, body)
	}
	var ingested struct {
		Ingested int `json:"ingested"`
	}
	if err := json.Unmarshal(body, &ingested); err != nil || ingested.Ingested != 1 {
		t.Fatalf("ingested=%+v err=%v body=%s", ingested, err, body)
	}

	code, body = s.do(t, http.MethodGet, "/api/inbox/events?kind=all", "")
	if code != http.StatusOK || !strings.Contains(string(body), "8823-4") {
		t.Fatalf("list=%d body=%s", code, body)
	}

	// 内容匹配延后提醒：手机端只上报通知字段。
	code, body = s.do(t, http.MethodPost, "/api/inbox/snooze",
		`{"source_pkg":"com.cainiao","title":"包裹到站","text":"8823-4","minutes":15}`)
	if code != http.StatusOK {
		t.Fatalf("snooze=%d body=%s", code, body)
	}
}

// TestMemoryCaptureAndSearch 验证情景记忆写入后可检索（手机端语音记录入口）。
func TestMemoryCaptureAndSearch(t *testing.T) {
	s := startForTest(t)
	code, body := s.do(t, http.MethodPost, "/api/memory/capture",
		`{"text":"今天下午去了社区医院拿降压药","context_before":"","context_after":"","occurred_at":"2026-10-30T15:00:00+08:00"}`)
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("capture=%d body=%s", code, body)
	}
	code, body = s.do(t, http.MethodGet, "/api/memory/search?q="+urlQuery("社区医院"), "")
	if code != http.StatusOK || !strings.Contains(string(body), "社区医院") {
		t.Fatalf("search=%d body=%s", code, body)
	}
}

// TestEmailPasswordSecretNotExposed 验证静态页与设置接口不泄漏真实 Key。
func TestSettingsRedactsKey(t *testing.T) {
	s := startForTest(t)
	code, body := s.do(t, http.MethodGet, "/api/settings/llm", "")
	if code != http.StatusOK {
		t.Fatalf("settings=%d body=%s", code, body)
	}
	if strings.Contains(string(body), "api_key\":\"sk-") {
		t.Fatalf("settings leaked plaintext key: %s", body)
	}
}

func urlQuery(text string) string { return url.QueryEscape(text) }

// TestPhoneRelayEndpoints 验证手机侧中继端点可用且未配对时如实报错。
func TestPhoneRelayEndpoints(t *testing.T) {
	s := startForTest(t)

	code, body := s.do(t, http.MethodGet, "/api/relay/phone/status", "")
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	var status struct {
		Paired bool `json:"paired"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("status json: %v (%s)", err, body)
	}
	if status.Paired {
		t.Fatalf("fresh install must be unpaired: %s", body)
	}

	// 未配对就发指令：必须明确拒绝，不能静默丢指令。
	code, body = s.do(t, http.MethodPost, "/api/relay/phone/send", `{"text":"整理下载目录"}`)
	if code == http.StatusOK {
		t.Fatalf("send without pairing should fail, got 200: %s", body)
	}

	// 错误的配对码：400 + 人话原因。
	code, body = s.do(t, http.MethodPost, "/api/relay/phone/pair",
		`{"url":"http://127.0.0.1:1","code":"000000","phone_name":"测试机"}`)
	if code == http.StatusOK {
		t.Fatalf("pair to unreachable relay should fail: %s", body)
	}
}

// TestServedPageComesFromWebSPA 验证手机加载的是 web/ 的同一份页面
// （含通知中心与电脑助手面板），而不是旧的手机专用分叉页面。
func TestServedPageComesFromWebSPA(t *testing.T) {
	s := startForTest(t)
	resp, err := http.Get("http://" + s.addr + "/?token=" + s.token)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	for _, marker := range []string{"pc-panel", "inbox-panel", "vendor/vue.global.prod.js"} {
		if !strings.Contains(page, marker) {
			t.Fatalf("served page missing %q (webassets 未同步？)", marker)
		}
	}
}

// TestServesVendorRuntime 验证内嵌页面依赖的 Vue 运行时真的可取到，
// 否则手机 WebView 会白屏（页面在、脚本 404）。
func TestServesVendorRuntime(t *testing.T) {
	s := startForTest(t)
	resp, err := http.Get("http://" + s.addr + "/vendor/vue.global.prod.js?token=" + s.token)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("vendor status=%d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if len(body) < 10_000 {
		t.Fatalf("vendor payload too small: %d bytes", len(body))
	}
}

// TestVoiceTextOnPhone 验证手机端口令判定链路：唤醒词在 Go 判定，
// 片段由 Go 自己落库（手机不再单独调 /api/memory/capture）。
func TestVoiceTextOnPhone(t *testing.T) {
	s := startForTest(t)

	code, body := s.do(t, http.MethodPost, "/api/voice/text", `{"text":"小金小金，明天几点开会"}`)
	if code != http.StatusOK {
		t.Fatalf("status=%d body=%s", code, body)
	}
	var resp struct {
		Actions []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"actions"`
		NextFlushMs int64 `json:"next_flush_ms"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	if len(resp.Actions) != 1 || resp.Actions[0].Type != "chat" || resp.Actions[0].Text != "明天几点开会" {
		t.Fatalf("actions=%+v", resp.Actions)
	}
	if resp.NextFlushMs <= 0 {
		t.Fatalf("next_flush_ms=%d", resp.NextFlushMs)
	}

	// flush 收尾：片段必须真的进情景记忆（返回片段 id）。
	code, body = s.do(t, http.MethodPost, "/api/voice/text", `{"flush":true}`)
	if code != http.StatusOK {
		t.Fatalf("flush status=%d body=%s", code, body)
	}
	var flushed struct {
		Captured []string `json:"captured"`
	}
	if err := json.Unmarshal(body, &flushed); err != nil {
		t.Fatalf("flush json: %v (%s)", err, body)
	}
	if len(flushed.Captured) != 1 {
		t.Fatalf("captured=%v（片段应写入本机情景记忆）", flushed.Captured)
	}

	// 落库后可被检索到。
	code, body = s.do(t, http.MethodGet, "/api/memory/search?q="+url.QueryEscape("开会"), "")
	if code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", code, body)
	}
	if !strings.Contains(string(body), "开会") {
		t.Fatalf("检索不到刚记的片段: %s", body)
	}
}
