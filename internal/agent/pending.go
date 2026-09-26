package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/nlu/timecn"
	"goldenfinger/agent/internal/store"
)

// resolvePendingImpl 根据用户回复完成一个冻结的澄清假设——
// 确定性完成，不经过第二次 LLM 调用（文档：低置信必反问 →
// 答复即解决）。done=false 表示放弃待办动作（新话题）。
func resolvePendingImpl(ctx context.Context, s *Session, userText string, rt *Runtime, now time.Time) (*TurnResult, bool) {
	p := s.Pending
	if p.Expired(now) {
		s.Pending = nil
		return &TurnResult{Reply: "刚才的事情先放一放吧。你还有什么需要我帮忙的吗？"}, true
	}

	outcome, opt := nlu.Match(p, userText)

	switch p.Type {
	case nlu.PendingTaskCreate:
		return resolveTaskConfirm(ctx, s, rt, p, outcome, now)
	case nlu.PendingPersonDisambig:
		return resolvePersonDisambig(ctx, s, rt, p, outcome, opt, now)
	case nlu.PendingFactConfirm:
		return resolveFactConfirm(ctx, s, rt, p, outcome)
	case nlu.PendingTimeMissing:
		return resolveTimeMissing(ctx, s, rt, p, userText, now)
	}

	// 未知的待办类型：丢弃并让常规路由处理该消息。
	s.Pending = nil
	return nil, false
}

// resolveTaskConfirm 处理 "要记下…对吗？" → 对 / 不对 / 无关。
func resolveTaskConfirm(ctx context.Context, s *Session, rt *Runtime, p *nlu.PendingAction, outcome nlu.MatchOutcome, now time.Time) (*TurnResult, bool) {
	switch outcome {
	case nlu.MatchDeny:
		s.Pending = nil
		return &TurnResult{Reply: "好的，那就不记了。还有别的事吗？"}, true
	case nlu.MatchAffirm:
		var tp nlu.TaskPayload
		if err := json.Unmarshal(p.Payload, &tp); err != nil {
			s.Pending = nil
			return &TurnResult{Reply: "好的。"}, true
		}
		s.Pending = nil
		u, err := userOf(&ToolContext{Session: s, Runtime: rt})
		if err != nil {
			return &TurnResult{Reply: "我这边有点卡住了，稍后再试好吗？"}, true
		}
		var absTime *time.Time
		if tp.TimeExprRaw != "" {
			if tr, ok := parseTime(&ToolContext{Session: s, Runtime: rt}, tp.TimeExprRaw, now, loadLocation(u.TZ)); ok {
				at := tr.Abs
				absTime = &at
			}
		}
		if absTime == nil {
				at := now.Add(time.Hour) // 冻结载荷曾有过时间；安全回退
			absTime = &at
		}
		res, err := scheduleTask(ctx, &ToolContext{Session: s, Runtime: rt}, u, tp, tp.PersonID, absTime, tp.Confidence, nil, true)
		if err != nil {
			return &TurnResult{Reply: "记的时候出了点问题，再说一次好吗？"}, true
		}
		return resultFromTool(res), true
	case nlu.MatchNone:
		// 不是答复——新话题：放弃待办任务（审计）并继续常规处理。
		s.Pending = nil
		return nil, false
	}
	return nil, false
}

// resolvePersonDisambig 处理 "哪位张阿姨？" → 选项选择。
func resolvePersonDisambig(ctx context.Context, s *Session, rt *Runtime, p *nlu.PendingAction, outcome nlu.MatchOutcome, opt *nlu.Option, now time.Time) (*TurnResult, bool) {
	if outcome != nlu.MatchOption {
		if outcome == nlu.MatchNone {
			s.Pending = nil
			return nil, false // 新话题
		}
		// 单纯的肯定/否定无法消歧——重新提问。
		return &TurnResult{Reply: p.Question}, true
	}

	var dp nlu.PersonDisambigPayload
	if err := json.Unmarshal(p.Payload, &dp); err != nil {
		s.Pending = nil
		return &TurnResult{Reply: "好的。"}, true
	}
	s.Pending = nil

	u, err := userOf(&ToolContext{Session: s, Runtime: rt})
	if err != nil {
		return &TurnResult{Reply: "我这边有点卡住了，稍后再试好吗？"}, true
	}

	if opt.Ref == "none" {
		return &TurnResult{Reply: "好的，那就先不记这个人。还有别的事吗？"}, true
	}
	personID := ""
	if len(opt.Ref) > 7 && opt.Ref[:7] == "person:" {
		personID = opt.Ref[7:]
	}

	switch dp.NextAction {
	case "task_create":
		if dp.Task == nil {
			return &TurnResult{Reply: "好的。"}, true
		}
		tp := *dp.Task
		tp.PersonID = personID
		var absTime *time.Time
		if tp.TimeExprRaw != "" {
			if tr, ok := parseTime(&ToolContext{Session: s, Runtime: rt}, tp.TimeExprRaw, now, loadLocation(u.TZ)); ok {
				at := tr.Abs
				absTime = &at
			}
		}
		if absTime == nil {
			payload, _ := json.Marshal(nlu.TimeMissingPayload{Task: tp})
			pending := nlu.NewPending(nlu.PendingTimeMissing, payload, "你想让我什么时候提醒你呢？", nil, now)
			s.Pending = pending
			return &TurnResult{Reply: pending.Question, Pending: pending}, true
		}
		res, err := scheduleTask(ctx, &ToolContext{Session: s, Runtime: rt}, u, tp, personID, absTime, tp.Confidence, nil, true)
		if err != nil {
			return &TurnResult{Reply: "记的时候出了点问题，再说一次好吗？"}, true
		}
		return resultFromTool(res), true

	case "save_fact":
		if dp.Fact == nil {
			return &TurnResult{Reply: "好的。"}, true
		}
		res, err := saveFactFlow(ctx, &ToolContext{Session: s, Runtime: rt}, u, *dp.Fact, personID)
		if err != nil {
			return &TurnResult{Reply: "记的时候出了点问题，再说一次好吗？"}, true
		}
		return resultFromTool(res), true

	case "query_person":
		view, err := rt.Tools.Memory.GetPersonView(ctx, u.ID, personID)
		if err != nil {
			return &TurnResult{Reply: "我这边查不到了，稍后再试好吗？"}, true
		}
		reply := fmt.Sprintf("关于%s，我记着这些：", view.Person.CanonicalName)
		for i, f := range view.Facts {
			if i >= 5 {
				break
			}
			reply += "\n· " + f.ValueText
		}
		if len(view.Facts) == 0 {
			reply = fmt.Sprintf("关于%s我还没记下什么呢。", view.Person.CanonicalName)
		}
		return &TurnResult{Reply: reply}, true
	}
	return &TurnResult{Reply: "好的。"}, true
}

