package superagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/worktask"
)

// Coordinator 是所有用户输入的低成本前置分流器。只有明确的 /task 或
// “暂停任务/恢复任务/取消任务/新建任务”指令才进入 WorkTask；其余文本
// 走原来的主会话快速路径，不额外调用模型。
type Coordinator struct {
	Tasks *worktask.Service
}

type CoordinatorResult struct {
	Reply  string `json:"reply"`
	TaskID string `json:"task_id,omitempty"`
	Action string `json:"action"`
}

func (c *Coordinator) Handle(ctx context.Context, ownerID, text string) (*CoordinatorResult, bool, error) {
	if c == nil || c.Tasks == nil {
		return nil, false, nil
	}
	raw := strings.TrimSpace(text)
	if raw == "" {
		return nil, false, nil
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "/task ") {
		return c.handleSlash(ctx, ownerID, strings.TrimSpace(raw[len("/task "):]))
	}
	for _, prefix := range []string{"新建工作任务：", "新建工作任务:", "新建任务：", "新建任务:"} {
		if strings.HasPrefix(raw, prefix) {
			goal := strings.TrimSpace(strings.TrimPrefix(raw, prefix))
			return c.create(ctx, ownerID, goal)
		}
	}
	for _, item := range []struct {
		prefix string
		action string
	}{
		{"暂停任务", "pause"}, {"恢复任务", "resume"}, {"取消任务", "cancel"},
	} {
		if strings.HasPrefix(raw, item.prefix) {
			rest := strings.TrimSpace(strings.TrimPrefix(raw, item.prefix))
			rest = strings.TrimSpace(strings.TrimLeft(rest, "：:# "))
			if rest == "" {
				return &CoordinatorResult{Reply: "请在指令后附任务 ID，例如：" + item.prefix + " <id>"}, true, nil
			}
			return c.control(ctx, ownerID, item.action, rest)
		}
	}
	return nil, false, nil
}

func (c *Coordinator) handleSlash(ctx context.Context, ownerID, rest string) (*CoordinatorResult, bool, error) {
	parts := strings.SplitN(rest, " ", 2)
	action := strings.ToLower(strings.TrimSpace(parts[0]))
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	switch action {
	case "new":
		return c.create(ctx, ownerID, arg)
	case "list":
		tasks, err := c.Tasks.List(ctx, ownerID, "", 20)
		if err != nil {
			return nil, true, err
		}
		if len(tasks) == 0 {
			return &CoordinatorResult{Reply: "当前没有长期任务。", Action: "list"}, true, nil
		}
		var b strings.Builder
		b.WriteString("当前任务：\n")
		for _, task := range tasks {
			fmt.Fprintf(&b, "- %s · %s · %s\n", task.Title, task.Status, task.ID)
		}
		return &CoordinatorResult{Reply: strings.TrimSpace(b.String()), Action: "list"}, true, nil
	case "show":
		id, err := c.resolveTaskID(ctx, ownerID, arg)
		if err != nil {
			return nil, true, err
		}
		detail, err := c.Tasks.Get(ctx, ownerID, id)
		if err != nil {
			return nil, true, err
		}
		return &CoordinatorResult{Reply: taskSummary(detail), TaskID: id, Action: "show"}, true, nil
	case "msg", "steer":
		fields := strings.SplitN(arg, " ", 2)
		if len(fields) < 2 {
			return &CoordinatorResult{Reply: "用法：/task msg <id> <内容>"}, true, nil
		}
		id, err := c.resolveTaskID(ctx, ownerID, fields[0])
		if err != nil {
			return nil, true, err
		}
		result, err := c.Tasks.AddMessage(ctx, ownerID, id, fields[1], nil,
			"coordinator:"+uuid.NewString())
		if err != nil {
			return nil, true, err
		}
		return &CoordinatorResult{
			Reply: "已记录补充，执行段：" + firstText(jobID(result.Job), "当前运行中") +
				"；状态：" + result.Delivered,
			TaskID: id, Action: "message",
		}, true, nil
	case "pause", "resume", "cancel":
		return c.control(ctx, ownerID, action, arg)
	default:
		return &CoordinatorResult{
			Reply:  "任务指令：/task new|list|show|msg|pause|resume|cancel <id> [内容]",
			Action: "help",
		}, true, nil
	}
}

