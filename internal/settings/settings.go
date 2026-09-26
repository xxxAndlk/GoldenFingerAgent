// Package settings 将运行时可编辑的模型设置持久化为 JSON 文件，
// 使 Web UI 无需重启即可修改 LLM 的 base_url/api_key/model。
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// LLM 保存可从 Web UI 编辑的对话模型连接参数。
type LLM struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

type fileShape struct {
	LLM LLM `json:"llm"`
}

// Store 是基于 JSON 文件的设置容器（单写者、原子改名落盘）。
type Store struct {
	path string
	mu   sync.Mutex
	data fileShape
}

// Open 加载 path 处的文件；文件不存在不算错误。
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, err
	}
	return s, nil
}

// LLM 返回当前对话模型设置（未设置时为零值）。
func (s *Store) LLM() LLM {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.LLM
}

// SaveLLM 替换对话模型设置并以原子方式持久化。
func (s *Store) SaveLLM(l LLM) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.LLM = l
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
