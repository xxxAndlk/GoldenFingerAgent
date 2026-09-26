package memory

import (
	"context"
	"math"
	"sort"
	"time"
)

// Snippet 是一个可检索的记忆条目（事实或片段）及其排序得分。
type Snippet struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"` // "fact" | "episode"
	Text   string  `json:"text"`
	Person string  `json:"person,omitempty"`
	Score  float64 `json:"score"`
	Status string  `json:"status,omitempty"`
}

// 排序得分：0.6*cosine + 0.25*recency + 0.15*confidence。
const (
	weightSim  = 0.6
	weightRec  = 0.25
	weightConf = 0.15
)

// Search 对查询做嵌入并对事实与片段排序。
func (s *Service) Search(ctx context.Context, ownerID, query string, k int) ([]Snippet, error) {
	if k <= 0 {
		k = 8
	}
	var queryVec []float32
	if s.embed != nil {
		vecs, err := s.embed.Embed(ctx, []string{query})
		if err == nil && len(vecs) == 1 {
			queryVec = vecs[0]
		}
	}

	var out []Snippet
	if len(queryVec) > 0 {
		halfLife := s.recencyHalfLifeValue()
		facts, err := s.facts.Similar(ctx, ownerID, queryVec, k)
		if err != nil {
			return nil, err
		}
		for _, f := range facts {
			out = append(out, Snippet{
				ID:     f.ID,
				Kind:   "fact",
				Text:   f.FactType + ": " + f.ValueText,
				Score:  finalScore(f.Score, f.UpdatedAt, f.Confidence, halfLife),
				Status: f.Status,
			})
		}
		eps, err := s.eps.Similar(ctx, ownerID, queryVec, k)
		if err != nil {
			return nil, err
		}
		for _, e := range eps {
			out = append(out, Snippet{
				ID:    e.ID,
				Kind:  "episode",
				Text:  e.Summary,
				Score: finalScore(e.Score, e.CreatedAt, 0.8, halfLife),
			})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// finalScore 融合相似度、时效性（指数衰减）与置信度。
func finalScore(sim float64, updatedAt time.Time, confidence float64, halfLife time.Duration) float64 {
	age := time.Since(updatedAt)
	if age < 0 {
		age = 0
	}
	half := halfLife
	if half <= 0 {
		half = 30 * 24 * time.Hour
	}
	recency := math.Exp(-math.Ln2 * float64(age) / float64(half))
	return weightSim*sim + weightRec*recency + weightConf*confidence
}
