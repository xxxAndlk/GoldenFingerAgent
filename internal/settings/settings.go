// Package settings persists runtime-editable model settings as a JSON file,
// so the web UI can change LLM base_url/api_key/model without a restart.
package settings

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// LLM holds the chat-model connection details editable from the web UI.
type LLM struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

type fileShape struct {
	LLM LLM `json:"llm"`
}

// Store is a JSON-file-backed settings holder (single-writer, atomic rename).
type Store struct {
	path string
	mu   sync.Mutex
	data fileShape
}

// Open loads path if it exists; a missing file is not an error.
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

// LLM returns the current chat-model settings (zero value when unset).
func (s *Store) LLM() LLM {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.LLM
}

// SaveLLM replaces the chat-model settings and persists them atomically.
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
