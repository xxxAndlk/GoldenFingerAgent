package superagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"goldenfinger/agent/internal/store"
)

// TaskIndexEntry 是 Router 可见的只读任务候选。禁止模型指定列表外的 Task。
type TaskIndexEntry struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Goal          string   `json:"goal,omitempty"`
	Status        string   `json:"status"`
	Version       int      `json:"version"`
	Stage         string   `json:"stage,omitempty"`
	NextAction    string   `json:"next_action,omitempty"`
	WaitingKind   string   `json:"waiting_kind,omitempty"`
	ThreadIDs     []string `json:"thread_ids,omitempty"`
	CurrentJobID  string   `json:"current_job_id,omitempty"`
	Profile       string   `json:"profile,omitempty"`
	ArtifactTitle string   `json:"artifact_title,omitempty"`
}

func (s *Service) TaskIndex(ctx context.Context, ownerID string) ([]TaskIndexEntry, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("superagent: database not configured")
	}
	repos := store.NewRepos(s.DB)
	tasks, err := repos.WorkTasks.ListCandidates(ctx, ownerID, 20)
	if err != nil {
		return nil, err
	}
	out := make([]TaskIndexEntry, 0, len(tasks))
	for _, task := range tasks {
		entry := TaskIndexEntry{
			ID: task.ID, Title: task.Title, Goal: truncateRunes(task.Goal, 800),
			Status: task.Status, Version: task.Version, CurrentJobID: task.CurrentJobID,
			Profile: task.Profile,
		}
		if live, err := repos.WorkTaskLive.Get(ctx, ownerID, task.ID); err == nil {
			entry.Stage = truncateRunes(live.Stage, 200)
			entry.NextAction = truncateRunes(live.NextAction, 500)
			entry.WaitingKind = live.WaitingKind
		}
		if links, err := repos.WorkTaskThreads.ListByTask(ctx, ownerID, task.ID); err == nil {
			for _, link := range links {
				entry.ThreadIDs = append(entry.ThreadIDs, link.ThreadID)
			}
		}
		if artifacts, err := repos.Artifacts.ListByTask(ctx, ownerID, task.ID); err == nil && len(artifacts) > 0 {
			entry.ArtifactTitle = truncateRunes(artifacts[0].Title, 200)
		}
		out = append(out, entry)
	}
	return out, nil
}

func (s *Service) taskIndexContains(ctx context.Context, ownerID, taskID string) (*TaskIndexEntry, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, nil
	}
	entries, err := s.TaskIndex(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].ID == taskID {
			return &entries[i], nil
		}
	}
	return nil, nil
}

func marshalTaskIndex(entries []TaskIndexEntry) string {
	raw, err := json.Marshal(entries)
	if err != nil {
		return "[]"
	}
	return string(raw)
}
