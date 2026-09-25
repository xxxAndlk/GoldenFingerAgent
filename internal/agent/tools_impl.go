package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"goldenfinger/agent/internal/intent"
	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/nlu/timecn"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
)

// ---- shared helpers ----

func toolSpec(name, desc string, props map[string]any, required ...string) llm.ToolSpec {
	params := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		params["required"] = required
	}
	raw, _ := json.Marshal(params)
	return llm.ToolSpec{Name: name, Description: desc, Parameters: raw}
}

func decodeArgs(args json.RawMessage, v any) error {
	if len(args) == 0 {
		return fmt.Errorf("empty arguments")
	}
	return json.Unmarshal(args, v)
}

// parseTime runs the rule parser first, LLM fallback second (hard-validated).
func parseTime(tc *ToolContext, rawExpr string, now time.Time, loc *time.Location) (timecn.TimeResult, bool) {
	if res, ok := timecn.Parse(rawExpr, now, loc); ok {
		return res, true
	}
	rt := tc.Runtime
	if rt == nil || rt.LLM == nil {
		return timecn.TimeResult{}, false
	}
	return nlu.LLMTimeFallback(context.Background(), rt.LLM, rt.Model, rawExpr, now, loc)
}

func userOf(tc *ToolContext) (*store.User, error) {
	if tc.Runtime == nil || tc.Runtime.Tools == nil {
		return nil, fmt.Errorf("runtime not wired")
	}
	return tc.Runtime.Tools.Repos.Users.Get(context.Background(), tc.Session.UserID)
}

// ---- get_datetime ----

type getDateTimeTool struct{}

func (getDateTimeTool) Spec() llm.ToolSpec {
	return toolSpec("get_datetime", "获取当前日期和时间。当用户问今天几号、现在几点，或需要确认日期时使用。", map[string]any{})
}

func (getDateTimeTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	loc := loadLocation(u.TZ)
	now := tc.Runtime.Tools.Now().In(loc)
	return ToolResult{Status: StatusOK, Data: map[string]string{
		"now":     now.Format("2006-01-02 15:04:05"),
		"weekday": now.Format("Monday"),
		"tz":      u.TZ,
	}}, nil
}

// ---- create_task ----

type createTaskArgs struct {
	RawText     string  `json:"raw_text"`
	KindHint    string  `json:"kind_hint"` // intent|fact|alarm|note
	Title       string  `json:"title"`
	TimeExprRaw string  `json:"time_expr_raw"`
	PersonName  string  `json:"person_name"`
	Confidence  float64 `json:"self_reported_confidence"` // model's own confidence 0~1
}

type createTaskTool struct{}

func (createTaskTool) Spec() llm.ToolSpec {
	return toolSpec("create_task", `把用户的话语变成一条待办/任务/备忘。kind_hint: intent=打算做但还没做（只提醒别忘了）; fact=已经发生/已订好（会安排行程类提醒）; note=随手记。time_expr_raw 用用户原话（如"后天"）。无法确定时间时也要调用，系统会自动追问。`,
		map[string]any{
			"raw_text":                 map[string]any{"type": "string", "description": "用户原话"},
			"kind_hint":                map[string]any{"type": "string", "enum": []string{"intent", "fact", "alarm", "note"}},
			"title":                    map[string]any{"type": "string", "description": "简短的事项标题"},
			"time_expr_raw":            map[string]any{"type": "string", "description": "用户说的时间原话，如\"后天\"\"明早7点\""},
			"person_name":              map[string]any{"type": "string", "description": "涉及的人名，可为空"},
			"self_reported_confidence": map[string]any{"type": "number", "description": "你对这次抽取的把握，0~1"},
		}, "raw_text", "kind_hint", "title")
}

func (createTaskTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a createTaskArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return createTaskFlow(ctx, tc, nlu.TaskPayload{
		RawText:     a.RawText,
		Kind:        a.KindHint,
		Title:       a.Title,
		TimeExprRaw: a.TimeExprRaw,
		PersonName:  a.PersonName,
		Confidence:  a.Confidence,
	}, nil)
}

