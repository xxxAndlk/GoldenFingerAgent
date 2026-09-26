package store

import (
	"fmt"
	"strconv"
	"strings"
)

// vecToString 把 float32 切片编码为 pgvector 文本格式 '[0.1,0.2,...]'。
func vecToString(v []float32) string {
	if len(v) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// parseVec 把 pgvector 文本格式（可能带括号）解码为 []float32。
func parseVec(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	parts := strings.Split(s, ",")
	out := make([]float32, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		f, err := strconv.ParseFloat(p, 32)
		if err != nil {
			return nil, fmt.Errorf("parse vector element %q: %w", p, err)
		}
		out = append(out, float32(f))
	}
	return out, nil
}
