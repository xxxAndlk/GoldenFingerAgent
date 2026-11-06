package relay

import (
	"context"
	"time"
)

// 中继指令回执的状态。
const (
	// CommandReceiptRunning 表示指令已经开始执行但没有结果。
	// 只剩这条记录说明上一次执行被中断，结果未知。
	CommandReceiptRunning = "running"
	// CommandReceiptDone 表示指令已完成，结果已保存。
	CommandReceiptDone = "done"
)

// CommandLedger 是电脑端持久化的中继指令回执仓库。
//
// 内存去重只在进程存活期间有效：电脑重启或去重窗口淘汰后，
// 同一指令会再次进入业务接口，任务状态、采集配置等路径无法保证
// 「最多执行一次」。落库的回执让这条约束在重启后依然成立。
//
// 实现方只负责读写，结果用 JSON 存放，避免 relay 依赖具体存储包。
type CommandLedger interface {
	// Lookup 查这条指令之前的回执。found=false 表示第一次执行。
	Lookup(ctx context.Context, key string) (status string, resultJSON []byte, found bool, err error)
	// Begin 标记指令已经开始执行。执行进程中途退出会留下这条记录。
	Begin(ctx context.Context, key string, at time.Time) error
	// Complete 记录执行结果，供重投时原样回给手机。
	Complete(ctx context.Context, key string, resultJSON []byte, at time.Time) error
}
