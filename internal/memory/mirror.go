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
// 镜像失败不能影响主流程——SQLite 已经写成功，用户的操作是有效的，
// 但必须留下日志，不能用静默失败假装后端正常。
func (s *Service) mirrorEpisode(ctx context.Context, sourceID, ownerID, text, source, role string, at time.Time) {
	if s.longTerm == nil || text == "" {
		return
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
	}
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
