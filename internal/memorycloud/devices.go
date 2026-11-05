package memorycloud

import (
	"context"
	"time"

	"goldenfinger/agent/internal/cloudauth"
	"goldenfinger/agent/internal/store"
)

// 设备 scope（与云端其他模块共用 cloudauth 定义）。
const (
	ScopeMemoryRead    = cloudauth.ScopeMemoryRead
	ScopeMemoryWrite   = cloudauth.ScopeMemoryWrite
	ScopeDeliveryRead  = cloudauth.ScopeDeliveryRead
	ScopeDeliveryWrite = cloudauth.ScopeDeliveryWrite
)

// CreateDevice 创建设备并返回明文 Token（调用方负责只展示一次）。
func CreateDevice(ctx context.Context, repos *store.Repos, ownerID, name string, scopes []string) (string, *store.CloudDevice, error) {
	return cloudauth.CreateDevice(ctx, repos, ownerID, name, scopes)
}

// RevokeDevice 撤销设备；不存在返回 store.ErrNotFound。
func RevokeDevice(ctx context.Context, repos *store.Repos, ownerID, deviceID string, now time.Time) error {
	return cloudauth.RevokeDevice(ctx, repos, ownerID, deviceID, now)
}
