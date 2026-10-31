// Package skill 管理本机 Markdown 技能（skills/<name>/SKILL.md）。
// 只把名称与描述放进提示词，正文由 load_skill 按需读取，避免整池占用上下文。
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Skill struct {
	ID          string
	Name        string
	Description string
	Path        string
	Content     string
}

type Pool struct {
	mu    sync.RWMutex
	items map[string]*Skill
}

func NewPool() *Pool { return &Pool{items: make(map[string]*Skill)} }

// LoadDir 扫描 root/skills/<name>/SKILL.md。frontmatter 至少需要 description。
func (p *Pool) LoadDir(root string) (int, []string) {
	base := filepath.Join(root, "skills")
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0, nil
	}
	loaded := 0
	var skipped []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(base, entry.Name(), "SKILL.md")
		raw, err := os.ReadFile(path)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", entry.Name(), err))
			continue
		}
		meta, body := parseFrontmatter(string(raw))
		name := strings.TrimSpace(meta["name"])
		if name == "" {
			name = entry.Name()
		}
		desc := strings.TrimSpace(meta["description"])
		if desc == "" {
			skipped = append(skipped, entry.Name()+": 缺少 description")
			continue
		}
		item := &Skill{
			ID:          strings.ToLower(entry.Name()),
			Name:        name,
			Description: desc,
			Path:        path,
			Content:     strings.TrimSpace(body),
		}
		p.mu.Lock()
		p.items[item.ID] = item
		p.mu.Unlock()
		loaded++
	}
	return loaded, skipped
}

func parseFrontmatter(raw string) (map[string]string, string) {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return map[string]string{}, text
	}
	rest := text[4:]
	index := strings.Index(rest, "\n---")
	if index < 0 {
		return map[string]string{}, text
	}
	meta := map[string]string{}
	for _, line := range strings.Split(rest[:index], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		meta[strings.TrimSpace(strings.ToLower(key))] = strings.TrimSpace(value)
	}
	return meta, rest[index+4:]
}

func (p *Pool) All() []*Skill {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Skill, 0, len(p.items))
	for _, item := range p.items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Find 按 ID 或名称匹配（不区分大小写）。
func (p *Pool) Find(key string) *Skill {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	lower := strings.ToLower(key)
	p.mu.RLock()
	defer p.mu.RUnlock()
	if item, ok := p.items[lower]; ok {
		return item
	}
	for _, item := range p.items {
		if strings.ToLower(item.Name) == lower {
			return item
		}
	}
	return nil
}

// MetadataBlock 渲染给模型的技能清单（仅名称 + 描述）。
func (p *Pool) MetadataBlock() string {
	items := p.All()
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## 可用技能\n")
	for _, item := range items {
		fmt.Fprintf(&b, "- %s: %s\n", item.Name, item.Description)
	}
	b.WriteString("需要完整说明时调用 load_skill(名称)；不要凭空猜测技能里的接口或路径。\n")
	return b.String()
}
