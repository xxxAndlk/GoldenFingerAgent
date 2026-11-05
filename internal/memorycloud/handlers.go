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
}

type episodeResponse struct {
	SourceID string `json:"source_id"`
	Status   string `json:"status"`
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
	duplicate := false
	err := s.db.WithTx(r.Context(), func(tx *sql.Tx) error {
		txr := store.NewRepos(tx)
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
	if duplicate {
		writeJSON(w, http.StatusOK, episodeResponse{SourceID: sourceID, Status: "duplicate"})
		return
	}
	if s.wake != nil {
		s.wake()
	}
	writeJSON(w, http.StatusOK, episodeResponse{SourceID: sourceID, Status: "accepted"})
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
	if req.SourceID == "" && req.FactID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "source_id or fact_id required"})
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
