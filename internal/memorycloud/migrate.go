package memorycloud

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"goldenfinger/agent/internal/store"
)

// ErrCloudNotEmpty 表示云端已存在该 owner 的记忆，不做自动对撞导入。
var ErrCloudNotEmpty = errors.New("memorycloud: 云端已存在该 owner 的记忆，请先备份并清空后再导入")

// ExportFile 是旧记忆迁移文件的稳定格式。
type ExportFile struct {
	SchemaVersion int             `json:"schema_version"`
	OwnerUserID   string          `json:"owner_user_id"`
	ExportedAt    time.Time       `json:"exported_at"`
	Episodes      []ExportEpisode `json:"episodes"`
	Edges         []ExportEdge    `json:"edges"`
}

type ExportEpisode struct {
	SourceID      string    `json:"source_id"`
	Text          string    `json:"text"`
	Source        string    `json:"source"`
	Role          string    `json:"role"`
	ReferenceTime time.Time `json:"reference_time"`
	Status        string    `json:"status"`
	Attempts      int       `json:"attempts"`
	Embedding     []float32 `json:"embedding,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type ExportEdge struct {
	ID            string     `json:"id"`
	Subject       string     `json:"subject"`
	Relation      string     `json:"relation"`
	Object        string     `json:"object"`
	Fact          string     `json:"fact"`
	ValidFrom     *time.Time `json:"valid_from,omitempty"`
	ValidTo       *time.Time `json:"valid_to,omitempty"`
	SourceRefs    []string   `json:"source_refs"`
	Embedding     []float32  `json:"embedding,omitempty"`
	Status        string     `json:"status"`
	ScopeType     string     `json:"scope_type"`
	ScopeID       string     `json:"scope_id"`
	ConditionText string     `json:"condition_text"`
	Confidence    float64    `json:"confidence"`
	Version       int        `json:"version"`
	ConfirmedBy   string     `json:"confirmed_by"`
	ConfirmedAt   *time.Time `json:"confirmed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// ImportStats 汇总一次导入的结果。
type ImportStats struct {
	Episodes       int  `json:"episodes"`
	Edges          int  `json:"edges"`
	VectorsRebuilt bool `json:"vectors_rebuilt"`
}

// Export 从本地库导出某个 owner 的原始片段与事实边。
func Export(ctx context.Context, db *store.DB, ownerID string) (*ExportFile, error) {
	if db == nil || ownerID == "" {
		return nil, errors.New("memorycloud: export 需要数据库与 owner")
	}
	repos := store.NewRepos(db)
	episodes, err := repos.LongTerm.ListEpisodes(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	edges, err := repos.LongTerm.Edges(ctx, ownerID, 100000)
	if err != nil {
		return nil, err
	}
	file := &ExportFile{
		SchemaVersion: 1, OwnerUserID: ownerID, ExportedAt: time.Now(),
		Episodes: make([]ExportEpisode, 0, len(episodes)),
		Edges:    make([]ExportEdge, 0, len(edges)),
	}
	for _, ep := range episodes {
		file.Episodes = append(file.Episodes, ExportEpisode{
			SourceID: ep.SourceID, Text: ep.Text, Source: ep.Source, Role: ep.Role,
			ReferenceTime: ep.ReferenceTime, Status: ep.Status, Attempts: ep.Attempts,
			Embedding: ep.Embedding, CreatedAt: ep.CreatedAt,
		})
	}
	for _, edge := range edges {
		file.Edges = append(file.Edges, ExportEdge{
			ID: edge.ID, Subject: edge.Subject, Relation: edge.Relation, Object: edge.Object,
			Fact: edge.Fact, ValidFrom: edge.ValidFrom, ValidTo: edge.ValidTo,
			SourceRefs: edge.SourceRefs, Embedding: edge.Embedding, Status: edge.Status,
			ScopeType: edge.ScopeType, ScopeID: edge.ScopeID, ConditionText: edge.ConditionText,
			Confidence: edge.Confidence, Version: edge.Version, ConfirmedBy: edge.ConfirmedBy,
			ConfirmedAt: edge.ConfirmedAt, CreatedAt: edge.CreatedAt, UpdatedAt: edge.UpdatedAt,
		})
	}
	return file, nil
}

// Import 在单个事务中把迁移文件写入云端库。
// dim 是云端 Embedder 维度：不一致时丢弃旧向量并让片段重新抽取，
// 避免把不同模型的向量混进同一索引。
func Import(ctx context.Context, db *store.DB, file *ExportFile, dim int) (ImportStats, error) {
	var stats ImportStats
	if db == nil || file == nil {
		return stats, errors.New("memorycloud: import 需要数据库与迁移文件")
	}
	if file.SchemaVersion != 1 {
		return stats, fmt.Errorf("memorycloud: 不支持的 schema_version=%d", file.SchemaVersion)
	}
	owner := file.OwnerUserID
	if owner == "" {
		return stats, errors.New("memorycloud: 迁移文件缺少 owner_user_id")
	}
	seen := make(map[string]bool, len(file.Episodes))
	for i, ep := range file.Episodes {
		if ep.SourceID == "" || ep.Text == "" {
			return stats, fmt.Errorf("memorycloud: 第 %d 条片段缺少 source_id/text", i+1)
		}
		if seen[ep.SourceID] {
			return stats, fmt.Errorf("memorycloud: 片段 source_id 重复: %s", ep.SourceID)
		}
		seen[ep.SourceID] = true
	}
	for i, edge := range file.Edges {
		for _, ref := range edge.SourceRefs {
			if !seen[ref] {
				return stats, fmt.Errorf("memorycloud: 第 %d 条事实引用了不存在的片段 %s", i+1, ref)
			}
		}
	}

	rebuild := false
	if dim > 0 {
		for _, edge := range file.Edges {
			if len(edge.Embedding) > 0 && len(edge.Embedding) != dim {
				rebuild = true
				break
			}
		}
	}

	err := db.WithTx(ctx, func(tx *sql.Tx) error {
		txr := store.NewRepos(tx)
		existing, err := txr.LongTerm.ListEpisodes(ctx, owner)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return ErrCloudNotEmpty
		}
		storedRefs := make(map[string]string, len(file.Episodes))
		for _, ep := range file.Episodes {
			stored := StoredSourceID(owner, ep.SourceID)
			storedRefs[ep.SourceID] = stored
			status := ep.Status
			if status == "" {
				status = store.LongTermStatusDone
			}
			var embedding []float32
			if !rebuild {
				embedding = ep.Embedding
			}
			if rebuild {
				status = store.LongTermStatusPending
			}
			if err := txr.LongTerm.ImportEpisode(ctx, &store.LongTermEpisode{
				SourceID: stored, OwnerUserID: owner, Text: ep.Text, Source: ep.Source,
				Role: firstNonEmptyRole(ep.Role), ReferenceTime: ep.ReferenceTime,
				Status: status, Attempts: ep.Attempts, Embedding: embedding, CreatedAt: ep.CreatedAt,
			}); err != nil {
				return err
			}
			if err := txr.CloudEpisodes.Insert(ctx, &store.CloudEpisodeMeta{
				OwnerUserID: owner, OriginalSourceID: ep.SourceID, StoredSourceID: stored,
				DeviceID: "migration", ContentHash: contentHash(ep.Text), CreatedAt: ep.CreatedAt,
			}); err != nil {
				return err
			}
			stats.Episodes++
		}
		if rebuild {
			stats.VectorsRebuilt = true
			return nil
		}
		for _, edge := range file.Edges {
			refs := make([]string, 0, len(edge.SourceRefs))
			for _, ref := range edge.SourceRefs {
				refs = append(refs, storedRefs[ref])
			}
			scopeType := edge.ScopeType
			if scopeType == "" {
				scopeType = "global"
			}
			status := edge.Status
			if status == "" {
				status = store.MemoryInferred
			}
			if err := txr.LongTerm.InsertEdge(ctx, &store.LongTermEdge{
				ID: uuid.NewString(), OwnerUserID: owner, Subject: edge.Subject,
				Relation: edge.Relation, Object: edge.Object, Fact: edge.Fact,
				ValidFrom: edge.ValidFrom, ValidTo: edge.ValidTo, SourceRefs: refs,
				Embedding: edge.Embedding, Status: status, ScopeType: scopeType,
				ScopeID: edge.ScopeID, ConditionText: edge.ConditionText,
				Confidence: edge.Confidence, Version: edge.Version, ConfirmedBy: edge.ConfirmedBy,
				ConfirmedAt: edge.ConfirmedAt,
			}); err != nil {
				return err
			}
			stats.Edges++
		}
		return nil
	})
	if err != nil {
		return ImportStats{}, err
	}
	return stats, nil
}

func firstNonEmptyRole(role string) string {
	if role == "" {
		return "owner"
	}
	return role
}

// MarshalExport 提供稳定的 JSON 输出（缩进便于人工检查）。
func MarshalExport(file *ExportFile) ([]byte, error) {
	return json.MarshalIndent(file, "", "  ")
}
