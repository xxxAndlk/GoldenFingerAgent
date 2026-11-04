package memory

import (
	"context"
	"errors"
	"time"
)

// ErrUnsupported 表示当前长期记忆后端不支持某项管理操作。
// 调用方必须把它如实返回，不能把未执行的删除当成成功。
var ErrUnsupported = errors.New("memory: operation unsupported by provider")

// Episode 是写入长期记忆的一段原始业务证据。SourceID 永远使用
// GoldenFinger 的本地 ID，不包含任何后端实现名称。
type Episode struct {
	SourceID      string
	OwnerID       string
	Text          string
	ReferenceTime time.Time
	Source        string
}

// MemoryQuery 是与后端无关的长期记忆检索条件。
type MemoryQuery struct {
	OwnerID string
	Query   string
	Limit   int
}

// MemoryFact 是后端抽取的事实及其双时间语义。SourceRefs 保留可回查的
// 原始 episode 引用；ID 仅在 GoldenFinger 内部作为不透明标识使用。
type MemoryFact struct {
	ID         string
	Text       string
	ValidAt    *time.Time
	InvalidAt  *time.Time
	SourceRefs []string
	Status     string
}

// ForgetRequest 以本地来源引用或内部事实 ID 请求遗忘。
type ForgetRequest struct {
	OwnerID  string
	SourceID string
	FactID   string
}

// MemoryHealth 只暴露稳定的健康语义，不绑定任何后端的诊断字段。
type MemoryHealth struct {
	Ready   bool
	Backend string
	Detail  string
}

// LongTermMemory 是可替换的长期记忆端口。SQLite 原始证据存储不属于此
// 接口；后端不可用时，业务层仍可依靠 SQLite 工作和降级检索。
type LongTermMemory interface {
	Append(ctx context.Context, episode Episode) error
	Search(ctx context.Context, query MemoryQuery) ([]MemoryFact, error)
	Forget(ctx context.Context, request ForgetRequest) error
	Health(ctx context.Context) (MemoryHealth, error)
}