// createTaskFlow is shared by create_task / set_alarm and the clarify resolver.
// Confidence gating lives here (in domain terms): ≥0.85 auto (undoable),
// 0.6–0.85 clarify, <0.6 discard.
func createTaskFlow(ctx context.Context, tc *ToolContext, p nlu.TaskPayload, sourceMsgID *string) (ToolResult, error) {
	svcs := tc.Runtime.Tools
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	now := svcs.Now()
	loc := loadLocation(u.TZ)

	in := nlu.ScoreInput{
		LLMSelfReported: p.Confidence,
		TimeRequired:    true,
	}
	if in.LLMSelfReported == 0 {
		in.LLMSelfReported = 0.75 // model didn't self-report; neutral baseline
	}

	// Person resolution.
	personID := ""
	if p.PersonName != "" {
		res, err := svcs.Memory.ResolvePerson(ctx, u.ID, p.PersonName)
		if err != nil {
			return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
		}
		switch {
		case res.Ambiguous:
			// Person clarify (per policy < person_clarify → ask).
			cands := make([]nlu.PersonCandidate, 0, len(res.Candidates))
			opts := make([]nlu.Option, 0, len(res.Candidates)+1)
			for _, c := range res.Candidates {
				cands = append(cands, nlu.PersonCandidate{ID: c.ID, Name: c.CanonicalName})
				opts = append(opts, nlu.Option{Label: c.CanonicalName, Ref: "person:" + c.ID})
			}
			opts = append(opts, nlu.Option{Label: "都不是", Ref: "none"})
			payload, _ := json.Marshal(nlu.PersonDisambigPayload{
				NextAction: "task_create", Task: &p, Candidates: cands,
			})
			pending := nlu.NewPending(nlu.PendingPersonDisambig, payload,
				fmt.Sprintf("你说的是哪位%s呢？", p.PersonName), opts, now)
			return ToolResult{
				Status: StatusClarify, Question: pending.Question, Pending: pending,
				Trace: fmt.Sprintf("create_task %q → person_disambig (%d 位候选)", p.Title, len(res.Candidates)),
			}, nil
		case res.NotFound:
			person, err := svcs.Memory.EnsurePerson(ctx, u.ID, p.PersonName, "")
			if err != nil {
				return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
			}
			personID = person.ID
		default:
			personID = res.Person.ID
			in.PersonResolved = true
		}
	}

	// Time normalization (rules → LLM fallback → clarify).
	var absTime *time.Time
	timeNote := "无时间"
	if p.TimeExprRaw != "" {
		tr, ok := parseTime(tc, p.TimeExprRaw, now, loc)
		if ok {
			at := tr.Abs
			absTime = &at
			in.TimeParsed = true
			timeNote = fmt.Sprintf("%q→%s[%s]", p.TimeExprRaw, tr.Abs.In(loc).Format("01-02 15:04"), tr.Method)
		} else {
			timeNote = fmt.Sprintf("%q→解析失败", p.TimeExprRaw)
		}
	}
	if absTime == nil {
		// Time missing/unparseable → clarify (frozen hypothesis).
		payload, _ := json.Marshal(nlu.TimeMissingPayload{Task: p})
		pending := nlu.NewPending(nlu.PendingTimeMissing, payload,
			"你想让我什么时候提醒你呢？", nil, now)
		return ToolResult{
			Status: StatusClarify, Question: pending.Question, Pending: pending,
			Trace: fmt.Sprintf("create_task %q → time_missing (%s)", p.Title, timeNote),
		}, nil
	}

	score := nlu.Score(in)
	action := nlu.GateTask(score, svcs.Th)
	switch action {
	case nlu.ActionDiscard:
		owner := u.ID
		_ = svcs.Guard.Audit(ctx, &owner, "agent", "task_discard", p.Title, map[string]any{"score": score})
		return ToolResult{
			Status: StatusDiscard,
			Data:   map[string]any{"note": "信息不够确定，未创建任务。可以换个说法再试。"},
			Trace:  fmt.Sprintf("create_task %q → discarded (score %.2f < %.2f, %s)", p.Title, score, svcs.Th.TaskClarify, timeNote),
		}, nil

	case nlu.ActionClarify:
		payload, _ := json.Marshal(nlu.TaskPayload{
			RawText: p.RawText, Kind: p.Kind, Title: p.Title,
			TimeExprRaw: p.TimeExprRaw, PersonName: p.PersonName, PersonID: personID,
			Confidence: score,
		})
		abs := absTime.Format("2006-01-02 15:04")
		q := fmt.Sprintf("要记下「%s」，时间是 %s，对吗？", p.Title, abs)
		pending := nlu.NewPending(nlu.PendingTaskCreate, payload, q, nil, now)
		return ToolResult{
			Status: StatusClarify, Question: q, Pending: pending,
			Trace: fmt.Sprintf("create_task %q → clarify (score %.2f, %s)", p.Title, score, timeNote),
		}, nil

	default: // ActionAutoCreate
		return scheduleTask(ctx, tc, u, p, personID, absTime, score, sourceMsgID, true)
	}
}

