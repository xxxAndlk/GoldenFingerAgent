package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Desktop 是电脑端使用中继的便捷封装：负责凭据持久化、
// 自动注册、长连接维持与配对码/通道管理。
//
// 凭据文件只包含中继地址、desktop_id 与 desktop_token，
// 不含任何用户数据；权限 0600。
type Desktop struct {
	BaseURL string
	Name    string
	// StatePath 是凭据文件路径，通常放在应用数据目录下。
	StatePath string
	// Handle 执行手机指令。
	Handle Handler
	Logger func(format string, args ...any)

	mu     sync.Mutex
	client *Client
	state  desktopState
	loaded bool
}

type desktopState struct {
	BaseURL   string `json:"base_url"`
	DesktopID string `json:"desktop_id"`
	Token     string `json:"desktop_token"`
}

// Load 读取已保存的凭据；文件不存在时返回空状态。
func (d *Desktop) Load() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.loaded = true
	raw, err := os.ReadFile(d.StatePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var st desktopState
	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("relay: 凭据文件损坏: %w", err)
	}
	// 中继地址变更（例如从测试机切到正式机）后旧凭据失效，需重新注册。
	if d.BaseURL != "" && st.BaseURL != "" && st.BaseURL != d.BaseURL {
		d.state = desktopState{}
		return nil
	}
	d.state = st
	if d.state.BaseURL == "" {
		d.state.BaseURL = d.BaseURL
	}
	return nil
}

func (d *Desktop) save() error {
	if d.StatePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(d.StatePath), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(d.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := d.StatePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.StatePath)
}

// EnsureCredentials 在首次使用时向中继注册并持久化 desktop_id/token。
func (d *Desktop) EnsureCredentials(ctx context.Context) (string, error) {
	d.mu.Lock()
	if !d.loaded {
		d.mu.Unlock()
		if err := d.Load(); err != nil {
			return "", err
		}
		d.mu.Lock()
	}
	if d.state.DesktopID != "" && d.state.Token != "" {
		id := d.state.DesktopID
		d.mu.Unlock()
		return id, nil
	}
	base := d.BaseURL
	if d.state.BaseURL != "" {
		base = d.state.BaseURL
	}
	d.mu.Unlock()

	probe := &Client{BaseURL: base, Name: d.Name, Logger: d.Log()}
	id, token, err := probe.Register(ctx)
	if err != nil {
		return "", err
	}

	d.mu.Lock()
	d.state = desktopState{BaseURL: base, DesktopID: id, Token: token}
	d.mu.Unlock()
	if err := d.save(); err != nil {
		return id, fmt.Errorf("relay: 保存凭据失败: %w", err)
	}
	return id, nil
}

// Log 返回日志函数（默认 log.Printf 由 Client 处理）。
func (d *Desktop) Log() func(string, ...any) {
	if d.Logger != nil {
		return d.Logger
	}
	return nil
}

// Client 返回当前客户端（未配置凭据时返回 nil）。
func (d *Desktop) ClientFor(ctx context.Context) (*Client, error) {
	if _, err := d.EnsureCredentials(ctx); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.client != nil {
		return d.client, nil
	}
	d.client = &Client{
		BaseURL: d.state.BaseURL, DesktopID: d.state.DesktopID, Token: d.state.Token,
		Name: d.Name, Handle: d.Handle, Logger: d.Log(),
	}
	return d.client, nil
}

// Run 保持在线直到 ctx 结束。未配置时立即返回错误，调用方应降级为“单机模式”。
func (d *Desktop) Run(ctx context.Context) error {
	client, err := d.ClientFor(ctx)
	if err != nil {
		return err
	}
	return client.Run(ctx)
}

// PairCode 获取一次性配对码（供电脑端界面展示）。
func (d *Desktop) PairCode(ctx context.Context) (string, time.Time, error) {
	client, err := d.ClientFor(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	return client.PairCode(ctx)
}

// Channels 列出已配对手机。
func (d *Desktop) Channels(ctx context.Context) ([]map[string]string, error) {
	client, err := d.ClientFor(ctx)
	if err != nil {
		return nil, err
	}
	return client.Channels(ctx)
}

// Revoke 解除某台手机的绑定（手机端需重新配对）。
func (d *Desktop) Revoke(ctx context.Context, channelID string) error {
	client, err := d.ClientFor(ctx)
	if err != nil {
		return err
	}
	return client.RevokeChannel(ctx, channelID)
}

// Status 返回可展示的连接状态（不含 token）。
func (d *Desktop) Status() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]any{
		"configured": d.state.DesktopID != "",
		"base_url":   d.state.BaseURL,
		"desktop_id": d.state.DesktopID,
		"connected":  false,
	}
	if d.client != nil {
		out["connected"] = d.client.Connected()
	}
	return out
}

// Connected 报告当前是否与中继保持连接。
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}