// resolveFactConfirm 处理 "记住这个对吗？" → 对（写入确认）/ 不对（丢弃）。
func resolveFactConfirm(ctx context.Context, s *Session, rt *Runtime, p *nlu.PendingAction, outcome nlu.MatchOutcome) (*TurnResult, bool) {
	var fp nlu.FactPayload
	if err := json.Unmarshal(p.Payload, &fp); err != nil {
		s.Pending = nil
		return &TurnResult{Reply: "好的。"}, true
	}
	switch outcome {
	case nlu.MatchDeny:
		s.Pending = nil
		return &TurnResult{Reply: "好，那我就不记了。"}, true
	case nlu.MatchAffirm:
		s.Pending = nil
		u, err := userOf(&ToolContext{Session: s, Runtime: rt})
		if err != nil {
			return &TurnResult{Reply: "我这边有点卡住了，稍后再试好吗？"}, true
		}
		fp.Confidence = 0.95 // 用户已确认
		res, err := saveFactFlow(ctx, &ToolContext{Session: s, Runtime: rt}, u, fp, fp.PersonID)
		if err != nil {
			return &TurnResult{Reply: "记的时候出了点问题，再说一次好吗？"}, true
		}
		return resultFromTool(res), true
	default:
		s.Pending = nil
		return nil, false
	}
}

// resolveTimeMissing 处理 "什么时候提醒你呢？" → 把回复解析为时间。
func resolveTimeMissing(ctx context.Context, s *Session, rt *Runtime, p *nlu.PendingAction, userText string, now time.Time) (*TurnResult, bool) {
	var tm nlu.TimeMissingPayload
	if err := json.Unmarshal(p.Payload, &tm); err != nil {
		s.Pending = nil
		return nil, false
	}
	outcome, _ := nlu.Match(p, userText)
	if outcome == nlu.MatchDeny {
		s.Pending = nil
		return &TurnResult{Reply: "好，那就不记了。"}, true
	}

	u, err := userOf(&ToolContext{Session: s, Runtime: rt})
	if err != nil {
		return &TurnResult{Reply: "我这边有点卡住了，稍后再试好吗？"}, true
	}
	tr, ok := parseTime(&ToolContext{Session: s, Runtime: rt}, userText, now, loadLocation(u.TZ))
	if !ok {
		// 带候选重新问一次；保持待办存活但重置 TTL。
		p.ExpiresAt = now.Add(nlu.DefaultPendingTTL)
		return &TurnResult{Reply: "我还是没听明白时间。比如说「明天早上8点」或者「20分钟后」，要什么时候呢？"}, true
	}
	s.Pending = nil

	tp := tm.Task
	tp.TimeExprRaw = userText
	absTime := tr.Abs
	score := nlu.Score(nlu.ScoreInput{
		LLMSelfReported: tp.Confidence,
		TimeParsed:      true,
		TimeRequired:    true,
	})
	if tp.Confidence == 0 {
		score = nlu.Score(nlu.ScoreInput{LLMSelfReported: 0.75, TimeParsed: true, TimeRequired: true})
	}
	res, err := scheduleTask(ctx, &ToolContext{Session: s, Runtime: rt}, u, tp, tp.PersonID, &absTime, score, nil, true)
	if err != nil {
		return &TurnResult{Reply: "记的时候出了点问题，再说一次好吗？"}, true
	}
	return resultFromTool(res), true
}

// resultFromTool 把工具结果映射为轮次结果（回复文本取自 data.reply）。
func resultFromTool(res ToolResult) *TurnResult {
	reply := ""
	if res.Data != nil {
		if m, ok := res.Data.(map[string]any); ok {
			if r, ok := m["reply"].(string); ok {
				reply = r
			}
		}
	}
	if reply == "" {
		reply = "好的，办好了。"
	}
	return &TurnResult{Reply: reply, Cards: res.Cards, Trace: []string{res.Trace}}
}

var (
	_ = memory.FactLine
	_ = store.KindIntent
	_ = timecn.TimeResult{}
)
