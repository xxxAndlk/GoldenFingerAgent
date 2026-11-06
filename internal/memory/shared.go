package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"goldenfinger/agent/internal/store"
)

// 共享记忆的槽位前缀。
//
// 产品口径：手机与电脑只共享同一份长期记忆，原始聊天记录与会话历史不共享。
// 因此协同总结、工作简报、联系人备注都要写进长期记忆，
// 由另一端从同一份长期记忆里检索到。
const (
	// SharedSlotBrief 是每日工作简报。
	SharedSlotBrief = "brief"
	// SharedSlotPersonNotes 是联系人备注。
	SharedSlotPersonNotes = "person_notes"
	// SharedSlotCollabSummary 是协作模式的最终阶段总结。
	SharedSlotCollabSummary = "collab_summary"
)

// BriefSlot 返回某一天简报的槽位。
func BriefSlot(date string) string {
	return SharedSlotBrief + ":" + strings.TrimSpace(date)
}

// PersonNotesSlot 返回某个联系人备注的槽位。
//
// 用规范名而不是本地人物 ID：两端的本地 ID 不同，只有同一个名字
// 才能让「手机改备注」和「电脑改备注」落在同一个槽位上。
func PersonNotesSlot(canonicalName string) string {
	return SharedSlotPersonNotes + ":" + strings.TrimSpace(canonicalName)
}

// CollabSummarySlot 返回某个协作阶段总结的槽位。
func CollabSummarySlot(phaseID string) string {
	return SharedSlotCollabSummary + ":" + strings.TrimSpace(phaseID)
}

// SharedInput 是一条要跨端共享的长期记忆。
type SharedInput struct {
	OwnerUserID string
	// Slot 是这条内容的稳定位置，例如 brief:2026-11-05。
	// 同一个槽位只保留最新一版。
	Slot string
	// Text 是正文；空字符串表示删除这个槽位。
	Text string
	// Source 标记内容来源（brief / collab_summary / person_notes）。
	Source string
	// At 是内容发生时间。
	At time.Time
}

// SaveShared 写入或更新一条跨端共享记忆。
//
// 来源 ID 按内容寻址（槽位 + 内容哈希）：同样的内容在两端写入是同一个 ID，
// 云端按 ID 去重，不会出现两条；内容变了就是新 ID，后端按槽位删除旧版本。
// 云端不允许同一个 ID 改内容，所以「更新」必须换 ID。
func (s *Service) SaveShared(ctx context.Context, in SharedInput) error {
	slot := strings.TrimSpace(in.Slot)
	if slot == "" {
		return errors.New("memory: 共享记忆需要 slot")
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return s.ForgetShared(ctx, in.OwnerUserID, slot)
	}
	at := in.At
	if at.IsZero() {
		at = s.now()
	}
	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = SharedSlotBrief
	}
	sourceID := SharedSourceID(slot, text)
	// 「同一槽位只保留最新版」由后端保证：
	// 本机（Native）与云端都在事务里替换旧版本，并按写入时间判断新旧，
	// 所以两端交替修改时不会各留一条，旧请求也不会覆盖新内容。
	return s.mirrorSharedEpisode(ctx, sourceID, in.OwnerUserID, text, source, slot, at, s.now())
}

// ForgetShared 删除某个槽位在共享长期记忆里的全部版本。
//
// 按前缀删而不是按本机记的那一条：另一端写的版本本机并不知道，
// 只删本机那条会留下「本机已删、云端仍可召回」的内容。
func (s *Service) ForgetShared(ctx context.Context, ownerID, slot string) error {
	slot = strings.TrimSpace(slot)
	if slot == "" {
		return nil
	}
	// 槽位版本记录作为过期写入屏障保留，防止其他设备的离线旧内容复活。
	return s.forgetLongTerm(ctx, ForgetRequest{OwnerID: ownerID, SourcePrefix: slot + ":"})
}

// SharedSourceID 计算槽位内容的来源 ID。
//
// 内容寻址：同样的内容在两端得到同一个 ID，云端按 ID 去重。
func SharedSourceID(slot, text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return slot + ":" + hex.EncodeToString(sum[:])[:12]
}

// SetSharedSlots 保留槽位仓储装配入口。
// 实际的槽位读取与替换由 Native 或云端后端在事务内完成。
func (s *Service) SetSharedSlots(repo *store.SharedMemorySlotRepo) {
	s.slots = repo
}
