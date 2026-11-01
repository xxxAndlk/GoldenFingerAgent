// Package superagent 是 Super-agent 的事件层：Trigger 接入、去重、Thread 映射、
// 路由决策与持久事件日志。它不直接执行具体工具，Worker 仍由独立执行器承担。
package superagent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"goldenfinger/agent/internal/store"
)

var (
	ErrInvalid  = errors.New("superagent: invalid input")
	ErrConflict = errors.New("superagent: state conflict")
)

type Service struct {
	DB     *store.DB
	Now    func() time.Time
	Filter Filter
	// Workers 为空时 create_worker/route_to_worker 会确定性回退为 notify。
	Workers WorkerSink
	// Summarizer 维护渠道 Thread 的滚动摘要；为空时只保存原始事件。
	Summarizer *Summarizer
	// OnNotify 是内部通知回调（桌面 toast/工作台角标）；不负责外部发送。
	OnNotify func(ctx context.Context, ownerID string, trigger *store.Trigger, decision Decision)
}

func New(db *store.DB) *Service {
	return &Service{DB: db, Now: time.Now}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type TriggerInput struct {
	Channel          string          `json:"channel"`
	ExternalID       string          `json:"external_id,omitempty"`
	ThreadID         string          `json:"thread_id,omitempty"`
	ThreadExternalID string          `json:"thread_external_id,omitempty"`
	Kind             string          `json:"kind"`
	Title            string          `json:"title,omitempty"`
	Body             string          `json:"body,omitempty"`
	Actor            json.RawMessage `json:"actor,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	ReceivedAt       time.Time       `json:"received_at,omitempty"`
	DedupeKey        string          `json:"dedupe_key,omitempty"`
	PrivacyLevel     string          `json:"privacy_level,omitempty"`
	Trusted          bool            `json:"trusted,omitempty"`
}

type IngestResult struct {
	Trigger  *store.Trigger `json:"trigger"`
	Thread   *store.Thread  `json:"thread,omitempty"`
	Created  bool           `json:"created"`
	Filtered bool           `json:"filtered"`
	Reason   string         `json:"reason,omitempty"`
}

func (s *Service) Ingest(ctx context.Context, ownerID string, in TriggerInput) (*IngestResult, error) {
	if s.DB == nil {
		return nil, errors.New("superagent: database not configured")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, fmt.Errorf("%w: owner required", ErrInvalid)
	}
	in.Channel = strings.TrimSpace(in.Channel)
	in.Kind = strings.TrimSpace(in.Kind)
	in.ExternalID = strings.TrimSpace(in.ExternalID)
	in.ThreadID = strings.TrimSpace(in.ThreadID)
	in.ThreadExternalID = strings.TrimSpace(in.ThreadExternalID)
	in.Title = strings.TrimSpace(in.Title)
	in.Body = strings.TrimSpace(in.Body)
	in.PrivacyLevel = strings.TrimSpace(in.PrivacyLevel)
	if in.Channel == "" || in.Kind == "" {
		return nil, fmt.Errorf("%w: channel and kind required", ErrInvalid)
	}
	if utf8.RuneCountInString(in.Channel) > 40 || utf8.RuneCountInString(in.Kind) > 40 {
		return nil, fmt.Errorf("%w: channel or kind too long", ErrInvalid)
	}
	if len(in.ExternalID) > 300 || len(in.ThreadExternalID) > 300 {
		return nil, fmt.Errorf("%w: external id too long", ErrInvalid)
	}
	if len(in.Title) > 2000 || len(in.Body) > 64<<10 {
		return nil, fmt.Errorf("%w: content too large", ErrInvalid)
	}
	if in.Title == "" && in.Body == "" {
		return nil, fmt.Errorf("%w: title or body required", ErrInvalid)
	}
	if len(in.DedupeKey) > 300 {
		return nil, fmt.Errorf("%w: dedupe_key too long", ErrInvalid)
	}
	receivedAt := in.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = s.now()
	}
	if in.DedupeKey == "" {
		in.DedupeKey = dedupeKey(in, receivedAt)
	}

	filtered, reason := s.Filter.Check(in)
	result := &IngestResult{Filtered: filtered, Reason: reason}

	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		repos := store.NewRepos(tx)
		thread, err := resolveThread(ctx, repos, ownerID, in, s.now())
		if err != nil {
			return err
		}
		trigger := &store.Trigger{
			OwnerUserID:  ownerID,
			Channel:      in.Channel,
			ExternalID:   in.ExternalID,
			ThreadID:     "",
			Actor:        in.Actor,
			Kind:         in.Kind,
			Title:        in.Title,
			Body:         in.Body,
			Payload:      in.Payload,
			ReceivedAt:   receivedAt,
			DedupeKey:    in.DedupeKey,
			PrivacyLevel: in.PrivacyLevel,
			Trusted:      in.Trusted,
			Status:       store.TriggerStatusNew,
		}
		if thread != nil {
			trigger.ThreadID = thread.ID
		}
		created, existing, err := repos.Triggers.Create(ctx, trigger)
		if err != nil {
			return err
		}
		if !created {
			result.Trigger = existing
			result.Created = false
			if existing.ThreadID != "" {
				result.Thread, _ = repos.Threads.Get(ctx, ownerID, existing.ThreadID)
			}
			return nil
		}
		result.Trigger = trigger
		result.Thread = thread
		result.Created = true

		if err := appendEvent(ctx, repos, ownerID, "trigger.created", "trigger", trigger.ID, map[string]any{
			"channel": trigger.Channel,
			"kind":    trigger.Kind,
			"status":  store.TriggerStatusNew,
		}); err != nil {
			return err
		}

		targetStatus := store.TriggerStatusQueued
		routeResult := json.RawMessage(`{}`)
		if filtered {
			targetStatus = store.TriggerStatusIgnored
			routeResult = mustJSON(map[string]any{"action": "ignore", "reason": reason})
		}
		changed, err := repos.Triggers.Transition(ctx, ownerID, trigger.ID,
			[]string{store.TriggerStatusNew}, targetStatus, routeResult, "", s.now())
		if err != nil {
			return err
		}
		if !changed {
			return ErrConflict
		}
		trigger.Status = targetStatus
		trigger.RouteResult = routeResult
		trigger.UpdatedAt = s.now()

		eventKind := "trigger.queued"
		if filtered {
			eventKind = "trigger.ignored"
		}
		if err := appendEvent(ctx, repos, ownerID, eventKind, "trigger", trigger.ID, map[string]any{
			"status": targetStatus,
			"reason": reason,
		}); err != nil {
			return err
		}
		if thread != nil {
			return repos.Threads.Touch(ctx, ownerID, thread.ID, receivedAt)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if result.Created && s.Summarizer != nil && result.Trigger != nil &&
		result.Trigger.ThreadID != "" && shouldSummarizeThread(result.Trigger.Channel) {
		s.Summarizer.Enqueue(ownerID, result.Trigger.ThreadID)
	}
	return result, nil
}

func resolveThread(ctx context.Context, repos *store.Repos, ownerID string, in TriggerInput, now time.Time) (*store.Thread, error) {
	if in.ThreadID != "" {
		thread, err := repos.Threads.Get(ctx, ownerID, in.ThreadID)
		if err != nil {
			return nil, fmt.Errorf("%w: thread not found", ErrInvalid)
		}
		return thread, nil
	}
	if in.ThreadExternalID == "" {
		return nil, nil
	}
	thread, err := repos.Threads.GetByExternal(ctx, ownerID, in.Channel, in.ThreadExternalID)
	if err == nil {
		return thread, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	thread = &store.Thread{
		OwnerUserID: ownerID,
		Channel:     in.Channel,
		ExternalID:  in.ThreadExternalID,
		Title:       in.Title,
		Status:      store.ThreadStatusActive,
	}
	if err := repos.Threads.Create(ctx, thread); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return repos.Threads.GetByExternal(ctx, ownerID, in.Channel, in.ThreadExternalID)
		}
		return nil, err
	}
	return thread, nil
}

func appendEvent(ctx context.Context, repos *store.Repos, ownerID, kind, entityType, entityID string, payload any) error {
	_, err := appendEventRecord(ctx, repos, ownerID, kind, entityType, entityID, payload)
	return err
}

func appendEventRecord(ctx context.Context, repos *store.Repos, ownerID, kind, entityType, entityID string, payload any) (*store.AgentEvent, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	event := &store.AgentEvent{
		OwnerUserID: ownerID,
		Kind:        kind,
		EntityType:  entityType,
		EntityID:    entityID,
		Payload:     raw,
	}
	if _, err := repos.AgentEvents.Append(ctx, event); err != nil {
		return nil, err
	}
	return event, nil
}

func dedupeKey(in TriggerInput, at time.Time) string {
	if in.ExternalID != "" {
		return in.Channel + ":" + in.ExternalID
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s", in.Channel, in.Kind, in.ThreadExternalID, in.Title, in.Body)
	bucket := at.Truncate(5 * time.Minute).UTC().Format(time.RFC3339)
	fmt.Fprintf(h, "\x00%s", bucket)
	return in.Channel + ":hash:" + hex.EncodeToString(h.Sum(nil))[:32]
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// ListThreads 返回 owner 的 Thread，可选渠道过滤。
func (s *Service) ListThreads(ctx context.Context, ownerID, channel string, limit int) ([]store.Thread, error) {
	return s.DB.Threads().List(ctx, ownerID, channel, limit)
}

func (s *Service) GetThread(ctx context.Context, ownerID, id string) (*store.Thread, []store.Trigger, []store.ThreadLink, error) {
	thread, err := s.DB.Threads().Get(ctx, ownerID, id)
	if err != nil {
		return nil, nil, nil, err
	}
	triggers, err := s.DB.Triggers().List(ctx, ownerID, "", id, 100)
	if err != nil {
		return nil, nil, nil, err
	}
	links, err := s.DB.ThreadLinks().List(ctx, ownerID, id)
	if err != nil {
		return nil, nil, nil, err
	}
	return thread, triggers, links, nil
}

func (s *Service) ListTriggers(ctx context.Context, ownerID, status, threadID string, limit int) ([]store.Trigger, error) {
	return s.DB.Triggers().List(ctx, ownerID, status, threadID, limit)
}

func (s *Service) ListEvents(ctx context.Context, ownerID string, afterID int64, limit int) ([]store.AgentEvent, error) {
	return s.DB.AgentEvents().After(ctx, ownerID, afterID, limit)
}

type Health struct {
	Enabled       bool           `json:"enabled"`
	Triggers      map[string]int `json:"triggers"`
	Events        map[string]int `json:"events"`
	WorkerTasks   int            `json:"worker_tasks"`
	Outbox        map[string]int `json:"outbox"`
	LatestEventID int64          `json:"latest_event_id"`
}

func (s *Service) Health(ctx context.Context, ownerID string) (*Health, error) {
	triggers, err := s.DB.Triggers().CountByStatus(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	events, err := s.DB.AgentEvents().Counts(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	outbox, err := outboxCounts(ctx, s.DB, ownerID)
	if err != nil {
		return nil, err
	}
	var workers int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_job
		WHERE owner_user_id = ? AND (thread_id <> '' OR parent_trigger_id <> '')`, ownerID).Scan(&workers); err != nil {
		return nil, err
	}
	latest, err := s.DB.AgentEvents().LatestID(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	return &Health{
		Enabled:       true,
		Triggers:      triggers,
		Events:        events,
		WorkerTasks:   workers,
		Outbox:        outbox,
		LatestEventID: latest,
	}, nil
}

func outboxCounts(ctx context.Context, db *store.DB, ownerID string) (map[string]int, error) {
	rows, err := db.QueryContext(ctx, `SELECT status, COUNT(*) FROM agent_action_outbox
		WHERE owner_user_id = ? GROUP BY status`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
	}
	return out, rows.Err()
}
