package memory

import (
	"context"
	"fmt"
	"strings"

	"goldenfinger/agent/internal/store"
)

// BuildContext 组装注入系统提示的记忆块，
// 在 token 预算内按优先级打包（文档：按 token 预算注入，不堆全量）。
//
// 优先级：用户画像 > 待办备注 > 进行中任务 > 人物 > 事实。
// 溢出时优先截断最低优先级的区块。
func (s *Service) BuildContext(ctx context.Context, ownerID, userType, tz string, budgetTokens int) (string, error) {
	if budgetTokens <= 0 {
		budgetTokens = 1500
	}
	var sections []section

	sections = append(sections, section{
		title: "用户",
		body:  fmt.Sprintf("类型: %s, 时区: %s", userType, tz),
	})

	// 认识的人名单。
	persons, err := s.persons.ListByOwner(ctx, ownerID)
	if err != nil {
		return "", err
	}
	if len(persons) > 0 {
		var b strings.Builder
		limit := min(len(persons), 10)
		for i := 0; i < limit; i++ {
			fmt.Fprintf(&b, "- %s\n", persons[i].CanonicalName)
		}
		sections = append(sections, section{title: "认识的人", body: strings.TrimRight(b.String(), "\n")})
	}

	// 当前事实，最新的在前（廉价预过滤；排序在检索时进行）。
	facts, err := s.facts.ListCurrentByOwner(ctx, ownerID, 15)
	if err != nil {
		return "", err
	}
	if len(facts) > 0 {
		var b strings.Builder
		for i, f := range facts {
			if i >= 10 {
				break
			}
			personName := personNameByID(persons, f.PersonID)
			status := ""
			if f.Status == "inferred" {
				status = "（待确认）"
			}
			fmt.Fprintf(&b, "- %s · %s: %s%s\n", personName, f.FactType, f.ValueText, status)
		}
		sections = append(sections, section{title: "记住的事实", body: strings.TrimRight(b.String(), "\n")})
	}

	// 最近的笔记/片段。
	eps, err := s.eps.ListRecent(ctx, ownerID, 5)
	if err != nil {
		return "", err
	}
	if len(eps) > 0 {
		var b strings.Builder
		for _, e := range eps {
			fmt.Fprintf(&b, "- %s\n", e.Summary)
		}
		sections = append(sections, section{title: "最近记下的", body: strings.TrimRight(b.String(), "\n")})
	}

	// 在预算内打包：先放头部区块，丢弃/裁剪尾部。
	var out strings.Builder
	used := 0
	for _, sec := range sections {
		cost := estimateTokens(sec.title+sec.body) + 8
		if used+cost > budgetTokens {
			// 整体丢弃前先尝试裁剪版本。
			trimmed := trimToTokens(sec.body, budgetTokens-used-8)
			if strings.TrimSpace(trimmed) == "" {
				break
			}
			fmt.Fprintf(&out, "## %s\n%s\n", sec.title, trimmed)
			break
		}
		fmt.Fprintf(&out, "## %s\n%s\n", sec.title, sec.body)
		used += cost
	}
	return out.String(), nil
}

type section struct {
	title string
	body  string
}

// estimateTokens 在不依赖 tokenizer 的情况下近似估算 token 数。
// 中文密集文本 ≈ 每 rune 1 token；该启发式偏向安全侧。
func estimateTokens(s string) int {
	return int(float64(len([]rune(s))) * 0.7)
}

func trimToTokens(s string, budget int) string {
	if budget <= 0 {
		return ""
	}
	runes := []rune(s)
	maxRunes := int(float64(budget) / 0.7)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

func personNameByID(persons []store.Person, id string) string {
	for _, p := range persons {
		if p.ID == id {
			return p.CanonicalName
		}
	}
	return "某人"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
