package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
)

// BuildContext 组装不依赖当前问题的记忆块（兼容旧调用）。
func (s *Service) BuildContext(ctx context.Context, ownerID, userType, tz string, budgetTokens int) (string, error) {
	return s.buildContext(ctx, ownerID, userType, tz, "", budgetTokens)
}

// BuildContextForQuery 先按当前问题检索共享长期记忆，再补充本地人物与事实。
// 检索超时/失败不会阻断整轮回答，只是缺少“相关长期记忆”区块。
func (s *Service) BuildContextForQuery(ctx context.Context, ownerID, userType, tz, query string, budgetTokens int) (string, error) {
	return s.buildContext(ctx, ownerID, userType, tz, query, budgetTokens)
}

// buildContext 在 token 预算内按优先级打包：用户 > 相关长期记忆 > 人物 > 事实 > 最近片段。
func (s *Service) buildContext(ctx context.Context, ownerID, userType, tz, query string, budgetTokens int) (string, error) {
	if budgetTokens <= 0 {
		budgetTokens = 1500
	}
	var sections []section
	seenFacts := map[string]bool{}

	sections = append(sections, section{
		title: "用户",
		body:  fmt.Sprintf("类型: %s, 时区: %s", userType, tz),
	})

	// 与当前问题相关的共享长期记忆。超时上限要短于一轮回答的整体预算。
	if s.longTerm != nil && strings.TrimSpace(query) != "" {
		searchCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
		facts, err := s.longTerm.Search(searchCtx, MemoryQuery{
			OwnerID: ownerID, Query: query, Limit: 6,
		})
		cancel()
		if err == nil && len(facts) > 0 {
			var b strings.Builder
			for _, fact := range facts {
				text := strings.TrimSpace(fact.Text)
				if text == "" || seenFacts[normalizeFactText(text)] {
					continue
				}
				seenFacts[normalizeFactText(text)] = true
				note := ""
				switch fact.Status {
				case "confirmed":
					note = "（已确认）"
				case "conflicted":
					note = "（有冲突）"
				}
				when := ""
				if fact.ValidAt != nil {
					when = "，自 " + fact.ValidAt.Format("2006-01-02")
				}
				if fact.InvalidAt != nil {
					when += "，截至 " + fact.InvalidAt.Format("2006-01-02")
				}
				fmt.Fprintf(&b, "- %s%s%s\n", text, note, when)
			}
			if b.Len() > 0 {
				sections = append(sections, section{title: "相关长期记忆", body: strings.TrimRight(b.String(), "\n")})
			}
		}
	}

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
			if seenFacts[normalizeFactText(f.ValueText)] {
				continue
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

func normalizeFactText(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
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
