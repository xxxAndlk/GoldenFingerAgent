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
	// Role 说明这段话是谁说的：owner 是机主本人，contact 是联系人，
	// system 是系统提示。抽取长期记忆时必须区分，否则会把别人的话
	// 记成机主自己的意愿。
	Role string
	// Slot 是跨端共享内容的位置（简报日期、联系人备注等）。
	//
	// 同一个槽位只保留最新一版：后端按 Slot 原子替换旧版本，
	// 两端先后修改时用 UpdatedAt 判断谁更新，旧请求不能覆盖新内容。
	// 为空表示普通片段，不参与槽位替换。
	Slot string
	// UpdatedAt 是这次写入的发生时间（毫秒精度由后端截断），
	// 用于槽位内的新旧判断。
	UpdatedAt time.Time
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
	// SourcePrefix 按来源 ID 前缀遗忘。
	//
	// 用于跨端共享的记忆槽位：同一位置可能有多个版本（内容每次变化
	// 都会换一个来源 ID），只删本机记着的那一条会留下另一端写的版本。
	SourcePrefix string
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
