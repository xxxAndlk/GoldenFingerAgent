package superagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"goldenfinger/agent/internal/store"
)

const (
	WebChannel = "web"
	WebKind    = "web_item"
)

// NormalizeWebText 只做可重复的空白归一化；不改变文字内容，供内容 hash 和去重使用。
func NormalizeWebText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func WebContentHash(text string) string {
	sum := sha256.Sum256([]byte(NormalizeWebText(text)))
	return hex.EncodeToString(sum[:])
}

// NormalizeWebURL 统一 scheme/host 大小写、去掉片段、排序查询参数并清理路径。
// 查询参数排序避免同一页面因参数顺序不同形成两条记录。
func NormalizeWebURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("url required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("url host required")
	}
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawFragment = ""
	if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
		u.Host = strings.TrimSuffix(u.Host, ":"+u.Port())
	}
	if u.Path == "" {
		u.Path = "/"
	} else {
		cleaned := path.Clean(u.Path)
		if cleaned != "/" && strings.HasSuffix(u.Path, "/") {
			cleaned += "/"
		}
		u.Path = cleaned
	}
	if u.RawQuery != "" {
		query := u.Query()
		keys := make([]string, 0, len(query))
		for key := range query {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		ordered := url.Values{}
		for _, key := range keys {
			values := query[key]
			sort.Strings(values)
			for _, value := range values {
				ordered.Add(key, value)
			}
		}
		u.RawQuery = ordered.Encode()
	}
	return u, nil
}

func CanonicalWebURL(raw string) (string, error) {
	u, err := NormalizeWebURL(raw)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func WebOrigin(raw string) (string, error) {
	u, err := NormalizeWebURL(raw)
	if err != nil {
		return "", err
	}
	return u.Scheme + "://" + u.Host, nil
}

// CheckWebURL 校验 URL 是否落在站点授权的 origin 和路径前缀内。
// 返回规范化后的 URL。
func CheckWebURL(raw string, allowedOrigins, allowedPathPrefixes []string) (string, error) {
	u, err := NormalizeWebURL(raw)
	if err != nil {
		return "", err
	}
	origin := u.Scheme + "://" + u.Host
	originAllowed := false
	for _, allowed := range allowedOrigins {
		allowed = strings.TrimRight(strings.ToLower(strings.TrimSpace(allowed)), "/")
		if allowed != "" && allowed == origin {
			originAllowed = true
			break
		}
	}
	if !originAllowed {
		return "", fmt.Errorf("origin %s 不在授权范围", origin)
	}
	if len(allowedPathPrefixes) == 0 {
		return u.String(), nil
	}
	requestPath := u.EscapedPath()
	for _, prefix := range allowedPathPrefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		if !strings.HasPrefix(prefix, "/") {
			prefix = "/" + prefix
		}
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(requestPath, prefix) {
				return u.String(), nil
			}
			continue
		}
		if requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/") {
			return u.String(), nil
		}
	}
	return "", fmt.Errorf("path %s 不在授权范围", u.Path)
}

func WebThreadExternalID(siteID, account, canonicalURL string) string {
	base := "web:" + siteID + ":" + account + ":" + canonicalURL
	if len(base) <= 300 {
		return base
	}
	sum := sha256.Sum256([]byte(canonicalURL))
	return "web:" + siteID + ":" + account + ":hash:" + hex.EncodeToString(sum[:])[:32]
}

func WebItemExternalID(siteID, account, itemID, contentHash string) string {
	base := "web:" + siteID + ":" + account + ":" + itemID + ":" + contentHash
	if len(base) <= 300 {
		return base
	}
	sum := sha256.Sum256([]byte(base))
	return "web:" + siteID + ":" + account + ":hash:" + hex.EncodeToString(sum[:])[:32]
}

// WebPayload 解析 Trigger 中保存的网页元数据。
type WebPayload struct {
	SiteID       string   `json:"site_id"`
	Account      string   `json:"account"`
	URL          string   `json:"url"`
	CanonicalURL string   `json:"canonical_url"`
	ItemID       string   `json:"item_id"`
	ContentHash  string   `json:"content_hash"`
	ExtractedBy  string   `json:"extracted_by"`
	Completeness string   `json:"completeness"`
	Change       string   `json:"change"`
	EvidenceIDs  []string `json:"evidence_ids"`
}

func WebSiteID(trigger *store.Trigger) string {
	return webPayload(trigger).SiteID
}

func WebAccount(trigger *store.Trigger) string {
	return webPayload(trigger).Account
}

func webPayload(trigger *store.Trigger) WebPayload {
	var payload WebPayload
	if trigger == nil || len(trigger.Payload) == 0 {
		return payload
	}
	_ = json.Unmarshal(trigger.Payload, &payload)
	return payload
}