// scheduleTask persists + enqueues the task (auto path or confirmed path).
func scheduleTask(ctx context.Context, tc *ToolContext, u *store.User, p nlu.TaskPayload,
	personID string, absTime *time.Time, score float64, sourceMsgID *string, auto bool) (ToolResult, error) {
	svcs := tc.Runtime.Tools
	kind := p.Kind
	if kind == "" {
		kind = store.KindIntent
	}
	in := task.CreateInput{
		OwnerUserID: u.ID,
		Kind:        kind,
		Title:       p.Title,
		TimeExprRaw: p.TimeExprRaw,
		AbsTime:     absTime,
		Confidence:  score,
		SourceMsgID: sourceMsgID,
		AutoAccept:  auto,
	}
	if personID != "" {
		in.LinkedPersonID = &personID
	}
	if kind == store.KindFact {
		in.EventTemplate = inferEventTemplate(p.RawText + " " + p.Title)
	}
	t, err := svcs.Tasks.Create(ctx, in)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}

	abs := ""
	if t.AbsTime != nil {
		abs = t.AbsTime.In(loadLocation(u.TZ)).Format("1月2日 15:04")
	}
	reply := fmt.Sprintf("好的，记下了：%s", p.Title)
	if abs != "" {
		reply += "，到 " + abs + " 我提醒你。"
	}
	if auto {
		reply += "（说「撤销」就可以取消）"
	}
	return ToolResult{
		Status: StatusOK,
		Data: map[string]any{
			"task_id": t.ID, "status": t.Status, "abs_time": abs, "title": p.Title,
			"reply": reply,
		},
		Cards: []Card{{
			Type: "task", Title: p.Title, Body: abs, RefID: t.ID,
			Actions: []CardAction{{Label: "完成", Verb: "done"}, {Label: "推迟", Verb: "snooze"}, {Label: "取消", Verb: "cancel"}},
		}},
		Trace: fmt.Sprintf("create_task %q → %s (score %.2f, abs=%s)", p.Title, t.Status, score, abs),
	}, nil
}

// inferEventTemplate is a cheap keyword map (trip/appointment/medication).
func inferEventTemplate(text string) string {
	switch {
	case strings.Contains(text, "票") || strings.Contains(text, "飞机") || strings.Contains(text, "火车") || strings.Contains(text, "出发") || strings.Contains(text, "行程"):
		return "trip"
	case strings.Contains(text, "预约") || strings.Contains(text, "挂号") || strings.Contains(text, "面试"):
		return "appointment"
	case strings.Contains(text, "吃药") || strings.Contains(text, "服药") || strings.Contains(text, "用药"):
		return "medication"
	default:
		return ""
	}
}

// ---- set_alarm ----

type setAlarmArgs struct {
	TimeExprRaw string `json:"time_expr_raw"`
	Label       string `json:"label"`
}

type setAlarmTool struct{}

func (setAlarmTool) Spec() llm.ToolSpec {
	return toolSpec("set_alarm", `设置闹钟或倒计时。time_expr_raw 用用户原话（"20分钟后""明早7点"）。`,
		map[string]any{
			"time_expr_raw": map[string]any{"type": "string"},
			"label":         map[string]any{"type": "string", "description": "闹钟要做什么，如\"吃药\""},
		}, "time_expr_raw", "label")
}

