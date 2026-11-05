// Package settings 将运行时可编辑的模型设置持久化为 JSON 文件，
// 使 Web UI 无需重启即可修改 LLM 的 base_url/api_key/model 与生成参数。
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"goldenfinger/agent/internal/secretstore"
)

// LLM 保存可从 Web UI 编辑的对话模型连接参数与生成参数。
type LLM struct {
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
	Model    string `json:"model"`
	Protocol string `json:"protocol,omitempty"`

	// 生成参数（都允许留空/零值＝使用内置默认）。
	ReasoningEffort string  `json:"reasoning_effort,omitempty"` // low | medium | high
	MaxContext      int     `json:"max_context,omitempty"`      // 上下文窗口 token 上限
	MaxOutput       int     `json:"max_output,omitempty"`       // 单次回复 token 上限
	Temperature     float64 `json:"temperature,omitempty"`
}

// Model 是一套可保存并切换的模型方案。
type Model struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	BaseURL         string  `json:"base_url"`
	APIKey          string  `json:"api_key,omitempty"`
	Model           string  `json:"model"`
	Protocol        string  `json:"protocol,omitempty"`
	ReasoningEffort string  `json:"reasoning_effort,omitempty"`
	MaxContext      int     `json:"max_context,omitempty"`
	MaxOutput       int     `json:"max_output,omitempty"`
	Temperature     float64 `json:"temperature,omitempty"`
}

// Search 保存联网检索的独立凭据（与对话模型分开，便于单独计价/限流）。
type Search struct {
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
}

// Relay 保存手机端中继地址。
type Relay struct {
	URL string `json:"url,omitempty"`
}

