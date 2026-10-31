package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type harness struct {
	t      *testing.T
	hub    *Hub
	server *httptest.Server
	cfg    Config
	now    func() time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clock := time.Now()
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	now := func() time.Time {
		<-mu
		v := clock
		mu <- struct{}{}
		return v
	}
	advance := func(d time.Duration) {}
	_ = advance
	cfg := Config{Now: now}
	h := &harness{t: t, cfg: cfg}
	h.hub = NewHub(cfg)
	h.server = httptest.NewServer(NewServer(h.hub, cfg).Handler())
	t.Cleanup(h.server.Close)
	return h
}

// setClock 让测试推进虚拟时钟。
func (h *harness) setClock(at time.Time) {
	h.hub.cfg.Now = func() time.Time { return at }
}

func (h *harness) postJSON(path string, body any, headers map[string]string) (int, map[string]any) {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, h.server.URL+path, strings.NewReader(string(raw)))
	if err != nil {
		h.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("post %s: %v", path, err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (h *harness) register() (string, string) {
	h.t.Helper()
	status, out := h.postJSON("/v1/desktop/register", map[string]string{"name": "桌面"}, nil)
	if status != http.StatusOK {
		h.t.Fatalf("register status=%d body=%v", status, out)
	}
	return out["desktop_id"].(string), out["desktop_token"].(string)
}

func (h *harness) pairCode(desktopID, token string) string {
	h.t.Helper()
	status, out := h.postJSON("/v1/pair/code", nil, map[string]string{
		"X-Desktop-Id": desktopID, "X-Desktop-Token": token,
	})
	if status != http.StatusOK {
		h.t.Fatalf("pair code status=%d body=%v", status, out)
	}
	return out["code"].(string)
}

func (h *harness) pair(code string) PairResult {
	h.t.Helper()
	status, out := h.postJSON("/v1/pair", map[string]string{"code": code, "phone_name": "我的手机"}, nil)
	if status != http.StatusOK {
		h.t.Fatalf("pair status=%d body=%v", status, out)
	}
	return PairResult{
		ChannelID: out["channel_id"].(string), PhoneToken: out["phone_token"].(string),
		DesktopID: out["desktop_id"].(string), DesktopName: out["desktop_name"].(string),
	}
}

type wsClient struct {
	conn *websocket.Conn
}

func (h *harness) dial(role, id, token string) *wsClient {
	h.t.Helper()
	u := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/v1/ws?role=" + role + "&id=" + id
	header := http.Header{}
	header.Set("X-Token", token)
	conn, _, err := websocket.DefaultDialer.Dial(u, header)
	if err != nil {
		h.t.Fatalf("dial %s: %v", role, err)
	}
	h.t.Cleanup(func() { _ = conn.Close() })
	return &wsClient{conn: conn}
}

func (c *wsClient) send(t *testing.T, env *Envelope) {
	t.Helper()
	raw, _ := json.Marshal(env)
	if err := c.conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (c *wsClient) read(t *testing.T) *Envelope {
	t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return &env
}

// readKind 跳过 hello/ack 之外的噪声帧，读取指定类型。
func (c *wsClient) readKind(t *testing.T, kind string) *Envelope {
	t.Helper()
	for i := 0; i < 10; i++ {
		env := c.read(t)
		if env.Kind == kind {
			return env
		}
	}
	t.Fatalf("no %s frame received", kind)
	return nil
}

func TestPhoneCommandReachesDesktopAndResultReturns(t *testing.T) {
	h := newHarness(t)
	desktopID, desktopToken := h.register()
	pr := h.pair(h.pairCode(desktopID, desktopToken))

	desktop := h.dial(RoleDesktop, desktopID, desktopToken)
	hello := desktop.readKind(t, KindHello)
	var helloPayload HelloPayload
	if err := json.Unmarshal(hello.Payload, &helloPayload); err != nil {
		t.Fatalf("hello payload: %v", err)
	}
	if helloPayload.Role != RoleDesktop || helloPayload.DesktopID != desktopID {
		t.Fatalf("desktop hello=%+v", helloPayload)
	}
	phone := h.dial(RolePhone, pr.ChannelID, pr.PhoneToken)
	phone.readKind(t, KindHello)

	phone.send(t, &Envelope{V: 1, ID: "cmd-1", Kind: KindCommand,
		Payload: json.RawMessage(`{"text":"打开浏览器"}`)})
	ack := phone.readKind(t, KindAck)
	if ack.ReplyTo != "cmd-1" {
		t.Fatalf("ack reply_to=%q", ack.ReplyTo)
	}

	cmd := desktop.readKind(t, KindCommand)
	if cmd.ID != "cmd-1" {
		t.Fatalf("desktop got id=%q", cmd.ID)
	}
	if cmd.Channel != pr.ChannelID {
		t.Fatalf("relay must stamp channel, got %q want %q", cmd.Channel, pr.ChannelID)
	}
	if cmd.From != "phone:"+pr.ChannelID || cmd.To != "desktop:"+desktopID {
		t.Fatalf("relay stamping wrong: from=%q to=%q", cmd.From, cmd.To)
	}
	if string(cmd.Payload) != `{"text":"打开浏览器"}` {
		t.Fatalf("payload changed: %s", cmd.Payload)
	}

	desktop.send(t, &Envelope{V: 1, ID: "res-1", Kind: KindResult, ReplyTo: "cmd-1", Channel: pr.ChannelID,
		Payload: json.RawMessage(`{"ok":true,"reply":"已打开"}`)})
	res := phone.readKind(t, KindResult)
	if res.ReplyTo != "cmd-1" || string(res.Payload) != `{"ok":true,"reply":"已打开"}` {
		t.Fatalf("phone result=%+v payload=%s", res, res.Payload)
	}
}

func TestClientCannotSpoofChannelOrSender(t *testing.T) {
	h := newHarness(t)
	desktopID, desktopToken := h.register()
	pr := h.pair(h.pairCode(desktopID, desktopToken))
	desktop := h.dial(RoleDesktop, desktopID, desktopToken)
	desktop.readKind(t, KindHello)
	phone := h.dial(RolePhone, pr.ChannelID, pr.PhoneToken)
	phone.readKind(t, KindHello)

	phone.send(t, &Envelope{ID: "cmd-x", Kind: KindCommand,
		Channel: "attacker-channel", From: "desktop:evil", To: "phone:evil",
		Payload: json.RawMessage(`{"text":"hi"}`)})
	cmd := desktop.readKind(t, KindCommand)
	if cmd.Channel == "attacker-channel" || strings.HasPrefix(cmd.From, "desktop:evil") {
		t.Fatalf("spoofed fields survived: %+v", cmd)
	}
}

func TestOfflineQueueDeliversInOrderAndAcks(t *testing.T) {
	h := newHarness(t)
	desktopID, desktopToken := h.register()
	pr := h.pair(h.pairCode(desktopID, desktopToken))
	phone := h.dial(RolePhone, pr.ChannelID, pr.PhoneToken)
	phone.readKind(t, KindHello)

	for _, id := range []string{"cmd-a", "cmd-b"} {
		phone.send(t, &Envelope{ID: id, Kind: KindCommand, Payload: json.RawMessage(`{"text":"` + id + `"}`)})
		ack := phone.readKind(t, KindAck)
		var payload AckPayload
		if err := json.Unmarshal(ack.Payload, &payload); err != nil {
			t.Fatalf("ack payload: %v", err)
		}
		if payload.Status != "accepted" || !payload.Queued {
			t.Fatalf("expected queued ack, got %+v", payload)
		}
	}

	desktop := h.dial(RoleDesktop, desktopID, desktopToken)
	desktop.readKind(t, KindHello)
	if first := desktop.readKind(t, KindCommand); first.ID != "cmd-a" {
		t.Fatalf("first queued=%q", first.ID)
	}
	if second := desktop.readKind(t, KindCommand); second.ID != "cmd-b" {
		t.Fatalf("second queued=%q", second.ID)
	}
	if stats := h.hub.Stats(); stats.Queued != 0 {
		t.Fatalf("queue not drained: %+v", stats)
	}
}

func TestDuplicateCommandExecutedOnce(t *testing.T) {
	h := newHarness(t)
	desktopID, desktopToken := h.register()
	pr := h.pair(h.pairCode(desktopID, desktopToken))
	desktop := h.dial(RoleDesktop, desktopID, desktopToken)
	desktop.readKind(t, KindHello)
	phone := h.dial(RolePhone, pr.ChannelID, pr.PhoneToken)
	phone.readKind(t, KindHello)

	env := &Envelope{ID: "cmd-dup", Kind: KindCommand, Payload: json.RawMessage(`{"text":"x"}`)}
	phone.send(t, env)
	if ack := phone.readKind(t, KindAck); !strings.Contains(string(ack.Payload), "accepted") {
		t.Fatalf("first ack=%s", ack.Payload)
	}
	phone.send(t, env)
	var dup AckPayload
	for {
		ack := phone.readKind(t, KindAck)
		_ = json.Unmarshal(ack.Payload, &dup)
		if dup.Status == "duplicate" {
			break
		}
	}
	desktop.readKind(t, KindCommand)
	// 再读一帧不应再有 command；用短超时断言没有第二条。
	_ = desktop.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		_, data, err := desktop.conn.ReadMessage()
		if err != nil {
			break
		}
		var env Envelope
		_ = json.Unmarshal(data, &env)
		if env.Kind == KindCommand {
			t.Fatalf("duplicate command delivered: %+v", env)
		}
	}
	if stats := h.hub.Stats(); stats.Duplicates == 0 {
		t.Fatalf("duplicate counter not raised: %+v", stats)
	}
}

func TestWSUnauthorized(t *testing.T) {
	h := newHarness(t)
	desktopID, _ := h.register()
	u := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/v1/ws?role=desktop&id=" + desktopID + "&token=wrong"
	_, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err == nil {
		t.Fatal("expected handshake failure")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%v err=%v", resp, err)
	}
}

func TestPairCodeSingleUseAndExpiry(t *testing.T) {
	h := newHarness(t)
	desktopID, desktopToken := h.register()
	code := h.pairCode(desktopID, desktopToken)
	h.pair(code)
	if status, _ := h.postJSON("/v1/pair", map[string]string{"code": code}, nil); status != http.StatusNotFound {
		t.Fatalf("reused code status=%d", status)
	}

	code2 := h.pairCode(desktopID, desktopToken)
	h.setClock(time.Now().Add(6 * time.Minute))
	if status, _ := h.postJSON("/v1/pair", map[string]string{"code": code2}, nil); status != http.StatusGone {
		t.Fatalf("expired code status=%d", status)
	}
}

func TestDesktopCannotReplyToForeignChannel(t *testing.T) {
	h := newHarness(t)
	desktopID, desktopToken := h.register()
	_ = h.pair(h.pairCode(desktopID, desktopToken)) // 自己已配对的通道
	otherID, otherToken := h.register()
	otherPR := h.pair(h.pairCode(otherID, otherToken))

	desktop := h.dial(RoleDesktop, desktopID, desktopToken)
	desktop.readKind(t, KindHello)
	desktop.send(t, &Envelope{ID: "res-x", Kind: KindResult, ReplyTo: "cmd-y", Channel: otherPR.ChannelID,
		Payload: json.RawMessage(`{"ok":true}`)})
	errEnv := desktop.readKind(t, KindError)
	if !strings.Contains(string(errEnv.Payload), "unauthorized") {
		t.Fatalf("expected unauthorized, got %s", errEnv.Payload)
	}
}

func TestPairRateLimited(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 10; i++ {
		if status, _ := h.postJSON("/v1/pair", map[string]string{"code": "000000"}, nil); status == http.StatusTooManyRequests {
			t.Fatalf("rate limited too early at attempt %d", i)
		}
	}
	if status, _ := h.postJSON("/v1/pair", map[string]string{"code": "000000"}, nil); status != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after burst, got %d", status)
	}
}

func TestRevokeChannelDropsPhone(t *testing.T) {
	h := newHarness(t)
	desktopID, desktopToken := h.register()
	pr := h.pair(h.pairCode(desktopID, desktopToken))
	phone := h.dial(RolePhone, pr.ChannelID, pr.PhoneToken)
	phone.readKind(t, KindHello)

	req, _ := http.NewRequest(http.MethodDelete, h.server.URL+"/v1/channels/"+pr.ChannelID, nil)
	req.Header.Set("X-Desktop-Id", desktopID)
	req.Header.Set("X-Desktop-Token", desktopToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d", resp.StatusCode)
	}
	u := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/v1/ws?role=phone&id=" + pr.ChannelID + "&token=" + pr.PhoneToken
	if _, _, err := websocket.DefaultDialer.Dial(u, nil); err == nil {
		t.Fatal("revoked channel should not authenticate")
	}
}

func TestClientExecutesAndReplies(t *testing.T) {
	h := newHarness(t)
	desktopID, desktopToken := h.register()
	pr := h.pair(h.pairCode(desktopID, desktopToken))

	executed := make(chan string, 4)
	client := &Client{
		BaseURL: h.server.URL, DesktopID: desktopID, Token: desktopToken,
		Handle: func(ctx context.Context, payload json.RawMessage) (ResultPayload, error) {
			var p struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(payload, &p)
			executed <- p.Text
			return ResultPayload{OK: true, Reply: "收到:" + p.Text}, nil
		},
		Logger: func(string, ...any) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	var phone *wsClient
	for time.Now().Before(deadline) {
		if h.hub.Stats().Online[RoleDesktop] > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	phone = h.dial(RolePhone, pr.ChannelID, pr.PhoneToken)
	phone.readKind(t, KindHello)

	phone.send(t, &Envelope{ID: "cmd-live", Kind: KindCommand, Payload: json.RawMessage(`{"text":"列出待办"}`)})
	res := phone.readKind(t, KindResult)
	var payload ResultPayload
	if err := json.Unmarshal(res.Payload, &payload); err != nil {
		t.Fatalf("result payload: %v", err)
	}
	if res.ReplyTo != "cmd-live" || !payload.OK || payload.Reply != "收到:列出待办" {
		t.Fatalf("result=%+v payload=%+v", res, payload)
	}
	if got := <-executed; got != "列出待办" {
		t.Fatalf("executed text=%q", got)
	}
}

func TestHealthzReportsStats(t *testing.T) {
	h := newHarness(t)
	h.register()
	resp, err := http.Get(h.server.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool  `json:"ok"`
		Stats Stats `json:"stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.Stats.Desktops != 1 {
		t.Fatalf("healthz=%+v", out)
	}
}