func (setAlarmTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a setAlarmArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return createTaskFlow(ctx, tc, nlu.TaskPayload{
		RawText:     a.TimeExprRaw + " " + a.Label,
		Kind:        store.KindAlarm,
		Title:       a.Label,
		TimeExprRaw: a.TimeExprRaw,
		Confidence:  0.9,
	}, nil)
}

// ---- save_note ----

type saveNoteArgs struct {
	Text string `json:"text"`
	Tags string `json:"tags"`
}

type saveNoteTool struct{}

func (saveNoteTool) Spec() llm.ToolSpec {
	return toolSpec("save_note", "保存随手记/备忘（如WiFi密码、车位）。以后可以被回忆检索。",
		map[string]any{
			"text": map[string]any{"type": "string"},
			"tags": map[string]any{"type": "string"},
		}, "text")
}

func (saveNoteTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a saveNoteArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	svcs := tc.Runtime.Tools
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	e, err := svcs.Memory.SaveNote(ctx, u.ID, a.Text, "", nil)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return ToolResult{
		Status: StatusOK,
		Data:   map[string]any{"episode_id": e.ID, "reply": "记下了：" + a.Text},
		Cards:  []Card{{Type: "note", Title: "随手记", Body: a.Text, RefID: e.ID}},
	}, nil
}

// ---- save_fact ----

type saveFactArgs struct {
	PersonName string `json:"person_name"`
	FactType   string `json:"fact_type"`
	ValueText  string `json:"value_text"`
}

type saveFactTool struct{}

func (saveFactTool) Spec() llm.ToolSpec {
	return toolSpec("save_fact", "把关于某个人的事实记入人物画像（如：张阿姨的女儿在上海）。只记事实，不记评价。fact_type 用小写英文类别：family/contact/address/work/school/habit/food/other。",
		map[string]any{
			"person_name": map[string]any{"type": "string"},
			"fact_type":   map[string]any{"type": "string"},
			"value_text":  map[string]any{"type": "string"},
		}, "person_name", "fact_type", "value_text")
}

func (saveFactTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a saveFactArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	svcs := tc.Runtime.Tools
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}

	// Person disambiguation first (same flow as create_task).
	res, err := svcs.Memory.ResolvePerson(ctx, u.ID, a.PersonName)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	if res.Ambiguous {
		cands := make([]nlu.PersonCandidate, 0, len(res.Candidates))
		opts := make([]nlu.Option, 0, len(res.Candidates)+1)
		for _, c := range res.Candidates {
			cands = append(cands, nlu.PersonCandidate{ID: c.ID, Name: c.CanonicalName})
			opts = append(opts, nlu.Option{Label: c.CanonicalName, Ref: "person:" + c.ID})
		}
		opts = append(opts, nlu.Option{Label: "都不是", Ref: "none"})
		payload, _ := json.Marshal(nlu.PersonDisambigPayload{
			NextAction: "save_fact",
			Fact: &nlu.FactPayload{
				PersonName: a.PersonName, FactType: a.FactType, ValueText: a.ValueText,
				Evaluative: nlu.IsEvaluative(a.FactType, a.ValueText),
			},
			Candidates: cands,
		})
		pending := nlu.NewPending(nlu.PendingPersonDisambig, payload,
			fmt.Sprintf("你说的是哪位%s呢？", a.PersonName), opts, svcs.Now())
		return ToolResult{Status: StatusClarify, Question: pending.Question, Pending: pending}, nil
	}

	return saveFactFlow(ctx, tc, u, nlu.FactPayload{
		PersonName: a.PersonName, FactType: a.FactType, ValueText: a.ValueText,
		Evaluative: nlu.IsEvaluative(a.FactType, a.ValueText),
		Confidence: 0.85,
	}, "")
}

