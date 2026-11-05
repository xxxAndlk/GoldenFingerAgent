package agent

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"

	"goldenfinger/agent/internal/llm"
	"goldenfinger/agent/internal/skill"
)

//go:embed prompts/butler.md
var butlerPrompt string

// PromptBuilder 组装系统提示：人设 + 记忆块 + 轮次提示。
// 用户画像每轮从会话解析。
type PromptBuilder struct {
	Memory MemoryContext // 通常是 *memory.Service 适配器
	UserFn func(ctx context.Context, userID string) UserContext
	Skills *skill.Pool
}

// MemoryContext 构建受 token 预算约束的记忆块。
type MemoryContext interface {
	BuildContext(ctx context.Context, ownerID, userType, tz string, budgetTokens int) (string, error)
}

// QueryMemoryContext 能按当前问题检索相关长期记忆（可选能力）。
type QueryMemoryContext interface {
	BuildContextForQuery(ctx context.Context, ownerID, userType, tz, query string, budgetTokens int) (string, error)
}

// UserContext 为提示头提供当前用户画像。
type UserContext struct {
	UserID   string
	UserName string
	UserType string
	TZ       string
}

// Stable 返回跨轮次逐字节不变的系统前缀。时间、记忆和澄清状态不能放进
// 这里，否则 DeepSeek 从第 0 个 token 开始的前缀缓存会在系统消息处中断。
func (b *PromptBuilder) Stable() string { return butlerPrompt }

// BuildDynamic 构建只附加到本轮用户消息末尾的动态上下文，不写回会话历史。
// 查询取会话中最后一条用户消息，保证非对话循环调用也能按问题召回。
func (b *PromptBuilder) BuildDynamic(ctx context.Context, s *Session, now time.Time) (string, error) {
	return b.BuildDynamicFor(ctx, s, now, lastUserText(s))
}

// BuildDynamicFor 使用当前轮用户原话检索共享长期记忆。
func (b *PromptBuilder) BuildDynamicFor(ctx context.Context, s *Session, now time.Time, query string) (string, error) {
	var sb strings.Builder
	user := UserContext{UserID: s.UserID, TZ: "Asia/Shanghai"}
	if b.UserFn != nil {
		user = b.UserFn(ctx, s.UserID)
	}

	fmt.Fprintf(&sb, "\n\n## 当前时间\n%s（%s）\n",
		now.In(loadLocation(user.TZ)).Format("2006-01-02 15:04 (周一)"), user.TZ)

	if b.Memory != nil {
		var block string
		var err error
		if queryMemory, ok := b.Memory.(QueryMemoryContext); ok && strings.TrimSpace(query) != "" {
			block, err = queryMemory.BuildContextForQuery(ctx, user.UserID, user.UserType, user.TZ, query, 1500)
		} else {
			block, err = b.Memory.BuildContext(ctx, user.UserID, user.UserType, user.TZ, 1500)
		}
		if err == nil && strings.TrimSpace(block) != "" {
			sb.WriteString("\n## 关于这位用户（你的记忆）\n")
			sb.WriteString(block)
		}
	}

	if b.Skills != nil {
		sb.WriteString(b.Skills.MetadataBlock())
	}

	if s.Pending != nil {
		fmt.Fprintf(&sb, "\n## 正在澄清中\n问题: %s\n请优先完成这个澄清，不要开启新话题。\n", s.Pending.Question)
	}
	return sb.String(), nil
}

// Build 保留完整提示词契约供其他调用方使用；对话循环使用 Stable 与
// BuildDynamic 分离静态前缀和本轮动态内容，以提高缓存复用率。
func (b *PromptBuilder) Build(ctx context.Context, s *Session, now time.Time) (string, error) {
	dynamic, err := b.BuildDynamic(ctx, s, now)
	if err != nil {
		return "", err
	}
	return b.Stable() + dynamic, nil
}

func lastUserText(s *Session) string {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == llm.RoleUser {
			return s.Messages[i].Content
		}
	}
	return ""
}

func loadLocation(tz string) *time.Location {
	if tz == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.FixedZone(tz, 8*3600)
	}
	return loc
}
