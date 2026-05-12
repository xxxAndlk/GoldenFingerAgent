"""TUI 常量：所有魔法数字的命名常量。"""

# ---- 聊天循环 ----
MAX_CHAT_ROUNDS = 6
INPUT_RECOVERY_TIMEOUT_SEC = 45

# ---- 上下文压缩阈值 ----
CHAT_MEMORY_TURN_LIMIT = 10     # 保留的对话轮数（每轮 user + assistant）
CHAT_MEMORY_L2_THRESHOLD = 12   # 消息条数：触发 L2 模型压缩
CHAT_MEMORY_L3_THRESHOLD = 20   # 消息条数：触发 L3 工具式压缩
CHAT_MEMORY_KEEP_RECENT = 8     # 压缩时保留的最近消息数

# ---- 双击时间窗口 ----
DOUBLE_PRESS_WINDOW_SEC = 1.0

# ---- ChromaDB ----
CHROMADB_WARMUP_TIMEOUT_SEC = 120

# ---- 显示截断 ----
REASONING_TRUNCATE_LEN = 800
RESULT_PREVIEW_LEN = 200

# ---- RichLog ----
CHAT_LOG_MAX_LINES = 10_000

# ---- 思考动画 ----
THINKING_TRACE_INTERVAL_SEC = 1.2
THINKING_PHASES = ["解析问题", "规划方案", "组织回复"]

# ---- 流水线阶段图标 ----
PIPELINE_ICONS: dict[str, str] = {
    "analysis": "🔮 天机推演",
    "execution": "⚡ 施法执行",
    "verification": "🔍 验道校验",
    "persistence": "📝 刻碑沉淀",
}

# ---- 防抖 ----
UI_DEBOUNCE_SEC = 0.05