// saveFactFlow executes the governance pipeline and maps the outcome to a reply.
func saveFactFlow(ctx context.Context, tc *ToolContext, u *store.User, p nlu.FactPayload, personID string) (ToolResult, error) {
	svcs := tc.Runtime.Tools
	outcome, fact, err := svcs.Memory.SaveFact(ctx, memory.SaveFactInput{
		OwnerUserID: u.ID,
		Actor:       "agent",
		PersonID:    personID,
		PersonName:  p.PersonName,
		FactType:    p.FactType,
		ValueText:   p.ValueText,
		Confidence:  p.Confidence,
		IsChildUser: u.UserType == store.UserChild,
	})
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}

	switch outcome {
	case memory.OutcomeRejected:
		return ToolResult{
			Status: StatusOK,
			Data: map[string]any{
				"reply": "这个我就不记啦，我只记实实在在的事情。还有别的要记的吗？",
				"note":  "evaluative or child-blocked fact rejected",
			},
		}, nil
	case memory.OutcomeConflict:
		return ToolResult{
			Status: StatusOK,
			Data: map[string]any{
				"reply": fmt.Sprintf("我记得关于%s的%s和这个不一样，现在改成「%s」对吗？", p.PersonName, p.FactType, p.ValueText),
				"note":  "conflict with existing confirmed fact",
			},
		}, nil
	case memory.OutcomeInferred:
		return ToolResult{
			Status: StatusOK,
			Data: map[string]any{
				"reply":   fmt.Sprintf("我先记下来了：%s。对吗？确认后我就正式记入。", p.ValueText),
				"fact_id": fact.ID, "status": fact.Status,
			},
		}, nil
	default: // written / superseded
		reply := fmt.Sprintf("记住了：%s的%s。", p.PersonName, p.ValueText)
		if outcome == memory.OutcomeSuperseded {
			reply = fmt.Sprintf("好的，已经把%s的信息更新为：%s。", p.PersonName, p.ValueText)
		}
		return ToolResult{
			Status: StatusOK,
			Data:   map[string]any{"reply": reply, "fact_id": fact.ID, "status": fact.Status},
		}, nil
	}
}

// ---- query_person ----

type queryPersonArgs struct {
	Name string `json:"name"`
}

type queryPersonTool struct{}

func (queryPersonTool) Spec() llm.ToolSpec {
	return toolSpec("query_person", "查看某个人物画像：关于这个人的事实列表。",
		map[string]any{"name": map[string]any{"type": "string"}}, "name")
}

func (queryPersonTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a queryPersonArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	svcs := tc.Runtime.Tools
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	res, err := svcs.Memory.ResolvePerson(ctx, u.ID, a.Name)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	if res.NotFound {
		return ToolResult{Status: StatusOK, Data: map[string]any{"found": false}}, nil
	}
	if res.Ambiguous {
		opts := make([]nlu.Option, 0, len(res.Candidates))
		for _, c := range res.Candidates {
			opts = append(opts, nlu.Option{Label: c.CanonicalName, Ref: "person:" + c.ID})
		}
		payload, _ := json.Marshal(nlu.PersonDisambigPayload{
			NextAction: "query_person", Candidates: toCandidates(res.Candidates),
		})
		pending := nlu.NewPending(nlu.PendingPersonDisambig, payload,
			fmt.Sprintf("你说的是哪位%s呢？", a.Name), opts, svcs.Now())
		return ToolResult{Status: StatusClarify, Question: pending.Question, Pending: pending}, nil
	}
	view, err := svcs.Memory.GetPersonView(ctx, u.ID, res.Person.ID)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return ToolResult{Status: StatusOK, Data: view}, nil
}

func toCandidates(persons []store.Person) []nlu.PersonCandidate {
	out := make([]nlu.PersonCandidate, 0, len(persons))
	for _, p := range persons {
		out = append(out, nlu.PersonCandidate{ID: p.ID, Name: p.CanonicalName})
	}
	return out
}

// ---- search_memory ----

type searchMemoryArgs struct {
	Query string `json:"query"`
}

type searchMemoryTool struct{}

func (searchMemoryTool) Spec() llm.ToolSpec {
	return toolSpec("search_memory", "搜索记忆：用户提过的事实、备忘、随口记下的事。当用户问\"我车位在哪\"\"上次说过什么\"这类问题时使用。",
		map[string]any{"query": map[string]any{"type": "string"}}, "query")
}

func (searchMemoryTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a searchMemoryArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	svcs := tc.Runtime.Tools
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	snips, err := svcs.Memory.Search(ctx, u.ID, a.Query, 8)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{"results": snips}}, nil
}

