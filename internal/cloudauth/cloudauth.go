// Package cloudauth 实现云端设备的凭据、scope 与 owner 解析。
// 云端服务永远从设备 Token 解析 owner，不信任请求体自报的身份。
package cloudauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
)

const (
	ScopeMemoryRead    = "memory:read"
	ScopeMemoryWrite   = "memory:write"
	ScopeDeliveryRead  = "delivery:read"
	ScopeDeliveryWrite = "delivery:write"
)

var (
	ErrUnauthorized = errors.New("cloudauth: unauthorized")
	ErrForbidden    = errors.New("cloudauth: forbidden")
)

var validScopes = map[string]bool{
	ScopeMemoryRead:    true,
	ScopeMemoryWrite:   true,
	ScopeDeliveryRead:  true,
	ScopeDeliveryWrite: true,
}

// TokenHash 返回设备 Token 的存储哈希。
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewToken 生成 32 字节随机设备 Token（只显示一次）。
func NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// NormalizeScopes 去重、去空白并拒绝未知 scope。
func NormalizeScopes(raw []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		scope := strings.TrimSpace(item)
		if scope == "" {
			continue
		}
		if !validScopes[scope] {
			return nil, fmt.Errorf("未知 scope: %s", scope)
		}
		if seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	if len(out) == 0 {
		return nil, errors.New("至少需要一个 scope")
	}
	return out, nil
}

// HasScope 判断设备是否拥有某个 scope。
func HasScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want || scope == "*" {
			return true
		}
	}
	return false
}

// CreateDevice 创建设备并返回明文 Token（调用方负责只展示一次）。
func CreateDevice(ctx context.Context, repos *store.Repos, ownerID, name string, scopes []string) (string, *store.CloudDevice, error) {
	ownerID = strings.TrimSpace(ownerID)
	name = strings.TrimSpace(name)
	if ownerID == "" || name == "" {
		return "", nil, errors.New("owner 和 name 必填")
	}
	normalized, err := NormalizeScopes(scopes)
	if err != nil {
		return "", nil, err
	}
	token, err := NewToken()
	if err != nil {
		return "", nil, err
	}
	device := &store.CloudDevice{
		ID: uuid.NewString(), OwnerUserID: ownerID, DeviceName: name,
		TokenHash: TokenHash(token), Scopes: normalized,
	}
	if err := repos.CloudDevices.Create(ctx, device); err != nil {
		return "", nil, err
	}
	return token, device, nil
}

// RevokeDevice 撤销设备；不存在返回 store.ErrNotFound。
func RevokeDevice(ctx context.Context, repos *store.Repos, ownerID, deviceID string, now time.Time) error {
	ok, err := repos.CloudDevices.Revoke(ctx, ownerID, deviceID, now)
	if err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	return nil
}

// Authenticator 从 Authorization: Bearer 解析设备并校验 scope。
type Authenticator struct {
	Repos *store.Repos
	Now   func() time.Time
}

func (a *Authenticator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Authenticate 返回设备；无效 Token 与已撤销设备统一返回 ErrUnauthorized。
func (a *Authenticator) Authenticate(ctx context.Context, r *http.Request, scope string) (*store.CloudDevice, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	token := ""
	if strings.HasPrefix(header, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	}
	if token == "" || a == nil || a.Repos == nil {
		return nil, ErrUnauthorized
	}
	hash := TokenHash(token)
	device, err := a.Repos.CloudDevices.GetByTokenHash(ctx, hash)
	if err != nil || device.RevokedAt != nil ||
		subtle.ConstantTimeCompare([]byte(device.TokenHash), []byte(hash)) != 1 {
		return nil, ErrUnauthorized
	}
	if !HasScope(device.Scopes, scope) {
		return nil, ErrForbidden
	}
	_ = a.Repos.CloudDevices.Touch(ctx, device.ID, a.now())
	return device, nil
}