func (c *Coordinator) control(ctx context.Context, ownerID, action, rawID string) (*CoordinatorResult, bool, error) {
	id, err := c.resolveTaskID(ctx, ownerID, rawID)
	if err != nil {
		return nil, true, err
	}
	switch action {
	case "pause":
		task, err := c.Tasks.Pause(ctx, ownerID, id, -1)
		if err != nil {
			return nil, true, err
		}
		return &CoordinatorResult{Reply: "已暂停任务「" + task.Title + "」。", TaskID: id, Action: "pause"}, true, nil
	case "resume":
		result, err := c.Tasks.Resume(ctx, ownerID, id, -1)
		if err != nil {
			return nil, true, err
		}
		return &CoordinatorResult{
			Reply:  "已恢复任务「" + result.Task.Title + "」，新执行段已排队。",
			TaskID: id, Action: "resume",
		}, true, nil
	case "cancel":
		task, err := c.Tasks.Cancel(ctx, ownerID, id, -1)
		if err != nil {
			return nil, true, err
		}
		return &CoordinatorResult{Reply: "已取消任务「" + task.Title + "」。", TaskID: id, Action: "cancel"}, true, nil
	default:
		return nil, true, fmt.Errorf("未知任务指令")
	}
}

func (c *Coordinator) create(ctx context.Context, ownerID, goal string) (*CoordinatorResult, bool, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return &CoordinatorResult{Reply: "请说明任务目标，例如：新建任务：整理本周邮件"}, true, nil
	}
	title := goal
	if runes := []rune(title); len(runes) > 40 {
		title = string(runes[:40])
	}
	task, err := c.Tasks.Create(ctx, worktask.CreateInput{
		OwnerID: ownerID, Title: title, Goal: goal, Profile: store.WorkTaskProfileRead,
		IdempotencyKey: "coordinator:" + uuid.NewString(), Start: true, CreatedBy: "coordinator",
	})
	if err != nil {
		return nil, true, err
	}
	return &CoordinatorResult{
		Reply:  "已创建任务「" + task.Title + "」，ID：" + task.ID + "。",
		TaskID: task.ID, Action: "create",
	}, true, nil
}

func (c *Coordinator) resolveTaskID(ctx context.Context, ownerID, raw string) (string, error) {
	raw = strings.TrimSpace(strings.TrimLeft(raw, "#"))
	if raw == "" {
		return "", fmt.Errorf("缺少任务 ID")
	}
	if task, err := c.Tasks.Get(ctx, ownerID, raw); err == nil {
		return task.Task.ID, nil
	}
	tasks, err := c.Tasks.List(ctx, ownerID, "", 100)
	if err != nil {
		return "", err
	}
	matches := make([]string, 0, 1)
	for _, task := range tasks {
		if strings.HasPrefix(task.ID, raw) {
			matches = append(matches, task.ID)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("任务 ID 前缀不唯一，请输入更长的 ID")
	}
	return "", fmt.Errorf("任务不存在")
}

func taskSummary(detail *worktask.Detail) string {
	if detail == nil || detail.Task == nil {
		return "任务不存在"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n状态：%s · 版本 %d · Profile %s\n", detail.Task.Title, detail.Task.Status, detail.Task.Version, detail.Task.Profile)
	if detail.Live != nil {
		if detail.Live.Stage != "" {
			fmt.Fprintf(&b, "阶段：%s\n", detail.Live.Stage)
		}
		if detail.Live.NextAction != "" {
			fmt.Fprintf(&b, "下一步：%s\n", detail.Live.NextAction)
		}
		if detail.Live.WaitingKind != store.WaitingNone && detail.Live.WaitingKind != "" {
			fmt.Fprintf(&b, "等待：%s\n", detail.Live.WaitingKind)
		}
	}
	fmt.Fprintf(&b, "ID：%s", detail.Task.ID)
	return b.String()
}

func jobID(job *store.AgentJob) string {
	if job == nil {
		return ""
	}
	return job.ID
}
