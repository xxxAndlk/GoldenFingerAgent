// Package firecrawl 是 Firecrawl Search API 客户端（POST /v2/search）。
// 只用 net/http + encoding/json——无第三方依赖。
package firecrawl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"goldenfinger/agent/internal/extsvc"
)

// Client 调用 Firecrawl Search API。零值不可用——请用 New。
type Client struct {
	BaseURL string
	APIKey  string
	Count   int
	HTTP    *http.Client
}

// New 构建客户端。baseURL 可为空（回退到公共端点）。
func New(baseURL, apiKey string, count int) *Client {
	if baseURL == "" {
		baseURL = "https://api.firecrawl.dev"
	}
	if count <= 0 {
		count = 8
	}
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Count:   count,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

type requestBody struct {
	Query   string   `json:"query"`
	Limit   int      `json:"limit"`
	Sources []string `json:"sources"`
}

type item struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

type responseEnvelope struct {
	Success bool   `json:"success"`
	Data    []item `json:"data"`
}

// Search 运行一次网页搜索并返回最多 count 条结果。
// HTTP 失败或 body.success != true 返回错误（绝不静默吞掉）。
func (c *Client) Search(ctx context.Context, query string, count int) ([]extsvc.SearchResult, error) {
	if count <= 0 {
		count = c.Count
	}
	body, err := json.Marshal(requestBody{Query: query, Limit: count, Sources: []string{"web"}})
	if err != nil {
		return nil, fmt.Errorf("firecrawl: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v2/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("firecrawl: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("firecrawl: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("firecrawl: http %d: %s", resp.StatusCode, truncate(string(b)))
	}
	var env responseEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("firecrawl: decode response: %w", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("firecrawl: api success=false")
	}
	res := make([]extsvc.SearchResult, 0, len(env.Data))
	for _, it := range env.Data {
		res = append(res, extsvc.SearchResult{Name: it.Title, URL: it.URL, Snippet: it.Description})
	}
	return res, nil
}

func truncate(s string) string {
	const max = 128
	if len(s) <= max {
		return s
	}
	i := max
	for i > 0 && s[i]&0xC0 == 0x80 {
		i--
	}
	return s[:i]
}
