package memorycloud

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/store"
)

const timeFormat = time.RFC3339Nano

func parseTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errors.New("invalid time")
}

var (
	errEpisodeConflict  = errors.New("source_id conflict")
	errEpisodeForgotten = errors.New("source forgotten")
)

// StoredSourceID 为不同 owner 的相同本地 SourceID 隔离命名空间。
func StoredSourceID(ownerID, original string) string {
	return "cloud:" + ownerID + ":" + original
}

func contentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func normalizeHash(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "sha256:")
	return strings.ToLower(value)
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "owner":
		return "owner"
	case "contact", "system":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return ""
	}
}

type episodeRequest struct {
	SourceID      string `json:"source_id"`
	Text          string `json:"text"`
	Source        string `json:"source"`
	Role          string `json:"role"`
	ReferenceTime string `json:"reference_time"`
	ContentHash   string `json:"content_hash"`
	// Slot 是跨端共享内容的位置（简报日期、联系人备注等）。
	// 同一个槽位只保留最新一版，云端在事务里替换旧版本。
	Slot string `json:"slot"`
	// SlotUpdatedAt 是写入方的写入时间（毫秒）：云端据此判断新旧，
	// 旧请求恢复后不能覆盖更新的内容。
	SlotUpdatedAt int64 `json:"slot_updated_at"`
}

type episodeResponse struct {
	SourceID string `json:"source_id"`
	Status   string `json:"status"`
	// Revision 是槽位当前版本号；非槽位写入为 0。
	Revision int `json:"revision,omitempty"`
	// ServerTime 是云端时间（毫秒）：端侧据此校正自己的时钟偏差。
	ServerTime int64 `json:"server_time,omitempty"`
}

func (s *Server) handleEpisode(w http.ResponseWriter, r *http.Request) {
	device, ok := s.requireScope(w, r, ScopeMemoryWrite)
	if !ok {
		return
	}
	var req episodeRequest
	limited := http.MaxBytesReader(w, r.Body, int64(s.maxEpisodeBytes)+4<<10)
	if err := json.NewDecoder(limited).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	sourceID := strings.TrimSpace(req.SourceID)
	text := req.Text
	if sourceID == "" || strings.TrimSpace(text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source_id and text are required"})
		return
	}
	if len(sourceID) > 512 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source_id too long"})
		return
	}
	if !utf8.ValidString(text) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text must be utf-8"})
		return
	}
	if len(text) > s.maxEpisodeBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "text too large"})
		return
	}
	role := normalizeRole(req.Role)
	if role == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role must be owner, contact or system"})
		return
	}
	hash := contentHash(text)
	if req.ContentHash != "" && normalizeHash(req.ContentHash) != hash {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "content_hash mismatch"})
		return
	}
	at := s.now()
	if strings.TrimSpace(req.ReferenceTime) != "" {
		parsed, err := parseTime(req.ReferenceTime)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid reference_time"})
			return
		}
		at = parsed
	}

	ownerID := device.OwnerUserID
	stored := StoredSourceID(ownerID, sourceID)
	slot := strings.TrimSpace(req.Slot)
	duplicate := false
	stale := false
	revision := 0
	err := s.db.WithTx(r.Context(), func(tx *sql.Tx) error {
		txr := store.NewRepos(tx)
		if slot != "" {
			// 共享槽位：同一位置只保留最新一版。
			// 旧请求（写入时间更早）不能覆盖新内容，直接判为过期。
			previous, isStale, err := txr.SharedSlots.Apply(r.Context(), ownerID, slot, sourceID,
				req.SlotUpdatedAt, s.now())
			if err != nil {
				return err
			}
			if isStale {
				stale = true
				return nil
			}
			revision = previous.Revision + 1
			if previous.SourceID != "" && previous.SourceID != sourceID {
				// 旧版本连同派生事实一起删掉：不能新旧两条同时留在云端。
				previousStored := StoredSourceID(ownerID, previous.SourceID)
				if _, err := txr.LongTerm.DeleteEdgesBySource(r.Context(), ownerID, previousStored); err != nil {
					return err
				}
				if err := txr.LongTerm.DeleteEpisode(r.Context(), previousStored); err != nil {
					return err
				}
				// 映射一并删除（不留墓碑）：槽位内容以后可能被重新写入，
				// 留着墓碑会让同一份内容再也传不上去。
				if err := txr.CloudEpisodes.Delete(r.Context(), ownerID, previous.SourceID); err != nil {
					return err
				}
			}
		}
		meta, err := txr.CloudEpisodes.GetByOriginal(r.Context(), ownerID, sourceID)
		if err == nil {
			if meta.ForgottenAt != nil {
				return errEpisodeForgotten
			}
			if meta.ContentHash == hash {
				duplicate = true
				return nil
			}
			return errEpisodeConflict
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := txr.CloudEpisodes.Insert(r.Context(), &store.CloudEpisodeMeta{
			OwnerUserID: ownerID, OriginalSourceID: sourceID, StoredSourceID: stored,
			DeviceID: device.ID, ContentHash: hash, CreatedAt: s.now(),
		}); err != nil {
			return err
		}
		_, err = txr.LongTerm.InsertEpisode(r.Context(), &store.LongTermEpisode{
			SourceID: stored, OwnerUserID: ownerID, Text: text, Source: strings.TrimSpace(req.Source),
			Role: role, ReferenceTime: at,
		})
		return err
	})
	switch {
	case errors.Is(err, errEpisodeForgotten):
		writeJSON(w, http.StatusGone, map[string]string{"error": "source forgotten"})
		return
	case errors.Is(err, errEpisodeConflict), errors.Is(err, store.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "source_id conflict"})
		return
	case err != nil:
		s.logf("[memorycloud] ingest failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "ingest failed"})
		return
	}
	if stale {
		// 旧请求：云端保留更新的版本，如实告诉端侧「这条已过期」。
		writeJSON(w, http.StatusOK, episodeResponse{
			SourceID: sourceID, Status: "stale", Revision: revision, ServerTime: s.now().UnixMilli(),
		})
		return
	}
	if duplicate {
		writeJSON(w, http.StatusOK, episodeResponse{
			SourceID: sourceID, Status: "duplicate", Revision: revision, ServerTime: s.now().UnixMilli(),
		})
		return
	}
	if s.wake != nil {
		s.wake()
	}
	writeJSON(w, http.StatusOK, episodeResponse{
		SourceID: sourceID, Status: "accepted", Revision: revision, ServerTime: s.now().UnixMilli(),
	})
}

type searchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type factDTO struct {
	ID         string   `json:"id"`
	Text       string   `json:"text"`
	ValidAt    *string  `json:"valid_at,omitempty"`
	InvalidAt  *string  `json:"invalid_at,omitempty"`
	SourceRefs []string `json:"source_refs,omitempty"`
	Status     string   `json:"status"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	device, ok := s.requireScope(w, r, ScopeMemoryRead)
	if !ok {
		return
	}
	if s.backend == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "memory backend unavailable"})
		return
	}
	var req searchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query required"})
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 8
	}
	if limit > 32 {
		limit = 32
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	facts, err := s.backend.Search(ctx, memory.MemoryQuery{
		OwnerID: device.OwnerUserID, Query: req.Query, Limit: limit,
	})
	if err != nil {
		s.logf("[memorycloud] search failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "search failed"})
		return
	}
	refs := make([]string, 0, len(facts))
	for _, fact := range facts {
		refs = append(refs, fact.SourceRefs...)
	}
	mapping, err := s.repos.CloudEpisodes.MapStoredToOriginal(r.Context(), device.OwnerUserID, refs)
	if err != nil {
		s.logf("[memorycloud] source map failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "search failed"})
		return
	}
	out := make([]factDTO, 0, len(facts))
	for _, fact := range facts {
		dto := factDTO{ID: fact.ID, Text: fact.Text, Status: fact.Status}
		if fact.ValidAt != nil {
			value := fact.ValidAt.Format(timeFormat)
			dto.ValidAt = &value
		}
		if fact.InvalidAt != nil {
			value := fact.InvalidAt.Format(timeFormat)
			dto.InvalidAt = &value
		}
		for _, ref := range fact.SourceRefs {
			if original, ok := mapping[ref]; ok {
				dto.SourceRefs = append(dto.SourceRefs, original)
			} else {
				dto.SourceRefs = append(dto.SourceRefs, ref)
			}
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"facts": out})
}

type forgetRequest struct {
	SourceID string `json:"source_id"`
	FactID   string `json:"fact_id"`
	// SourcePrefix 按来源 ID 前缀遗忘，用于共享记忆的槽位。
	SourcePrefix string `json:"source_prefix"`
}

func (s *Server) handleForget(w http.ResponseWriter, r *http.Request) {
	device, ok := s.requireScope(w, r, ScopeMemoryWrite)
	if !ok {
		return
	}
	var req forgetRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	req.SourceID = strings.TrimSpace(req.SourceID)
	req.FactID = strings.TrimSpace(req.FactID)
	req.SourcePrefix = strings.TrimSpace(req.SourcePrefix)
	if req.SourceID == "" && req.FactID == "" && req.SourcePrefix == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source_id, source_prefix or fact_id required"})
		return
	}
	if req.FactID != "" {
		if s.backend == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "memory backend unavailable"})
			return
		}
		err := s.backend.Forget(r.Context(), memory.ForgetRequest{
			OwnerID: device.OwnerUserID, FactID: req.FactID,
		})
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeJSON(w, http.StatusOK, map[string]string{"status": "not_found"})
		case err != nil:
			s.logf("[memorycloud] forget fact failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "forget failed"})
		default:
			writeJSON(w, http.StatusOK, map[string]string{"status": "forgotten"})
		}
		return
	}

	ownerID := device.OwnerUserID
	if req.SourcePrefix != "" {
		// 按前缀遗忘共享记忆槽位：同一位置可能有多个版本，
		// 只删一个版本会留下过期内容继续被检索到。
		err := s.db.WithTx(r.Context(), func(tx *sql.Tx) error {
			txr := store.NewRepos(tx)
			metas, err := txr.CloudEpisodes.ListByOriginalPrefix(r.Context(), ownerID, req.SourcePrefix)
			if err != nil {
				return err
			}
			for _, meta := range metas {
				if _, err := txr.LongTerm.DeleteEdgesBySource(r.Context(), ownerID, meta.StoredSourceID); err != nil {
					return err
				}
				if err := txr.LongTerm.DeleteEpisode(r.Context(), meta.StoredSourceID); err != nil {
					return err
				}
				// 彻底删除映射：槽位内容以后可能被重新写入，
				// 留着墓碑会让同一份内容再也传不上去。
				if err := txr.CloudEpisodes.Delete(r.Context(), ownerID, meta.OriginalSourceID); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			s.logf("[memorycloud] forget prefix failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "forget failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "forgotten"})
		return
	}
	stored := StoredSourceID(ownerID, req.SourceID)
	err := s.db.WithTx(r.Context(), func(tx *sql.Tx) error {
		txr := store.NewRepos(tx)
		if err := txr.CloudEpisodes.MarkForgotten(r.Context(), ownerID, req.SourceID,
			stored, device.ID, s.now()); err != nil {
			return err
		}
		if _, err := txr.LongTerm.DeleteEdgesBySource(r.Context(), ownerID, stored); err != nil {
			return err
		}
		return txr.LongTerm.DeleteEpisode(r.Context(), stored)
	})
	if err != nil {
		s.logf("[memorycloud] forget source failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "forget failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "forgotten"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	device, ok := s.requireScope(w, r, ScopeMemoryRead)
	if !ok {
		return
	}
	if s.backend == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ready": false, "backend": "cloud", "detail": "memory backend unavailable",
		})
		return
	}
	health, err := s.backend.Health(r.Context())
	if err != nil {
		s.logf("[memorycloud] health failed: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ready": false, "backend": "cloud", "detail": "health check failed",
		})
		return
	}
	pending, done, failed, countsErr := s.repos.LongTerm.EpisodeCountsByOwner(r.Context(), device.OwnerUserID)
	if countsErr != nil {
		pending, done, failed = 0, 0, 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ready": health.Ready, "backend": "cloud:" + health.Backend,
		"pending": pending, "done": done, "failed": failed,
		"detail": health.Detail,
	})
}
