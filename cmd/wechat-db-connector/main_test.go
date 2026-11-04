package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"goldenfinger/agent/internal/wechatdb"
)

func message(id string, seq int64) wechatdb.Message {
	return wechatdb.Message{
		MessageID: id, SourceID: "wechat-db:acct:chat", SortSeq: seq,
		EventID: "evt-" + id, Text: "hi",
	}
}

func TestAdmitMessagesSkipsAckedAndWaiting(t *testing.T) {
	previous := []string{"m1"}
	pending := map[string]pendingMessage{
		"m3": {chatKey: "chat", message: message("m3", 3)},
	}
	messages := []wechatdb.Message{
		message("m1", 1), message("m2", 2), message("m3", 3), message("m4", 4),
	}
	fresh := admitMessages(previous, pending, 1, messages)
	// m1 已确认、m3 等待确认，只有 m2 和 m4 需要投递。
	if len(fresh) != 2 || fresh[0].MessageID != "m2" || fresh[1].MessageID != "m4" {
		t.Fatalf("fresh=%v", fresh)
	}
}

func TestWatermarkDoesNotSkipUnconfirmedMessage(t *testing.T) {
	pending := map[string]pendingMessage{
		"m2": {chatKey: "chat", message: message("m2", 2)},
	}
	// m3 已确认，m2 还没确认：高水位只能停在 1，重启后 m2 仍会被重新投递。
	if got := nextWatermark(3, pending, "chat"); got != 1 {
		t.Fatalf("watermark=%d", got)
	}
	delete(pending, "m2")
	if got := nextWatermark(3, pending, "chat"); got != 3 {
		t.Fatalf("watermark=%d", got)
	}
	other := map[string]pendingMessage{
		"x": {chatKey: "other", message: message("x", 9)},
	}
	if got := nextWatermark(3, other, "chat"); got != 3 {
		t.Fatalf("watermark=%d", got)
	}
}

func TestAcknowledgeAdvancesCheckpointOnlyAfterConfirm(t *testing.T) {
	dir := t.TempDir()
	statePath := dir + "/state.json"
	c := &connector{
		workDir: dir, statePath: statePath,
		state:   stateFile{Seen: map[string][]string{}, LastSeq: map[string]int64{}, Pending: map[string][]string{}},
		pending: map[string]pendingMessage{}, maxSeq: map[string]int64{},
		out: json.NewEncoder(&bytes.Buffer{}),
	}
	c.maxSeq["chat"] = 5
	c.emit("chat", message("m5", 5))
	c.persistState("chat")
	if got := c.state.LastSeq["chat"]; got != 4 {
		t.Fatalf("watermark before ack=%d", got)
	}
	reply := c.acknowledge(map[string]any{"message_id": "m5", "ok": true})
	if reply["known"] != true || reply["persisted"] != true {
		t.Fatalf("reply=%v", reply)
	}
	if got := c.state.LastSeq["chat"]; got != 5 {
		t.Fatalf("watermark after ack=%d", got)
	}
	if len(c.pending) != 0 {
		t.Fatalf("pending=%v", c.pending)
	}
}

func TestAcknowledgeFailureKeepsPendingAndCheckpoint(t *testing.T) {
	dir := t.TempDir()
	c := &connector{
		workDir: dir, statePath: dir + "/state.json",
		state:   stateFile{Seen: map[string][]string{}, LastSeq: map[string]int64{}, Pending: map[string][]string{}},
		pending: map[string]pendingMessage{}, maxSeq: map[string]int64{"chat": 5},
		out: json.NewEncoder(&bytes.Buffer{}),
	}
	c.emit("chat", message("m5", 5))
	c.persistState("chat")
	reply := c.acknowledge(map[string]any{"message_id": "m5", "ok": false, "discard": false})
	if reply["persisted"] != false {
		t.Fatalf("reply=%v", reply)
	}
	if _, waiting := c.pending["m5"]; !waiting {
		t.Fatal("落盘失败的消息必须保留等待重试")
	}
	if got := c.state.LastSeq["chat"]; got != 4 {
		t.Fatalf("落盘失败不能推进检查点，watermark=%d", got)
	}
	if len(c.state.Seen["chat"]) != 0 {
		t.Fatalf("落盘失败不能标记 Seen：%v", c.state.Seen)
	}
}

