// Package deepseek 实现 DeepSeek 官方 Search 与 Fetch 工具 API。
// 文档：https://api-docs.deepseek.com/guides/search/
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"goldenfinger/agent/internal/extsvc"
)

const DefaultBaseURL = "https://api.deepseek.com"

// Client 同时实现 extsvc.SearchService 与 extsvc.FetchService。
type Client struct {
	BaseURL string
	APIKey  string
	Count   int
	HTTP    *http.Client
}

func New(baseURL, apiKey string, count int) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if count <= 0 {
		count = 8
	}
	return &Client{BaseURL: baseURL, APIKey: strings.TrimSpace(apiKey), Count: count, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

type searchRequest struct {
	Query       string `json:"query"`
	Count       int    `json:"count"`
	NeedContent bool   `json:"need_content"`
}

type searchResponse struct {
	Results []struct {
		Title    string `json:"title"`
		URL      string `json:"url"`
		Snippet  string `json:"snippet"`
		Summary  string `json:"summary"`
		SiteName string `json:"site_name"`
		Date     string `json:"date"`
	} `json:"results"`
}

func (c *Client) Search(ctx context.Context, query string, count int) ([]extsvc.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > 128 {
		return nil, errors.New("deepseek search: query 必须为 1 到 128 个字符")
	}
	if count <= 0 {
		count = c.Count
	}
	if count < 1 || count > 50 {
		return nil, errors.New("deepseek search: count 必须在 1 到 50 之间")
	}
	var response searchResponse
	if err := c.post(ctx, "/v1/tools/search", searchRequest{Query: query, Count: count, NeedContent: false}, &response); err != nil {
		return nil, err
	}
	results := make([]extsvc.SearchResult, 0, len(response.Results))
	for _, item := range response.Results {
		results = append(results, extsvc.SearchResult{
			Name: item.Title, URL: item.URL, Snippet: item.Snippet, Summary: item.Summary,
			SiteName: item.SiteName, DatePublished: item.Date,
		})
	}
	return results, nil
}

func (c *Client) Fetch(ctx context.Context, rawURL string) (*extsvc.WebPage, error) {
	rawURL = strings.TrimSpace(rawURL)
	if len(rawURL) == 0 || len(rawURL) > 8192 {
		return nil, errors.New("deepseek fetch: url 长度必须为 1 到 8192")
	}
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return nil, errors.New("deepseek fetch: url 必须是不含用户名密码的 http/https 地址")
	}
	var page extsvc.WebPage
	if err := c.post(ctx, "/v1/tools/fetch", map[string]string{"url": rawURL}, &page); err != nil {
		return nil, err
	}
	page.URL = rawURL
	return &page, nil
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	if c.APIKey == "" {
		return errors.New("deepseek tools: 请先在模型设置中配置 DeepSeek API Key")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("deepseek tools: 编码请求：%w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("deepseek tools: 创建请求：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("deepseek tools: 请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("deepseek tools: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return fmt.Errorf("deepseek tools: 解析响应：%w", err)
	}
	return nil
}