// ---- forget_memory ----

type forgetArgs struct {
	PersonID   string `json:"person_id"`
	PersonName string `json:"person_name"`
	FactID     string `json:"fact_id"`
}

type forgetTool struct{}

func (forgetTool) Spec() llm.ToolSpec {
	return toolSpec("forget_memory", "遗忘记忆：删除某个人物及其全部信息，或删除一条事实。用户说\"忘了它\"\"删掉\"\"别记着了\"时使用。",
		map[string]any{
			"person_id":   map[string]any{"type": "string"},
			"person_name": map[string]any{"type": "string"},
			"fact_id":     map[string]any{"type": "string"},
		})
}

func (forgetTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a forgetArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	svcs := tc.Runtime.Tools
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	switch {
	case a.FactID != "":
		if err := svcs.Memory.ForgetFact(ctx, u.ID, a.FactID); err != nil {
			return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
		}
		return ToolResult{Status: StatusOK, Data: map[string]any{"reply": "删掉了，我不再记着这条。"}}, nil
	case a.PersonID != "":
		if err := svcs.Memory.ForgetPerson(ctx, u.ID, a.PersonID); err != nil {
			return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
		}
		return ToolResult{Status: StatusOK, Data: map[string]any{"reply": "好的，我把关于这个人的一切都忘掉了。"}}, nil
	case a.PersonName != "":
		res, err := svcs.Memory.ResolvePerson(ctx, u.ID, a.PersonName)
		if err != nil {
			return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
		}
		if res.Person == nil {
			return ToolResult{Status: StatusOK, Data: map[string]any{"reply": "我这边没有这个人的记录。"}}, nil
		}
		if err := svcs.Memory.ForgetPerson(ctx, u.ID, res.Person.ID); err != nil {
			return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
		}
		return ToolResult{Status: StatusOK, Data: map[string]any{"reply": "好的，我把关于这个人的一切都忘掉了。"}}, nil
	}
	return ToolResult{Status: StatusError, Data: map[string]string{"error": "person_id/person_name/fact_id 之一必填"}}, nil
}

// ---- update_task ----

type updateTaskArgs struct {
	TaskID     string `json:"task_id"`
	Action     string `json:"action"` // done|snooze|cancel
	SnoozeExpr string `json:"snooze_expr"`
}

type updateTaskTool struct{}

func (updateTaskTool) Spec() llm.ToolSpec {
	return toolSpec("update_task", "更新任务状态：action=done(完成)/cancel(取消)/snooze(推迟，给 snooze_expr 如\"半小时后\")。",
		map[string]any{
			"task_id":     map[string]any{"type": "string"},
			"action":      map[string]any{"type": "string", "enum": []string{"done", "cancel", "snooze"}},
			"snooze_expr": map[string]any{"type": "string"},
		}, "task_id", "action")
}

func (updateTaskTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a updateTaskArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	svcs := tc.Runtime.Tools
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	switch a.Action {
	case "done":
		err = svcs.Tasks.Done(ctx, u.ID, a.TaskID)
	case "cancel":
		err = svcs.Tasks.Cancel(ctx, u.ID, a.TaskID)
	case "snooze":
		var tr timecn.TimeResult
		var ok bool
		if a.SnoozeExpr != "" {
			tr, ok = parseTime(tc, a.SnoozeExpr, svcs.Now(), loadLocation(u.TZ))
		}
		if !ok {
			// Default snooze: 30 minutes.
			tr.Abs = svcs.Now().Add(30 * time.Minute)
		}
		_, err = svcs.Tasks.Snooze(ctx, u.ID, a.TaskID, tr.Abs)
	default:
		return ToolResult{Status: StatusError, Data: map[string]string{"error": "unknown action"}}, nil
	}
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	reply := map[string]string{"done": "好的，这条完成了。", "cancel": "好，取消了。", "snooze": "好，晚点再提醒你。"}[a.Action]
	return ToolResult{Status: StatusOK, Data: map[string]any{"reply": reply}}, nil
}

// ---- list_tasks ----

type listTasksArgs struct {
	Status string `json:"status"` // "" | pending | upcoming | done
}

