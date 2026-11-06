package memory

import (
	"context"
	"log"
	"time"
)

// mirrorEpisode 把已写入 SQLite 的证据同步到可替换长期记忆后端。
//
// 为什么需要它：SQLite 的 episode/fact 是「原始真相」，长期记忆后端负责
// 抽取与检索；不镜像就会导致两边越用越不一致（新鲜对话进不了语义检索）。
// SQLite 已经写成功时，调用方可以把镜像失败作为同步错误返回；
// 普通聊天镜像可以忽略该错误，共享事实写入必须把它暴露给调用方。
func (s *Service) mirrorEpisode(ctx context.Context, sourceID, ownerID, text, source, role string, at time.Time) error {
	if s.longTerm == nil || text == "" {
		return nil
	}
	episode := Episode{
		SourceID:      sourceID,
		OwnerID:       ownerID,
		Text:          text,
		ReferenceTime: at,
		Source:        source,
		Role:          role,
	}
	if err := s.longTerm.Append(ctx, episode); err != nil {
		log.Printf("[memory] long-term mirror failed (source=%s): %v", sourceID, err)
		return err
	}
	return nil
}

// mirrorSharedEpisode 写入一条跨端共享的记忆。
//
// slot 让后端按槽位原子替换旧版本（同一位置只保留最新一版），
// updatedAt 是这次写入的时刻：两端时钟有偏差时，后端据此判断谁更新，
// 离线很久的旧请求恢复后不会把新内容覆盖掉。
func (s *Service) mirrorSharedEpisode(ctx context.Context, sourceID, ownerID, text, source, slot string,
	at, updatedAt time.Time) error {
	if s.longTerm == nil || text == "" {
		return nil
	}
	episode := Episode{
		SourceID:      sourceID,
		OwnerID:       ownerID,
		Text:          text,
		ReferenceTime: at,
		Source:        source,
		Role:          RoleOwner,
		Slot:          slot,
		UpdatedAt:     updatedAt,
	}
	if err := s.longTerm.Append(ctx, episode); err != nil {
		log.Printf("[memory] 共享记忆写入失败 (slot=%s): %v", slot, err)
		return err
	}
	return nil
}

// SetLongTermMemory 装配可替换的长期记忆后端；传 nil 表示只用 SQLite 检索。
func (s *Service) SetLongTermMemory(provider LongTermMemory) {
	s.longTerm = provider
}

// MirrorChat 把一条聊天消息镜像到长期记忆（来源 ID 为 chat:<messageID>）。
// 后端按来源 ID 去重，因此重复调用不会重复抽取。
func (s *Service) MirrorChat(ctx context.Context, ownerID, messageID, text string, at time.Time) {
	s.mirrorEpisode(ctx, "chat:"+messageID, ownerID, text, "chat", RoleOwner, at)
}

// MirrorContact 把一条联系人发来的消息镜像到长期记忆。角色是 contact，
// 抽取时不会把对方的话当成机主自己的安排。
func (s *Service) MirrorContact(ctx context.Context, ownerID, messageID, text string, at time.Time) {
	s.mirrorEpisode(ctx, "chat:"+messageID, ownerID, text, "chat", RoleContact, at)
}
