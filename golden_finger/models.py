"""金手指 Agent 系统 — 核心数据模型

所有 Pydantic 模型定义，按五域划分：
- 天机推演：TaskPlan / AtomTask / TaskComplexity
- 施法执行：ExecutionReport / ToolCallLog / ExecutionContext
- 验道校验：VerificationReport / VerificationResult / RollbackPlan
- 刻碑沉淀：SkillManifest / SkillStats / ExecutionSummary / GapReport
- 内外结界：HostProfile / SpiritRoot / PhysiquePanel / Realm
"""

import uuid
from datetime import datetime, timezone
from enum import Enum
from typing import Any

from pydantic import BaseModel, Field


# ============================================================
# 通用标识
# ============================================================

def new_id() -> str:
    return uuid.uuid4().hex[:16]


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


# ============================================================
# 境界系统
# ============================================================

class RealmStage(str, Enum):
    """境界内的阶段"""
    EARLY = "初阶"
    MIDDLE = "中阶"
    HIGH = "高阶"
    PERFECT = "圆满"


class RealmLevel(int, Enum):
    """修炼境界等级"""
    MORTAL = 0        # 凡人
    QI_REFINING = 1   # 练气
    FOUNDATION = 2    # 筑基
    GOLDEN_CORE = 3   # 金丹
    NASCENT_SOUL = 4  # 元婴
    DEITY = 5         # 化神
    TRIBULATION = 6   # 渡劫
    MAHAYANA = 7      # 大乘
    TRUE_IMMORTAL = 8 # 真仙

    @property
    def display(self) -> str:
        _map = {
            0: "凡人", 1: "练气期", 2: "筑基期", 3: "金丹期",
            4: "元婴期", 5: "化神期", 6: "渡劫期", 7: "大乘期", 8: "真仙境"
        }
        return _map[self.value]


# ============================================================
# 宿主画像相关（内外结界域）
# ============================================================

class SpiritRoot(BaseModel):
    """灵根：认知风格与天赋模型"""
    metal: float = Field(default=50.0, ge=0, le=100, description="金灵根：逻辑推理/数学")
    wood: float = Field(default=50.0, ge=0, le=100, description="木灵根：创造力/发散思维")
    water: float = Field(default=50.0, ge=0, le=100, description="水灵根：语言/沟通共情")
    fire: float = Field(default=50.0, ge=0, le=100, description="火灵根：行动力/决策速度")
    earth: float = Field(default=50.0, ge=0, le=100, description="土灵根：耐心/持久力")

    @property
    def dominant(self) -> str:
        scores = {
            "metal": self.metal, "wood": self.wood,
            "water": self.water, "fire": self.fire, "earth": self.earth
        }
        return max(scores, key=lambda k: scores[k])


class PhysiquePanel(BaseModel):
    """体质面板：身体基线数据"""
    strength: float = Field(default=50.0, description="力量")
    agility: float = Field(default=50.0, description="敏捷")
    endurance: float = Field(default=50.0, description="耐力")
    vitality: float = Field(default=50.0, description="活力")
    defense: float = Field(default=50.0, description="防御（免疫力）")


class MentalState(BaseModel):
    """精神状态"""
    stability: float = Field(default=70.0, description="稳定度")
    focus: float = Field(default=70.0, description="专注度")
    mood: float = Field(default=0.0, ge=-1, le=1, description="情绪值")
    energy: float = Field(default=70.0, description="精力值")


class HostProfile(BaseModel):
    """宿主画像：金手指系统的核心数据资产"""
    host_id: str = Field(default_factory=new_id)
    soul_mark: str = Field(default_factory=new_id)
    created_at: str = Field(default_factory=now_iso)
    updated_at: str = Field(default_factory=now_iso)

    # 天赋与身体
    spirit_root: SpiritRoot = Field(default_factory=SpiritRoot)
    physique: PhysiquePanel = Field(default_factory=PhysiquePanel)

    # 境界
    realm: RealmLevel = RealmLevel.MORTAL
    realm_stage: RealmStage = RealmStage.EARLY
    realm_progress: float = Field(default=0.0, ge=0, le=100)

    # 技能
    knowledge_tree: dict[str, float] = Field(default_factory=dict)  # 领域 → 掌握度
    skill_levels: dict[str, int] = Field(default_factory=dict)       # skill名 → 等级

    # 状态
    mental: MentalState = Field(default_factory=MentalState)

    # 统计
    total_cultivation_time: int = 0         # 总修炼分钟
    total_tasks_completed: int = 0          # 已完成任务数
    breakthrough_history: list[dict[str, Any]] = Field(default_factory=list)
    interest_tags: list[str] = Field(default_factory=list)

    # 隐私设定
    privacy_level: int = Field(default=1, ge=0, le=2)