type listTasksTool struct{}

func (listTasksTool) Spec() llm.ToolSpec {
	return toolSpec("list_tasks", "列出用户的任务/待办/提醒。用户问\"我今天要做什么\"\"有什么安排\"时使用。",
		map[string]any{"status": map[string]any{"type": "string", "enum": []string{"", "pending", "upcoming", "done"}}})
}

func (listTasksTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a listTasksArgs
	_ = decodeArgs(args, &a) // empty args ok
	svcs := tc.Runtime.Tools
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	var statuses []string
	switch a.Status {
	case "pending":
		statuses = []string{store.TaskPendingConfirm}
	case "upcoming":
		statuses = []string{store.TaskScheduled, store.TaskNotified, store.TaskSnoozed}
	case "done":
		statuses = []string{store.TaskDone}
	default:
		statuses = []string{store.TaskDraft, store.TaskPendingConfirm, store.TaskScheduled, store.TaskNotified, store.TaskSnoozed}
	}
	tasks, err := svcs.Tasks.List(ctx, u.ID, statuses, "")
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	type item struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Kind    string `json:"kind"`
		Status  string `json:"status"`
		AbsTime string `json:"abs_time,omitempty"`
	}
	items := make([]item, 0, len(tasks))
	for _, t := range tasks {
		it := item{ID: t.ID, Title: taskTitle(&t), Kind: t.Kind, Status: t.Status}
		if t.AbsTime != nil {
			it.AbsTime = t.AbsTime.In(loadLocation(u.TZ)).Format("2006-01-02 15:04")
		}
		items = append(items, it)
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{"tasks": items}}, nil
}

func taskTitle(t *store.Task) string {
	var s struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(t.Schema, &s); err == nil && s.Title != "" {
		return s.Title
	}
	return t.TimeExprRaw
}

// ---- create_intent / list_intents / cancel_intent ----
// Standing intents: event-conditioned reminders ("当……时提醒我"). Time-based
// reminders belong to create_task/set_alarm — not here.

type createIntentArgs struct {
	Description   string     `json:"description"`
	TriggerGroups [][]string `json:"trigger_groups"`
	MaxFires      int        `json:"max_fires"`
	CooldownHours int        `json:"cooldown_hours"`
	ExpiresInDays int        `json:"expires_in_days"`
}

type createIntentTool struct{}

func (createIntentTool) Spec() llm.ToolSpec {
	return toolSpec("create_intent", `创建"事件触发"的常备提醒：以后当某件事出现时提醒用户（如"张阿姨来电话时提醒我问她女儿"）。
trigger_groups 是"或"的条件组，组内所有词都出现在一句话里才触发：例如 [["张阿姨","来电话"],["张妈","电话"]]。
固定时间的提醒用 create_task 或 set_alarm，不要用这个。愿望/目标类（"这季度想…"）存进记忆即可。`,
		map[string]any{
			"description":     map[string]any{"type": "string", "description": "触发时要提醒用户什么，如\"问她女儿的情况\""},
			"trigger_groups":  map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "description": "条件组数组（或的关系），组内为且的关系"},
			"max_fires":       map[string]any{"type": "integer", "description": "最多触发几次，默认 3"},
			"cooldown_hours":  map[string]any{"type": "integer", "description": "触发后多久内不重复提醒，默认 24 小时"},
			"expires_in_days": map[string]any{"type": "integer", "description": "多少天后自动过期，默认 90"},
		}, "description", "trigger_groups")
}

func (createIntentTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a createIntentArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	svcs := tc.Runtime.Tools
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	it, err := svcs.Intents.Create(ctx, intent.CreateInput{
		OwnerUserID:   u.ID,
		Description:   a.Description,
		TriggerGroups: a.TriggerGroups,
		MaxFires:      a.MaxFires,
		Cooldown:      time.Duration(a.CooldownHours) * time.Hour,
		ExpiresIn:     time.Duration(a.ExpiresInDays) * 24 * time.Hour,
	})
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return ToolResult{
		Status: StatusOK,
		Data: map[string]any{
			"intent_id":  it.ID,
			"status":     it.Status,
			"expires_at": it.ExpiresAt.Format("2006-01-02"),
			"message":    "已记下：以后" + triggerPhrase(it.TriggerGroups) + "时，我会提醒你" + it.Description + "。",
		},
	}, nil
}