// CloudMemory 保存云端共享记忆与跨端交付的端侧连接参数。
// Token 由 secretstore 加密保存（Windows DPAPI），settings.json 只保留引用。
type CloudMemory struct {
	Enabled  bool   `json:"enabled,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
	OwnerID  string `json:"owner_user_id,omitempty"`
	// Token 仅用于兼容旧版本；新版本写入 TokenRef 后该字段应为空。
	Token    string `json:"token,omitempty"`
	TokenRef string `json:"token_ref,omitempty"`
	// DeliveryEnabled 单独控制文件交付，异常时可只关闭交付不影响记忆。
	DeliveryEnabled bool `json:"delivery_enabled,omitempty"`
}

// Email 保存可选的 IMAP 收件配置；密码只落本地 settings.json，不写日志。
type Email struct {
	Enabled        bool     `json:"enabled,omitempty"`
	Host           string   `json:"host,omitempty"`
	Port           int      `json:"port,omitempty"`
	Username       string   `json:"username,omitempty"`
	Password       string   `json:"password,omitempty"`
	Mailbox        string   `json:"mailbox,omitempty"`
	Mailboxes      []string `json:"mailboxes,omitempty"`
	Senders        []string `json:"senders,omitempty"`
	SubjectFilters []string `json:"subject_filters,omitempty"`
	SinceDays      int      `json:"since_days,omitempty"`
	TLS            bool     `json:"tls,omitempty"`
	InitialFetch   int      `json:"initial_fetch,omitempty"`
	PollSeconds    int      `json:"poll_seconds,omitempty"`
}

// WebSite 是授权网页采集的站点配置。只保存 credential_ref，不保存密码。
type WebSite struct {
	ID                  string   `json:"id"`
	Account             string   `json:"account,omitempty"`
	Enabled             bool     `json:"enabled"`
	StartURLs           []string `json:"start_urls,omitempty"`
	AllowedOrigins      []string `json:"allowed_origins,omitempty"`
	AllowedPathPrefixes []string `json:"allowed_path_prefixes,omitempty"`
	IntervalSeconds     int      `json:"interval_seconds,omitempty"`
	MaxPages            int      `json:"max_pages,omitempty"`
	MaxDepth            int      `json:"max_depth,omitempty"`
	SummaryEnabled      bool     `json:"summary_enabled"`
	IndexEnabled        bool     `json:"index_enabled"`
	BriefEnabled        bool     `json:"brief_enabled"`
	AutoWorkerEnabled   bool     `json:"auto_worker_enabled"`
	NavigationEnabled   bool     `json:"navigation_enabled"`
	SessionEnabled      bool     `json:"session_enabled"`
	AutoLoginEnabled    bool     `json:"auto_login_enabled"`
	CredentialRef       string   `json:"credential_ref,omitempty"`
	LoginURL            string   `json:"login_url,omitempty"`
	UsernameSelector    string   `json:"username_selector,omitempty"`
	PasswordSelector    string   `json:"password_selector,omitempty"`
	SubmitSelector      string   `json:"submit_selector,omitempty"`
}

// Web 保存授权网页采集的站点列表。
type Web struct {
	Sites []WebSite `json:"sites,omitempty"`
}

type fileShape struct {
	LLM         LLM         `json:"llm"`
	Models      []Model     `json:"models,omitempty"`
	Search      Search      `json:"search,omitempty"`
	Relay       Relay       `json:"relay,omitempty"`
	Email       Email       `json:"email,omitempty"`
	Web         Web         `json:"web,omitempty"`
	CloudMemory CloudMemory `json:"cloud_memory,omitempty"`
}

// Shape 是全部设置的完整快照，供 Save 的回调直接编辑各分区。
type Shape = fileShape

// Store 是基于 JSON 文件的设置容器（单写者、原子改名落盘）。
type Store struct {
	path string
	mu   sync.Mutex
	data fileShape
	raw  []byte
}

// Open 加载 path 处的文件；文件不存在不算错误。
func Open(path string) (*Store, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	s := &Store{path: absolutePath}
	raw, err := os.ReadFile(absolutePath)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, err
	}
	s.raw = raw
	return s, nil
}

// Path 返回可读写的 JSON 设置文件位置。
func (s *Store) Path() string { return s.path }

// reloadLocked 在文件被外部改动时热加载；无效内容保留上一次有效值。
func (s *Store) reloadLocked() {
	raw, err := os.ReadFile(s.path)
	if err != nil || bytes.Equal(raw, s.raw) {
		return
	}
	var next fileShape
	if json.Unmarshal(raw, &next) == nil {
		s.data = next
		s.raw = raw
	}
}

// LLM 返回当前设置；外部编辑 JSON 后，下次读取即热加载。
func (s *Store) LLM() LLM {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	return s.data.LLM
}

// Models 返回保存的模型方案列表。
func (s *Store) Models() []Model {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	return append([]Model(nil), s.data.Models...)
}

// Search 返回联网检索凭据。
func (s *Store) Search() Search {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	return s.data.Search
}

// Relay 返回中继地址。
func (s *Store) Relay() Relay {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	return s.data.Relay
}

// CloudMemory 返回云端共享记忆配置。
func (s *Store) CloudMemory() CloudMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	return s.data.CloudMemory
}

// SaveCloudMemory 替换云端共享记忆配置。
func (s *Store) SaveCloudMemory(v CloudMemory) error {
	return s.Save(func(f *fileShape) { f.CloudMemory = v })
}

func (s *Store) cloudTokenPath() string { return s.path + ".cloud-token" }

// CloudToken 返回明文 Token：优先旧字段，其次解密本机密钥文件。
func (s *Store) CloudToken() string {
	s.mu.Lock()
	s.reloadLocked()
	legacy := strings.TrimSpace(s.data.CloudMemory.Token)
	ref := s.data.CloudMemory.TokenRef
	s.mu.Unlock()
	if legacy != "" {
		if ref == "" {
			// 旧版本明文 Token 首次读取时自动迁移到加密存储。
			if err := s.SaveCloudToken(legacy); err == nil {
				return legacy
			}
		}
		return legacy
	}
	if ref == "" {
		return ""
	}
	raw, err := secretstore.Load(s.cloudTokenPath())
	if err != nil {
		return ""
	}
	return string(raw)
}

// HasCloudToken 报告是否已有可用 Token（不返回正文）。
func (s *Store) HasCloudToken() bool { return s.CloudToken() != "" }

// SaveCloudToken 加密保存 Token，并从 settings.json 清除明文。
func (s *Store) SaveCloudToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("settings: token 为空")
	}
	if err := secretstore.Save(s.cloudTokenPath(), []byte(token)); err != nil {
		return err
	}
	return s.Save(func(f *fileShape) {
		f.CloudMemory.Token = ""
		f.CloudMemory.TokenRef = "cloud-token.bin"
	})
}

// Email 返回 IMAP 收件配置。
func (s *Store) Email() Email {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	return s.data.Email
}

// Web 返回授权网页采集的站点配置。
func (s *Store) Web() Web {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	return s.data.Web
}

// Site 按 ID 查找站点配置；不存在返回 false。
func (s *Store) Site(id string) (WebSite, bool) {
	web := s.Web()
	for _, site := range web.Sites {
		if site.ID == id {
			return site, true
		}
	}
	return WebSite{}, false
}

// Save 在锁内应用 mutate 并以原子方式持久化全部设置。
func (s *Store) Save(mutate func(*fileShape)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadLocked()
	next := s.data
	mutate(&next)
	return s.writeLocked(next)
}

// SaveLLM 替换当前对话模型设置（保留模型列表与其它分区）。
func (s *Store) SaveLLM(l LLM) error {
	return s.Save(func(f *fileShape) { f.LLM = l })
}

// SaveSearch 替换联网检索凭据。
func (s *Store) SaveSearch(v Search) error {
	return s.Save(func(f *fileShape) { f.Search = v })
}

// SaveRelay 替换中继地址。
func (s *Store) SaveRelay(v Relay) error {
	return s.Save(func(f *fileShape) { f.Relay = v })
}

// SaveEmail 替换 IMAP 收件配置。
func (s *Store) SaveEmail(v Email) error {
	return s.Save(func(f *fileShape) { f.Email = v })
}

// SaveWeb 替换站点列表。
func (s *Store) SaveWeb(v Web) error {
	return s.Save(func(f *fileShape) { f.Web = v })
}

// UpsertSite 新增或覆盖一个站点，保留其他站点。
func (s *Store) UpsertSite(site WebSite) error {
	return s.Save(func(f *fileShape) {
		sites := make([]WebSite, 0, len(f.Web.Sites)+1)
		found := false
		for _, item := range f.Web.Sites {
			if item.ID == site.ID {
				sites = append(sites, site)
				found = true
				continue
			}
			sites = append(sites, item)
		}
		if !found {
			sites = append(sites, site)
		}
		f.Web.Sites = sites
	})
}

// DeleteSite 删除站点；不存在时仍成功。
func (s *Store) DeleteSite(id string) error {
	return s.Save(func(f *fileShape) {
		sites := make([]WebSite, 0, len(f.Web.Sites))
		for _, item := range f.Web.Sites {
			if item.ID != id {
				sites = append(sites, item)
			}
		}
		f.Web.Sites = sites
	})
}

func (s *Store) writeLocked(next fileShape) error {
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.data = next
	s.raw = raw
	return nil
}