# ============================================================
# Skill 相关（刻碑沉淀域）
# ============================================================

class SkillState(str, Enum):
    DORMANT = "dormant"        # 未解锁
    UNLOCKED = "unlocked"      # 已解锁
    ACTIVE = "active"          # 激活中
    COOLDOWN = "cooldown"      # 冷却中
    OVERLOADED = "overloaded"  # 过载
    SEALED = "sealed"          # 封印


class SkillStats(BaseModel):
    """Skill 使用统计"""
    total_uses: int = 0
    success_count: int = 0
    avg_duration_ms: float = 0.0
    avg_satisfaction: float = 0.0
    last_used: str | None = None


class KnowledgeEntry(BaseModel):
    """Skill 知识条目"""
    entry_id: str = Field(default_factory=new_id)
    source_execution: str = ""        # 来源执行 ID
    context: str = ""                 # 使用场景
    pattern: str = ""                 # 成功模式
    pitfalls: str = ""                # 踩坑记录
    prompt_template: str = ""         # 提示词片段
    created_at: str = Field(default_factory=now_iso)


class TriggerCondition(BaseModel):
    """Skill 自动触发条件"""
    task_types: list[str] = Field(default_factory=list)      # code/test/debug/plan/review/learn/file_ops
    context_states: list[str] = Field(default_factory=list)   # error/multi_task/clean_slate/after_implementation/any
    domain_tags: list[str] = Field(default_factory=list)      # web/db/api/security/ai/data/infra/cli
    keyword_patterns: list[str] = Field(default_factory=list)  # 在 query 中匹配
    min_realm: RealmLevel = RealmLevel.MORTAL


class SkillManifest(BaseModel):
    """技能声明"""
    name: str
    display_name: str
    description: str = ""
    category: str = "utility"
    realm_requirement: RealmLevel = RealmLevel.MORTAL
    version: str = "0.1.0"
    dependencies: list[str] = Field(default_factory=list)
    tools_required: list[str] = Field(default_factory=list)
    created_from: str = ""            # 来源执行 ID

    # 自动触发 (superpowers 概念)
    trigger_conditions: list[TriggerCondition] = Field(default_factory=list)
    mandatory: bool = False           # 是否强制激活（不可跳过）
    trigger_priority: int = 50        # 触发优先级 (0-100)

    # 进化相关
    knowledge: list[KnowledgeEntry] = Field(default_factory=list)
    stats: SkillStats = Field(default_factory=SkillStats)
    state: SkillState = SkillState.UNLOCKED
    level: int = 0
    experience: int = 0


# ============================================================
# 天机推演域
# ============================================================

class TaskComplexity(str, Enum):
    SIMPLE_QA = "simple_qa"
    SKILL_SINGLE = "skill_single"
    SKILL_CHAIN = "skill_chain"
    LONG_RUNNING = "long_running"


class AtomTask(BaseModel):
    """原子任务：DAG 中的一个节点"""
    task_id: str = Field(default_factory=new_id)
    description: str
    depends_on: list[str] = Field(default_factory=list)
    matched_skill: str | None = None
    is_new_skill_needed: bool = False
    prompt: str = ""
    safety_level: int = Field(default=1, ge=0, le=3)
    timeout_ms: int = 120_000
    max_retries: int = 2
    status: str = "pending"  # pending / running / done / failed / skipped
    dispatch_mode: str = "sync"  # sync / async
    chief_agent: str = "chief-general"
    agent_kind: str = "reusable"  # reusable / ephemeral
    protocol_status: str = "draft"  # draft/published/claimed/running/closed/failed_closed