// triggerPhrase renders groups as 「A 且 B / C 且 D」 for the confirmation text.
func triggerPhrase(groups [][]string) string {
	parts := make([]string, 0, len(groups))
	for _, g := range groups {
		parts = append(parts, strings.Join(g, "且"))
	}
	return strings.Join(parts, "，或")
}

type listIntentsTool struct{}

func (listIntentsTool) Spec() llm.ToolSpec {
	return toolSpec("list_intents", "列出用户的常备提醒（\"当……时提醒我\"类）。用户问\"你都答应提醒我什么\"时使用。", map[string]any{})
}

func (listIntentsTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	svcs := tc.Runtime.Tools
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	items, err := svcs.Intents.List(ctx, u.ID)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	type item struct {
		ID          string     `json:"id"`
		Description string     `json:"description"`
		Triggers    [][]string `json:"trigger_groups"`
		Status      string     `json:"status"`
		FireCount   int        `json:"fire_count"`
		MaxFires    int        `json:"max_fires"`
	}
	out := make([]item, 0, len(items))
	for _, it := range items {
		out = append(out, item{ID: it.ID, Description: it.Description, Triggers: it.TriggerGroups,
			Status: it.Status, FireCount: it.FireCount, MaxFires: it.MaxFires})
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{"intents": out}}, nil
}

type cancelIntentArgs struct {
	IntentID string `json:"intent_id"`
}

type cancelIntentTool struct{}

func (cancelIntentTool) Spec() llm.ToolSpec {
	return toolSpec("cancel_intent", "取消一条常备提醒（用户明确说\"不用提醒了\"\"取消那个约定\"时）。取消必须显式，不要自行推断。",
		map[string]any{"intent_id": map[string]any{"type": "string"}}, "intent_id")
}

func (cancelIntentTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a cancelIntentArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	svcs := tc.Runtime.Tools
	u, err := userOf(tc)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	if err := svcs.Intents.Cancel(ctx, u.ID, a.IntentID); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return ToolResult{Status: StatusOK, Data: map[string]any{"message": "已取消这条常备提醒。"}}, nil
}

// ---- get_weather ----

type weatherArgs struct {
	City     string `json:"city"`
	DateExpr string `json:"date_expr"`
}

type weatherTool struct{}

func (weatherTool) Spec() llm.ToolSpec {
	return toolSpec("get_weather", "查询天气。city 为城市名（如\"北京\"）。",
		map[string]any{
			"city":      map[string]any{"type": "string"},
			"date_expr": map[string]any{"type": "string", "description": "可选：\"明天\"等"},
		}, "city")
}

func (weatherTool) Execute(ctx context.Context, args json.RawMessage, tc *ToolContext) (ToolResult, error) {
	var a weatherArgs
	if err := decodeArgs(args, &a); err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	svcs := tc.Runtime.Tools
	if a.DateExpr != "" {
		fc, err := svcs.Weather.Forecast(ctx, a.City, 3)
		if err != nil {
			return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
		}
		return ToolResult{Status: StatusOK, Data: map[string]any{"forecast": fc}}, nil
	}
	w, err := svcs.Weather.Now(ctx, a.City)
	if err != nil {
		return ToolResult{Status: StatusError, Data: map[string]string{"error": err.Error()}}, nil
	}
	return ToolResult{Status: StatusOK, Data: w}, nil
}

// ---- registry wiring ----

// DefaultTools returns the full MVP tool set.
func DefaultTools() []Tool {
	return []Tool{
		getDateTimeTool{},
		createTaskTool{},
		setAlarmTool{},
		saveNoteTool{},
		saveFactTool{},
		queryPersonTool{},
		searchMemoryTool{},
		forgetTool{},
		updateTaskTool{},
		listTasksTool{},
		createIntentTool{},
		listIntentsTool{},
		cancelIntentTool{},
		weatherTool{},
	}
}

var _ = strings.Contains // reserved for future keyword heuristics
