package memory

import (
	"context"
	"fmt"
	"strings"

	"goldenfinger/agent/internal/store"
)

// BuildContext assembles the memory block injected into the system prompt,
// packed in priority order under a token budget (doc: 按 token 预算注入，不堆全量).
//
// Priority: user profile > pending note > active tasks > persons > facts.
// Overflow truncates the lowest-priority section first.
func (s *Service) BuildContext(ctx context.Context, ownerID, userType, tz string, budgetTokens int) (string, error) {
	if budgetTokens <= 0 {
		budgetTokens = 1500
	}
	var sections []section

	sections = append(sections, section{
		title: "用户",
		body:  fmt.Sprintf("类型: %s, 时区: %s", userType, tz),
	})

	// Person roster.
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

	// Current facts, most recent first (cheap pre-filter; ranking happens on search).
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

	// Recent notes/episodes.
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

	// Pack under budget: head sections first, drop/trim the tail.
	var out strings.Builder
	used := 0
	for _, sec := range sections {
		cost := estimateTokens(sec.title+sec.body) + 8
		if used+cost > budgetTokens {
			// Try a trimmed version before dropping entirely.
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

// estimateTokens approximates token count without a tokenizer dependency.
// CJK-heavy text ≈ 1 token per rune; this heuristic errs on the safe side.
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
