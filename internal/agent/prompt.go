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

// PromptBuilder assembles the system prompt: persona + memory block + turn hints.
// User profile is resolved from the session each turn.
type PromptBuilder struct {
	Memory MemoryContext // usually *memory.Service adapter
	UserFn func(ctx context.Context, userID string) UserContext
}

// MemoryContext builds the token-budgeted memory block.
type MemoryContext interface {
	BuildContext(ctx context.Context, ownerID, userType, tz string, budgetTokens int) (string, error)
}

// UserContext supplies the current user's profile for the prompt header.
type UserContext struct {
	UserID   string
	UserName string
	UserType string
	TZ       string
}

// Build renders the full system prompt for one turn.
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
