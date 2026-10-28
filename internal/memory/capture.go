package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
)

// CaptureInput 是一段来自持续听写的情景记忆。ContextBefore/After 保存
// 原话附近的转写片段，避免“这个/那个”等指代脱离语境后无法回忆。
type CaptureInput struct {
	OwnerUserID   string
	Text          string
	ContextBefore string
	ContextAfter  string
	Source        string
	CapturedAt    time.Time
}

// CaptureResult 返回已保存的片段，以及它是否仍缺少指代上下文。
type CaptureResult struct {
	Episode      *store.Episode `json:"episode"`
	NeedsContext bool           `json:"needs_context"`
}

// EpisodeEvidence 保存在 episode.raw_ref 中。当前只保存文字证据，不保存音频。
type EpisodeEvidence struct {
	Version       int       `json:"version"`
	Source        string    `json:"source"`
	OriginalText  string    `json:"original_text"`
	ContextBefore string    `json:"context_before,omitempty"`
	ContextAfter  string    `json:"context_after,omitempty"`
	CapturedAt    time.Time `json:"captured_at"`
	NeedsContext  bool      `json:"needs_context"`
}

// CaptureEpisode 将一段随口说的话连同上下文持久化，并为语义检索建立向量。
// 没有 embedder 时仍保存全文，Search 会退化到关键词检索。
func (s *Service) CaptureEpisode(ctx context.Context, in CaptureInput) (*CaptureResult, error) {
	text := cleanCaptureText(in.Text, 2000)
	if text == "" {
		return nil, errors.New("memory: capture text is required")
	}
	before := cleanCaptureText(in.ContextBefore, 4000)
	after := cleanCaptureText(in.ContextAfter, 4000)
	source := cleanCaptureText(in.Source, 80)
	if source == "" {
		source = "ambient_speech"
	}
	at := in.CapturedAt
	if at.IsZero() {
		at = s.now()
	}
	needsContext := hasUnresolvedDeixis(text) && before == "" && after == ""

	summary := "原话：" + text
	if before != "" {
		summary += "\n前文：" + before
	}
	if after != "" {
		summary += "\n后文：" + after
	}
	evidence := EpisodeEvidence{
		Version: 1, Source: source, OriginalText: text,
		ContextBefore: before, ContextAfter: after,
		CapturedAt: at, NeedsContext: needsContext,
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}

	var vec []float32
	if s.embed != nil {
		if vecs, embedErr := s.embed.Embed(ctx, []string{summary}); embedErr == nil && len(vecs) == 1 {
			vec = vecs[0]
		}
	}
	e := &store.Episode{
		OwnerUserID: in.OwnerUserID,
		Summary:     summary,
		RawRef:      string(raw),
		TimeStart:   &at,
		TimeEnd:     &at,
		Embedding:   vec,
	}
	if err := s.eps.Create(ctx, e); err != nil {
		return nil, err
	}
	owner := in.OwnerUserID
	detail, _ := json.Marshal(map[string]any{"source": source, "needs_context": needsContext})
	_ = s.audit.Append(ctx, &owner, "device", "episode_capture", e.ID, detail)
	return &CaptureResult{Episode: e, NeedsContext: needsContext}, nil
}

func cleanCaptureText(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	runes := []rune(text)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return text
}

func hasUnresolvedDeixis(text string) bool {
	for _, marker := range []string{"这个", "那个", "这些", "那些", "它", "他说", "她说", "刚才那个"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
