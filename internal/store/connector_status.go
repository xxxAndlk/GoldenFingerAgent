package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ConnectorStatus 是采集连接器上报的运行状态。只保存状态摘要，
// 不保存正文，避免聊天内容在数据库里多留一份。
type ConnectorStatus struct {
	OwnerUserID string          `json:"owner_user_id"`
	Channel     string          `json:"channel"`
	State       string          `json:"state"`
	Payload     json.RawMessage `json:"payload"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type ConnectorStatusRepo struct{ q Querier }

func NewConnectorStatusRepo(q Querier) *ConnectorStatusRepo { return &ConnectorStatusRepo{q: q} }
func (d *DB) ConnectorStatuses() *ConnectorStatusRepo      { return NewConnectorStatusRepo(d) }

// Save 覆盖某个渠道的连接器状态。
func (r *ConnectorStatusRepo) Save(ctx context.Context, ownerID, channel, state string, payload []byte, at time.Time) error {
	channel = NormalizeChannel(channel)
	_, err := r.q.ExecContext(ctx, `INSERT INTO agent_connector_status
		(owner_user_id, channel, state, payload_json, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (owner_user_id, channel) DO UPDATE SET
		state = excluded.state, payload_json = excluded.payload_json, updated_at = excluded.updated_at`,
		ownerID, channel, state, string(notNullJSON(payload)), at)
	return err
}

// Get 返回最近一次上报的状态；从未上报时返回 ErrNotFound。
func (r *ConnectorStatusRepo) Get(ctx context.Context, ownerID, channel string) (*ConnectorStatus, error) {
	channel = NormalizeChannel(channel)
	item := &ConnectorStatus{OwnerUserID: ownerID, Channel: channel}
	var payload []byte
	err := r.q.QueryRowContext(ctx, `SELECT state, payload_json, updated_at
		FROM agent_connector_status WHERE owner_user_id = ? AND channel = ?`, ownerID, channel).
		Scan(&item.State, &payload, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	item.Payload = payload
	return item, nil
}
