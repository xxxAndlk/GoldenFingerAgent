package agent

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"time"
)

//go:embed prompts/butler.md
var butlerPrompt string

// PromptBuilder 组装系统提示：人设 + 记忆块 + 轮次提示。
// 用户画像每轮从会话解析。
type PromptBuilder struct {
	Memory MemoryContext // 通常是 *memory.Service 适配器
	UserFn func(ctx context.Context, userID string) UserContext
}

// MemoryContext 构建受 token 预算约束的记忆块。
type MemoryContext interface {
	BuildContext(ctx context.Context, ownerID, userType, tz string, budgetTokens int) (string, error)
}

// UserContext 为提示头提供当前用户画像。
type UserContext struct {
	UserID   string
	UserName string
	UserType string
	TZ       string
}

// Build 渲染一轮对话的完整系统提示。
func (b *PromptBuilder) Build(ctx context.Context, s *Session, now time.Time) (string, error) {
	var sb strings.Builder
	sb.WriteString(butlerPrompt)

	user := UserContext{UserID: s.UserID, TZ: "Asia/Shanghai"}
	if b.UserFn != nil {
		user = b.UserFn(ctx, s.UserID)
	}

	fmt.Fprintf(&sb, "\n\n## 当前时间\n%s（%s）\n",
		now.In(loadLocation(user.TZ)).Format("2006-01-02 15:04 (周一)"), user.TZ)

	if b.Memory != nil {
		block, err := b.Memory.BuildContext(ctx, user.UserID, user.UserType, user.TZ, 1500)
		if err == nil && strings.TrimSpace(block) != "" {
			sb.WriteString("\n## 关于这位用户（你的记忆）\n")
			sb.WriteString(block)
		}
	}

	if s.Pending != nil {
		fmt.Fprintf(&sb, "\n## 正在澄清中\n问题: %s\n请优先完成这个澄清，不要开启新话题。\n", s.Pending.Question)
	}

	return sb.String(), nil
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
