package superagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/store"
)

type MemorySearcher interface {
	Search(ctx context.Context, ownerID, query string, k int) ([]memory.Snippet, error)
}

type ContextReceipt struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	Version    string `json:"version,omitempty"`
	Injected   bool   `json:"injected"`
	Truncated  string `json:"truncated,omitempty"`
	Hash       string `json:"hash,omitempty"`
	Permission string `json:"permission"`
}

type ContextBundle struct {
	Blocks   []string         `json:"blocks"`
	Receipts []ContextReceipt `json:"receipts"`
}

type ContextBudget struct {
	Total    int
	Task     int
	Live     int
	Artifact int
	Memory   int
	Thread   int
	Policy   int
}

func DefaultContextBudget() ContextBudget {
	return ContextBudget{Total: 6000, Task: 1200, Live: 1000, Artifact: 800, Memory: 1500, Thread: 1500, Policy: 500}
}

type ContextBroker struct {
	Repos  *store.Repos
	Memory MemorySearcher
	Now    func() time.Time
	Budget ContextBudget
}

func NewContextBroker(repos *store.Repos, mem MemorySearcher) *ContextBroker {
	return &ContextBroker{Repos: repos, Memory: mem, Now: time.Now, Budget: DefaultContextBudget()}
}

func (b *ContextBroker) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// Build 生成 Worker 的任务级上下文包；超出预算时保留引用并记录截断原因。
func (b *ContextBroker) Build(ctx context.Context, ownerID, taskID, jobID string) (*ContextBundle, error) {
	if b.Repos == nil {
		return nil, fmt.Errorf("context broker: repos not configured")
	}
	task, err := b.Repos.WorkTasks.Get(ctx, ownerID, taskID)
	if err != nil {
		return nil, err
	}
	bundle := &ContextBundle{}
	add := func(title, body string, budget int, sourceType, sourceID, version, permission string) {
		if strings.TrimSpace(body) == "" {
			return
		}
		text := title + "\n" + body
		truncated := ""
		if budget > 0 && len([]rune(text)) > budget {
			text = string([]rune(text)[:budget]) + "\n…（已截断）"
			truncated = "budget"
		}
		bundle.Blocks = append(bundle.Blocks, text)
		sum := sha256.Sum256([]byte(text))
		bundle.Receipts = append(bundle.Receipts, ContextReceipt{
			SourceType: sourceType, SourceID: sourceID, Version: version,
			Injected: true, Truncated: truncated, Hash: hex.EncodeToString(sum[:]), Permission: "owner",
		})
	}

	taskBlock := fmt.Sprintf("标题：%s\n目标：%s\n完成标准：%s\nProfile：%s",
		task.Title, task.Goal, firstNonEmpty(task.CompletionCriteria, "未指定"), task.Profile)
	add("【任务目标】", taskBlock, b.Budget.Task, "work_task", task.ID, strconv.Itoa(task.Version), "owner")

	if live, err := b.Repos.WorkTaskLive.Get(ctx, ownerID, taskID); err == nil {
		liveBlock := fmt.Sprintf("阶段：%s\n摘要：%s\n下一步：%s\n等待：%s\n交接：%s",
			firstNonEmpty(live.Stage, "—"), firstNonEmpty(live.Summary, "—"),
			firstNonEmpty(live.NextAction, "—"), firstNonEmpty(live.WaitingKind, "none"),
			firstNonEmpty(live.HandoffNote, "—"))
		add("【实时状态】", liveBlock, b.Budget.Live, "task_live_state", taskID, strconv.Itoa(live.Version), "owner")
	}

	artifacts, _ := b.Repos.Artifacts.ListByTask(ctx, ownerID, taskID)
	if len(artifacts) > 0 {
		var artifactLines []string
		for i, artifact := range artifacts {
			if i >= 8 {
				break
			}
			line := fmt.Sprintf("- %s（%s，v%d，%s）", artifact.Title, artifact.Kind,
				artifact.CurrentVersion, artifact.Status)
			artifactLines = append(artifactLines, line)
		}
		add("【交付物索引】", strings.Join(artifactLines, "\n"), b.Budget.Artifact,
			"artifact_index", taskID, strconv.Itoa(len(artifacts)), "owner")
	}

	if b.Memory != nil {
		snippets, searchErr := b.Memory.Search(ctx, ownerID, task.Goal, 8)
		if searchErr == nil && len(snippets) > 0 {
			var lines []string
			for i, snippet := range snippets {
				if i >= 8 {
					break
				}
				mark := ""
				if snippet.Status == store.MemoryInferred || snippet.Status == store.MemoryDraft {
					mark = "（待确认）"
				} else if snippet.Status == store.MemoryConflicted {
					mark = "（与已确认事实冲突，需人工处理）"
				}
				lines = append(lines, fmt.Sprintf("- %s%s", snippet.Text, mark))
			}
			add("【按任务范围检索的长期记忆】", strings.Join(lines, "\n"), b.Budget.Memory,
				"memory_search", task.ID, "", "owner")
		}
	}

	links, _ := b.Repos.WorkTaskThreads.ListByTask(ctx, ownerID, taskID)
	if len(links) > 0 {
		var lines []string
		for i, link := range links {
			if i >= 5 {
				break
			}
			thread, getErr := b.Repos.Threads.Get(ctx, ownerID, link.ThreadID)
			if getErr != nil {
				continue
			}
			summary := firstNonEmpty(thread.Summary, thread.Title, "（暂无摘要）")
			lines = append(lines, fmt.Sprintf("- [%s] %s：%s", thread.Channel, thread.Title, summary))
			add("【来源 Thread 摘要】", fmt.Sprintf("%s：%s", thread.Title, summary), b.Budget.Thread/2,
				"agent_thread", thread.ID, strconv.FormatInt(thread.SummaryEventID, 10), "owner")
		}
		if len(lines) > 0 {
			// 为 Router/诊断保留一个合并视图，不重复注入模型（已按 Thread 分别加入）。
			_ = lines
		}
	}

	policies, _ := b.Repos.PersonalPolicies.ListActive(ctx, ownerID, "task", taskID, b.now())
	globalPolicies, _ := b.Repos.PersonalPolicies.ListActive(ctx, ownerID, "global", "", b.now())
	policies = append(policies, globalPolicies...)
	if len(policies) > 0 {
		var lines []string
		for _, policy := range policies {
			lines = append(lines, fmt.Sprintf("- [%s] %s：%s", policy.PolicyType, policy.Name, string(policy.Content)))
		}
		add("【生效中个人策略 / SOP】", strings.Join(lines, "\n"), b.Budget.Policy,
			"personal_policy", taskID, "", "owner")
	}

	if jobID != "" {
		// ContextReceipt 的来源版本由调用方随 Checkpoint 持久化。
	}
	return bundle, nil
}