func TestAcknowledgeDiscardDoesNotMarkSeen(t *testing.T) {
	dir := t.TempDir()
	c := &connector{
		workDir: dir, statePath: dir + "/state.json",
		state:   stateFile{Seen: map[string][]string{}, LastSeq: map[string]int64{}, Pending: map[string][]string{}},
		pending: map[string]pendingMessage{}, maxSeq: map[string]int64{"chat": 5},
		out: json.NewEncoder(&bytes.Buffer{}),
	}
	c.emit("chat", message("m5", 5))
	c.persistState("chat")
	reply := c.acknowledge(map[string]any{"message_id": "m5", "ok": false, "discard": true})
	if reply["discarded"] != true {
		t.Fatalf("reply=%v", reply)
	}
	if len(c.pending) != 0 || len(c.state.Seen["chat"]) != 0 {
		t.Fatalf("discard 应删除 pending 且不写 Seen：pending=%v seen=%v", c.pending, c.state.Seen)
	}
	if got := c.state.LastSeq["chat"]; got != 5 {
		t.Fatalf("明确丢弃后可以推进检查点，watermark=%d", got)
	}
}

func TestConfigureDropsPendingOfRevokedChat(t *testing.T) {
	dir := t.TempDir()
	c := &connector{
		workDir: dir, statePath: dir + "/state.json",
		state:   stateFile{Seen: map[string][]string{}, LastSeq: map[string]int64{"chat": 4}, Pending: map[string][]string{}},
		pending: map[string]pendingMessage{}, maxSeq: map[string]int64{"chat": 5},
		out: json.NewEncoder(&bytes.Buffer{}),
	}
	c.emit("chat", message("m5", 5))
	reply := c.configure(map[string]any{"enabled": false, "chats": []any{}})
	if reply["chats"] != 0 {
		t.Fatalf("reply=%v", reply)
	}
	if len(c.pending) != 0 {
		t.Fatalf("pending=%v", c.pending)
	}
}

func TestLegacyCheckpointWithoutMessagesIsNotBaselined(t *testing.T) {
	c := &connector{state: stateFile{
		Seen: map[string][]string{"chat": nil}, LastSeq: map[string]int64{"chat": 0},
		Baselined: map[string]bool{},
	}}
	// 旧检查点里没有消息的会话：高水位为 0，不能当成已完成基线，
	// 否则启用瞬间会把整段历史当成新消息回灌。
	if c.isBaselined("chat", nil, true, 0, true) {
		t.Fatal("空会话不能被当成已完成基线")
	}
	c.state.Baselined["chat"] = true
	if !c.isBaselined("chat", nil, true, 0, true) {
		t.Fatal("显式基线标记必须生效")
	}
}

func TestProtocolAnswersRequestsWithRequestID(t *testing.T) {
	dir := t.TempDir()
	input := strings.Join([]string{
		`{"type":"configure","id":"req-1","enabled":false,"chats":[]}`,
		`{"type":"health","id":"req-2"}`,
		`{"type":"stop","id":"req-3"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := run(strings.NewReader(input), &out, []string{"--workdir", dir, "--state", dir + "/state.json"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 4 {
		t.Fatalf("lines=%v", lines)
	}
	seen := map[string]string{}
	for _, line := range lines {
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		if id, ok := message["request_id"].(string); ok {
			seen[id] = message["type"].(string)
		}
	}
	if seen["req-1"] != "configured" || seen["req-2"] != "health" || seen["req-3"] != "stopped" {
		t.Fatalf("seen=%v", seen)
	}
}

func TestStatePersistsPendingAndWatermark(t *testing.T) {
	dir := t.TempDir()
	statePath := dir + "/state.json"
	c := &connector{
		workDir: dir, statePath: statePath,
		state:   stateFile{Seen: map[string][]string{}, LastSeq: map[string]int64{}, Pending: map[string][]string{}},
		pending: map[string]pendingMessage{}, maxSeq: map[string]int64{"chat": 5},
		out: json.NewEncoder(&bytes.Buffer{}),
	}
	c.emit("chat", message("m5", 5))
	c.persistState("chat")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var saved stateFile
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	if saved.LastSeq["chat"] != 4 {
		t.Fatalf("last_seq=%v", saved.LastSeq)
	}
	if len(saved.Pending["chat"]) != 1 || saved.Pending["chat"][0] != "m5" {
		t.Fatalf("pending=%v", saved.Pending)
	}
}
