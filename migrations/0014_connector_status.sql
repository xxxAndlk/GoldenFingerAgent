-- 采集连接器的运行状态心跳。桌面端定期上报，模型侧据此区分
-- “没有授权”“连接器没在跑”和“确实没有新消息”。
CREATE TABLE agent_connector_status (
    owner_user_id  TEXT NOT NULL REFERENCES app_user(id),
    channel        TEXT NOT NULL,
    state          TEXT NOT NULL,
    payload_json   TEXT NOT NULL,
    updated_at     DATETIME NOT NULL,
    PRIMARY KEY (owner_user_id, channel)
);
