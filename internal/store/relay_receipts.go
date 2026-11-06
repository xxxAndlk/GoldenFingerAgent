package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// 电脑端中继指令回执的状态。
const (
	// RelayReceiptRunning 表示指令已经开始执行但还没有结果。
	// 只剩这条记录说明执行进程中途退出了，结果未知。
	RelayReceiptRunning = "running"
	// RelayReceiptDone 表示指令已完成，结果已经保存。
	RelayReceiptDone = "done"
)

// RelayReceiptRepo 保存电脑端执行过的中继指令回执。
//
// 内存去重在电脑重启后失效，同一指令会再次进入业务接口。
// 这里按幂等键落库，让「最多执行一次」在重启后依然成立。
type RelayReceiptRepo struct{ q Querier }

func NewRelayReceiptRepo(q Querier) *RelayReceiptRepo { return &RelayReceiptRepo{q: q} }

// Lookup 查这条指令之前的回执。found=false 表示第一次执行。
func (r *RelayReceiptRepo) Lookup(ctx context.Context, key string) (status string, resultJSON []byte, found bool, err error) {
	var raw []byte
	err = r.q.QueryRowContext(ctx, `
		SELECT status, result_json FROM relay_command_receipt WHERE idempotency_key = ?`, key).
		Scan(&status, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return status, raw, true, nil
}

// Begin 标记指令已经开始执行。
//
// 记录写在真正调用业务接口之前：进程中途退出时这条 running 记录
// 就是「结果未知」的证据，重投时不能盲目再执行一遍。
func (r *RelayReceiptRepo) Begin(ctx context.Context, key string, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO relay_command_receipt (idempotency_key, status, result_json, created_at, updated_at)
		VALUES (?, ?, '{}', ?, ?)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		key, RelayReceiptRunning, at, at)
	return err
}

// Complete 记录执行结果，供重投时原样回给手机。
func (r *RelayReceiptRepo) Complete(ctx context.Context, key string, resultJSON []byte, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO relay_command_receipt (idempotency_key, status, result_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (idempotency_key) DO UPDATE SET
			status = excluded.status, result_json = excluded.result_json, updated_at = excluded.updated_at`,
		key, RelayReceiptDone, string(notNullJSON(resultJSON)), at, at)
	return err
}

// Prune 删掉早于给定时间的回执，避免表无限增长。
// 回执只用于「重投时不要执行第二遍」，过期后重投的风险可以接受。
func (r *RelayReceiptRepo) Prune(ctx context.Context, before time.Time) error {
	_, err := r.q.ExecContext(ctx,
		`DELETE FROM relay_command_receipt WHERE updated_at < ?`, before)
	return err
}