class TaskPlan(BaseModel):
    """任务计划：有向无环图"""
    plan_id: str = Field(default_factory=new_id)
    original_query: str
    complexity: TaskComplexity = TaskComplexity.SIMPLE_QA
    tasks: list[AtomTask] = Field(default_factory=list)
    execution_order: list[list[str]] = Field(default_factory=list)
    estimated_duration_ms: int = 0
    host_context_snapshot: dict[str, Any] = Field(default_factory=dict)
    created_at: str = Field(default_factory=now_iso)


# ============================================================
# 施法执行域
# ============================================================

class ToolCallLog(BaseModel):
    """工具调用日志"""
    timestamp: str = Field(default_factory=now_iso)
    tool_name: str
    params: dict[str, Any] = Field(default_factory=dict)
    result: Any = None
    error: str | None = None
    duration_ms: int = 0
    injection_check_passed: bool = True
    permission_check_passed: bool = True

    @property
    def success(self) -> bool:
        return self.error is None


class ToolCallPhase(str, Enum):
    """工具调用阶段"""
    TOOL_START = "tool_start"
    TOOL_END = "tool_end"
    TASK_PUBLISH = "task_publish"
    TASK_CLAIM = "task_claim"
    TASK_CLOSE = "task_close"
    TASK_FORCE_CLOSE = "task_force_close"
    AGENT_START = "agent_start"
    AGENT_END = "agent_end"
    SUBTASK_SCHEDULE = "subtask_schedule"
    SUBTASK_RESULT = "subtask_result"
    MAIL_SEND = "mail_send"
    MAIL_RECV = "mail_recv"


class ToolCallEvent(BaseModel):
    """流式工具调用事件，在执行过程中实时发出"""
    phase: ToolCallPhase
    task_id: str = ""
    tool_name: str
    params: dict[str, Any] = Field(default_factory=dict)
    result: Any = None
    error: str | None = None
    duration_ms: int = 0
    from_agent: str = ""
    to_agent: str = ""
    message: str = ""


class ExecutionReport(BaseModel):
    """执行域输出"""
    execution_id: str = Field(default_factory=new_id)
    plan_id: str
    status: str = "pending"
    task_results: dict[str, Any] = Field(default_factory=dict)
    tool_call_logs: list[ToolCallLog] = Field(default_factory=list)
    total_duration_ms: int = 0
    anomalies: list[str] = Field(default_factory=list)
    llm_messages: list[dict[str, Any]] = Field(default_factory=list)
    agent_messages: list[dict[str, Any]] = Field(default_factory=list)


# ============================================================
# 验道校验域
# ============================================================

class VerificationResult(BaseModel):
    """单项校验结果"""
    check_name: str
    passed: bool
    detail: str = ""
    evidence: str | None = None
    fix_suggestion: str | None = None


class RollbackPlan(BaseModel):
    """回退计划"""
    plan_id: str = Field(default_factory=new_id)
    affected_tasks: list[str] = Field(default_factory=list)
    rollback_actions: list[dict[str, Any]] = Field(default_factory=list)
    side_effects_to_clean: list[str] = Field(default_factory=list)


class VerificationReport(BaseModel):
    """校验域输出"""
    verification_id: str = Field(default_factory=new_id)
    execution_id: str
    original_requirement: str
    overall_pass: bool = False
    structure_checks: list[VerificationResult] = Field(default_factory=list)
    content_checks: list[VerificationResult] = Field(default_factory=list)
    replay_checks: list[VerificationResult] = Field(default_factory=list)
    action: str = "pass"  # pass / retry / rollback / reconstruct / ask_user
    rollback_plan: RollbackPlan | None = None


# ============================================================
# 刻碑沉淀域
# ============================================================

class ExecutionSummary(BaseModel):
    """执行摘要"""
    summary_id: str = Field(default_factory=new_id)
    execution_id: str
    original_query: str
    what_was_done: str = ""
    how_it_was_done: str = ""
    problems_encountered: str = ""
    solutions_found: str = ""
    success_pattern: str = ""
    pitfalls: str = ""
    prompt_improvement: str = ""
    tools_used: list[str] = Field(default_factory=list)
    new_skills_needed: list[str] = Field(default_factory=list)
    created_at: str = Field(default_factory=now_iso)


class GapReport(BaseModel):
    """Skill 缺口报告"""
    missing_skills: list[dict[str, str]] = Field(default_factory=list)
    deficient_skills: list[dict[str, str]] = Field(default_factory=list)
    suggestions: list[str] = Field(default_factory=list)
