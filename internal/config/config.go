// Package config 加载 YAML 配置，并支持 GFA_* 环境变量覆盖。
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server     Server     `yaml:"server"`
	Database   Database   `yaml:"database"`
	LLM        LLM        `yaml:"llm"`
	Embedder   LLM        `yaml:"embedder"`
	Search     Search     `yaml:"search"`
	Thresholds Thresholds `yaml:"thresholds"`
	DND        DND        `yaml:"dnd"`
	Scheduler  Scheduler  `yaml:"scheduler"`
	Digest     Digest     `yaml:"digest"`
	Memory     Memory     `yaml:"memory"`
}

type Server struct {
	Addr   string `yaml:"addr"`
	WebDir string `yaml:"web_dir"`
}

type Database struct {
	URL string `yaml:"url"`
}

type LLM struct {
	BaseURL     string  `yaml:"base_url"`
	APIKey      string  `yaml:"api_key"`     // 直接密钥；共享环境建议优先用环境变量
	APIKeyEnv   string  `yaml:"api_key_env"` // 存放密钥的环境变量名（优先级高于 api_key）
	Model       string  `yaml:"model"`
	Temperature float64 `yaml:"temperature"`
	MaxTokens   int     `yaml:"max_tokens"`
	Dim         int     `yaml:"dim"` // 仅 embedder 使用
}

type Thresholds struct {
	TaskAuto      float64 `yaml:"task_auto"`
	TaskClarify   float64 `yaml:"task_clarify"`
	PersonClarify float64 `yaml:"person_clarify"`
	FactConfirmed float64 `yaml:"fact_confirmed"`
}

// Search 配置 Web 搜索（Firecrawl Search API）。
type Search struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Count   int    `yaml:"count"`
}

type DND struct {
	Window             string   `yaml:"window"` // "22:00-07:00"
	ChildNightSilence  bool     `yaml:"child_night_silence"`
	UrgentBreaksDNDFor []string `yaml:"urgent_breaks_dnd_for"`
}

type Scheduler struct {
	TickInterval time.Duration `yaml:"tick_interval"`
	AckTimeout   time.Duration `yaml:"ack_timeout"`
	MaxLevel     int           `yaml:"max_level"`
	MaxDailyPush int           `yaml:"max_daily_push"`
}

type Digest struct {
	Time string `yaml:"time"` // "11:00" 用户本地时区
}

type Memory struct {
	ContextTokenBudget int           `yaml:"context_token_budget"`
	RecencyHalfLife    time.Duration `yaml:"recency_half_life"`
}

// Load 读取 path（或 GFA_CONFIG，默认 config.yaml）并应用环境变量覆盖。
func Load(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("GFA_CONFIG")
	}
	if path == "" {
		path = "config.yaml"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	c.applyEnv()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.WebDir == "" {
		c.Server.WebDir = "web"
	}
	if c.Scheduler.TickInterval == 0 {
		c.Scheduler.TickInterval = 30 * time.Second
	}
	if c.Scheduler.AckTimeout == 0 {
		c.Scheduler.AckTimeout = 30 * time.Minute
	}
	if c.Scheduler.MaxLevel == 0 {
		c.Scheduler.MaxLevel = 3
	}
	if c.Scheduler.MaxDailyPush == 0 {
		c.Scheduler.MaxDailyPush = 3
	}
	if c.Digest.Time == "" {
		c.Digest.Time = "11:00"
	}
	if c.Memory.ContextTokenBudget == 0 {
		c.Memory.ContextTokenBudget = 1500
	}
	if c.Memory.RecencyHalfLife == 0 {
		c.Memory.RecencyHalfLife = 720 * time.Hour
	}
	if c.DND.Window == "" {
		c.DND.Window = "22:00-07:00"
	}
	if c.Embedder.Dim == 0 {
		c.Embedder.Dim = 1024
	}
	if c.LLM.Temperature == 0 {
		c.LLM.Temperature = 0.4
	}
	if c.LLM.MaxTokens == 0 {
		c.LLM.MaxTokens = 2048
	}
	if c.Search.BaseURL == "" {
		c.Search.BaseURL = "https://api.firecrawl.dev"
	}
	if c.Search.Count == 0 {
		c.Search.Count = 8
	}
}

func (c *Config) applyEnv() {
	if v := os.Getenv("GFA_DATABASE_URL"); v != "" {
		c.Database.URL = v
	}
	if v := os.Getenv("GFA_SERVER_ADDR"); v != "" {
		c.Server.Addr = v
	}
	if v := os.Getenv("GFA_LLM_BASE_URL"); v != "" {
		c.LLM.BaseURL = v
	}
	if v := os.Getenv("GFA_LLM_MODEL"); v != "" {
		c.LLM.Model = v
	}
	if v := os.Getenv("GFA_EMBED_BASE_URL"); v != "" {
		c.Embedder.BaseURL = v
	}
	if v := os.Getenv("GFA_EMBED_MODEL"); v != "" {
		c.Embedder.Model = v
	}
	if v := os.Getenv("GFA_EMBED_DIM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Embedder.Dim = n
		}
	}
	// API 密钥优先级：YAML api_key < api_key_env 指定的环境变量 < GFA_*_API_KEY。
	if c.LLM.APIKeyEnv != "" {
		if v := os.Getenv(c.LLM.APIKeyEnv); v != "" {
			c.LLM.APIKey = v
		}
	}
	if c.Embedder.APIKeyEnv != "" {
		if v := os.Getenv(c.Embedder.APIKeyEnv); v != "" {
			c.Embedder.APIKey = v
		}
	}
	if v := os.Getenv("GFA_LLM_API_KEY"); v != "" {
		c.LLM.APIKey = v
	}
	if v := os.Getenv("GFA_EMBED_API_KEY"); v != "" {
		c.Embedder.APIKey = v
	}
	if v := os.Getenv("GFA_SEARCH_API_KEY"); v != "" {
		c.Search.APIKey = v
	}
}

func (c *Config) validate() error {
	if c.Database.URL == "" {
		return fmt.Errorf("config: database.url is required")
	}
	if c.Thresholds.TaskAuto == 0 {
		c.Thresholds.TaskAuto = 0.85
	}
	if c.Thresholds.TaskClarify == 0 {
		c.Thresholds.TaskClarify = 0.6
	}
	if c.Thresholds.PersonClarify == 0 {
		c.Thresholds.PersonClarify = 0.8
	}
	if c.Thresholds.FactConfirmed == 0 {
		c.Thresholds.FactConfirmed = 0.8
	}
	if c.Thresholds.TaskAuto <= c.Thresholds.TaskClarify {
		return fmt.Errorf("config: thresholds.task_auto must be > task_clarify")
	}
	return nil
}
