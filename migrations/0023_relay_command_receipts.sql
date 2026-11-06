-- 0023_relay_command_receipts.sql — 电脑端中继指令的持久化回执。
--
-- 内存去重在电脑重启或窗口淘汰后失效：同一指令可能再次进入业务接口，
-- 任务状态、采集配置等路径无法统一保证「最多执行一次」。
--
-- 这里按幂等键保存执行结果：
--   - done：已完成，重投时原样返回上次的结果，不再执行一遍；
--   - running：只留下「已经开始」的痕迹（进程中途退出），
--     重投时按 unknown 回执，交给用户核对，不盲目重跑。

CREATE TABLE IF NOT EXISTS relay_command_receipt (
    idempotency_key TEXT PRIMARY KEY,
    status          TEXT NOT NULL CHECK (status IN ('running', 'done')),
    result_json     TEXT NOT NULL DEFAULT '{}',
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL
);
