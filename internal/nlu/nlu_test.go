package nlu

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/llm/mock"
)

var th = Thresholds{
	TaskAuto:      0.85,
	TaskClarify:   0.6,
	PersonClarify: 0.8,
	FactConfirmed: 0.8,
}

func TestScoreBoundaries(t *testing.T) {
	// Pure self-report passes through.
	if s := Score(ScoreInput{LLMSelfReported: 0.9}); s != 0.9 {
		t.Errorf("pass-through score = %v", s)
	}
	// Time parse bonus.
	if s := Score(ScoreInput{LLMSelfReported: 0.82, TimeParsed: true}); s < 0.85 {
		t.Errorf("time bonus missing: %v", s)
	}
	// Missing required time penalty.
	if s := Score(ScoreInput{LLMSelfReported: 0.8, TimeRequired: true}); s != 0.65 {
		t.Errorf("time-missing score = %v", s)
	}
	// Person ambiguity penalty.
	if s := Score(ScoreInput{LLMSelfReported: 0.9, PersonAmbiguous: true}); s != 0.7 {
		t.Errorf("ambiguous score = %v", s)
	}
	// Evaluative forces zero.
	if s := Score(ScoreInput{LLMSelfReported: 0.99, Evaluative: true}); s != 0 {
		t.Errorf("evaluative must be 0, got %v", s)
	}
	// Clamped to [0,1].
	if s := Score(ScoreInput{LLMSelfReported: 1, TimeParsed: true, PersonResolved: true}); s != 1 {
		t.Errorf("clamp high = %v", s)
	}
}

func TestGateTask(t *testing.T) {
	cases := []struct {
		score float64
		want  TaskAction
	}{
		{0.90, ActionAutoCreate},
		{0.85, ActionAutoCreate}, // boundary inclusive
		{0.84, ActionClarify},
		{0.60, ActionClarify}, // boundary inclusive
		{0.59, ActionDiscard},
		{0.0, ActionDiscard},
	}
	for _, c := range cases {
		if got := GateTask(c.score, th); got != c.want {
			t.Errorf("GateTask(%v) = %v, want %v", c.score, got, c.want)
		}
	}
}

func TestIsEvaluative(t *testing.T) {
	if !IsEvaluative("personality", "脾气不好") {
		t.Error("evaluative content not flagged")
	}
	if IsEvaluative("contact", "电话13800000000") {
		t.Error("factual content wrongly flagged")
	}
	if !IsEvaluative("health", "有高血压") {
		t.Error("health judgment not flagged")
	}
}

func TestChildFactAllowed(t *testing.T) {
	if ChildFactAllowed("personality") {
		t.Error("children must not get evaluative fact types")
	}
	if !ChildFactAllowed("contact") {
		t.Error("factual types must be allowed for children")
	}
}

func TestClarifyMatchOptions(t *testing.T) {
	now := time.Now()
	p := NewPending(PendingPersonDisambig, json.RawMessage(`{}`), "你说的是哪位张阿姨？",
		[]Option{
			{Label: "张阿姨（邻居）", Ref: "person:a"},
			{Label: "张阿姨（同事）", Ref: "person:b"},
		}, now)

	if out, opt := Match(p, "张阿姨（邻居）"); out != MatchOption || opt.Ref != "person:a" {
		t.Errorf("exact label match failed: %v %v", out, opt)
	}
	if out, opt := Match(p, "2"); out != MatchOption || opt.Ref != "person:b" {
		t.Errorf("index match failed: %v %v", out, opt)
	}
	if out, opt := Match(p, "都不是"); out != MatchOption || opt.Ref != "none" {
		t.Errorf("none match failed: %v %v", out, opt)
	}
	if out, _ := Match(p, "明天天气怎么样"); out != MatchNone {
		t.Errorf("unrelated message must not match, got %v", out)
	}
}

func TestClarifyAffirmDeny(t *testing.T) {
	now := time.Now()
	p := NewPending(PendingTaskCreate, json.RawMessage(`{}`), "要记下后天去订票，对吗？", nil, now)

	if out, _ := Match(p, "对"); out != MatchAffirm {
		t.Errorf("affirm failed: %v", out)
	}
	if out, _ := Match(p, "不对"); out != MatchDeny {
		t.Errorf("deny failed: %v", out)
	}
	if out, _ := Match(p, "今天几号"); out != MatchNone {
		t.Errorf("unrelated must be none: %v", out)
	}
}

func TestPendingExpiry(t *testing.T) {
	now := time.Now()
	p := NewPending(PendingTaskCreate, json.RawMessage(`{}`), "?", nil, now)
	if p.Expired(now.Add(time.Minute)) {
		t.Error("must not expire within TTL")
	}
	if !p.Expired(now.Add(11 * time.Minute)) {
		t.Error("must expire past TTL")
	}
	var nilPending *PendingAction
	if !nilPending.Expired(now) {
		t.Error("nil pending counts as expired")
	}
}

func TestLLMTimeFallbackValidation(t *testing.T) {
	now := time.Date(2026, 3, 5, 10, 0, 0, 0, time.FixedZone("CST", 8*3600))
	loc := now.Location()

	// Valid answer accepted.
	client := mock.New(mock.TextResponse(`{"abs_time":"2026-03-06T07:00:00+08:00","recurrence":"","confidence":0.9}`))
	res, ok := LLMTimeFallback(context.Background(), client, "m", "明儿个早上", now, loc)
	if !ok || res.Method != "llm" || res.Abs.Hour() != 7 {
		t.Fatalf("valid fallback rejected: %+v ok=%v", res, ok)
	}

	// Past time rejected.
	client = mock.New(mock.TextResponse(`{"abs_time":"2026-03-01T07:00:00+08:00","recurrence":"","confidence":0.9}`))
	if _, ok := LLMTimeFallback(context.Background(), client, "m", "上周", now, loc); ok {
		t.Error("past time must be rejected")
	}

	// Bad RFC3339 rejected.
	client = mock.New(mock.TextResponse(`{"abs_time":"下周三","recurrence":"","confidence":0.9}`))
	if _, ok := LLMTimeFallback(context.Background(), client, "m", "下周三", now, loc); ok {
		t.Error("non-RFC3339 must be rejected")
	}

	// Bad recurrence rejected.
	client = mock.New(mock.TextResponse(`{"abs_time":"2026-03-06T07:00:00+08:00","recurrence":"whenever","confidence":0.9}`))
	if _, ok := LLMTimeFallback(context.Background(), client, "m", "随便", now, loc); ok {
		t.Error("bad recurrence must be rejected")
	}

	// Good recurrence accepted.
	client = mock.New(mock.TextResponse(`{"abs_time":"2026-03-06T07:00:00+08:00","recurrence":"daily@07:00","confidence":0.9}`))
	res, ok = LLMTimeFallback(context.Background(), client, "m", "每天早上吧", now, loc)
	if !ok || res.Recurrence != "daily@07:00" {
		t.Errorf("good recurrence rejected: %+v", res)
	}
}

var _ llm.Client = (*mock.Scripted)(nil)
