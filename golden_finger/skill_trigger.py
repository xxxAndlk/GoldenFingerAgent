"""金手指 Agent 系统 — Skill 自动触发引擎

灵感来自 superpowers: "The agent checks for relevant skills before any task."
将 Skill 从"被动推荐"升级为"主动强制执行"。

触发条件维度：
- task_type:  任务类型 (code/test/debug/plan/review/learn/file_ops)
- context_state: 上下文状态 (error/multi_task/clean_slate/any)
- domain_tags: 领域标签 (web/db/api/security/ai/data/infra)
- min_realm:   最低境界要求

每个 Skill 可以定义多条 trigger rules，命中任一条即触发。
"""

from __future__ import annotations

import logging
import re
from dataclasses import dataclass, field
from enum import Enum
from typing import Any

from .models import RealmLevel

logger = logging.getLogger("golden_finger.skill_trigger")


# ============================================================
# 触发条件类型
# ============================================================

class TaskType(str, Enum):
    """任务类型"""
    CODE = "code"           # 编写代码
    TEST = "test"           # 编写/运行测试
    DEBUG = "debug"         # 调试错误
    PLAN = "plan"           # 制定计划
    REVIEW = "review"       # 代码审查
    LEARN = "learn"         # 学习/研究
    FILE_OPS = "file_ops"   # 文件操作


class ContextState(str, Enum):
    """上下文状态"""
    ERROR = "error"                 # 发生错误
    MULTI_TASK = "multi_task"       # 多个并行任务
    CLEAN_SLATE = "clean_slate"     # 全新任务（无上下文）
    AFTER_IMPLEMENTATION = "after_implementation"  # 刚完成实现
    ANY = "any"                     # 任意状态


# ============================================================
# 触发规则
# ============================================================

@dataclass
class TriggerRule:
    """单条触发规则：所有非空条件必须同时满足"""
    task_types: list[TaskType] = field(default_factory=list)
    context_states: list[ContextState] = field(default_factory=list)
    domain_tags: list[str] = field(default_factory=list)
    keyword_patterns: list[str] = field(default_factory=list)  # 在 query 中匹配
    min_realm: RealmLevel = RealmLevel.MORTAL

    def matches(
        self,
        task_type: TaskType | None,
        context_state: ContextState | None,
        domain_tags: set[str],
        query_text: str,
        realm: RealmLevel,
    ) -> bool:
        """检查当前上下文是否满足此触发规则"""
        # realm 检查
        if realm.value < self.min_realm.value:
            return False

        # task_type 检查
        if self.task_types and task_type not in self.task_types:
            return False

        # context_state 检查
        if self.context_states and ContextState.ANY not in self.context_states:
            if context_state not in self.context_states:
                return False

        # domain_tags 检查
        if self.domain_tags:
            if not set(self.domain_tags) & domain_tags:
                return False

        # keyword 检查
        if self.keyword_patterns:
            query_lower = query_text.lower()
            if not any(kw.lower() in query_lower for kw in self.keyword_patterns):
                return False

        return True


@dataclass
class SkillTriggerDef:
    """Skill 触发定义"""
    skill_name: str
    rules: list[TriggerRule] = field(default_factory=list)
    mandatory: bool = True          # 是否强制执行（不可跳过）
    priority: int = 50              # 优先级 (0-100, 越高越优先)
    cooldown_seconds: int = 0       # 冷却时间（秒）
    description: str = ""           # 为什么触发


# ============================================================
# 上下文输入
# ============================================================

@dataclass
class TriggerContext:
    """自动触发引擎的输入上下文"""
    query_text: str = ""
    task_type: TaskType | None = None
    context_state: ContextState | None = None
    domain_tags: set[str] = field(default_factory=set)
    realm: RealmLevel = RealmLevel.MORTAL
    has_error: bool = False
    task_count: int = 1
    is_parallel: bool = False


# ============================================================
# 触发结果
# ============================================================

@dataclass
class TriggerResult:
    """触发评估结果"""
    mandatory_skills: list[tuple[str, str]] = field(default_factory=list)
    # [(skill_name, trigger_reason), ...]
    recommended_skills: list[tuple[str, str]] = field(default_factory=list)
    # [(skill_name, trigger_reason), ...]

    @property
    def all_skill_names(self) -> list[str]:
        return [s[0] for s in self.mandatory_skills + self.recommended_skills]


# ============================================================
# 领域标签推断器
# ============================================================

DOMAIN_KEYWORD_MAP: dict[str, list[str]] = {
    "web": ["网页", "前端", "html", "css", "javascript", "vue", "react", "api", "http", "rest", "网站"],
    "db": ["数据库", "sql", "mysql", "postgres", "mongo", "redis", "存储", "查询"],
    "api": ["接口", "端点", "endpoint", "路由", "route", "openapi", "swagger", "graphql"],
    "security": ["安全", "加密", "认证", "授权", "auth", "jwt", "token", "密码", "xss", "注入"],
    "ai": ["ai", "模型", "训练", "推理", "llm", "gpt", "claude", "prompt", "深度学习"],
    "data": ["数据", "分析", "处理", "etl", "pandas", "爬虫", "采集", "统计"],
    "infra": ["部署", "docker", "k8s", "ci/cd", "服务器", "nginx", "linux", "运维"],
    "cli": ["命令行", "终端", "tui", "cli", "bash", "shell", "脚本"],
}


class DomainTagger:
    """从查询文本推断领域标签"""

    @staticmethod
    def tag(query_text: str) -> set[str]:
        tags: set[str] = set()
        text_lower = query_text.lower()
        for tag, keywords in DOMAIN_KEYWORD_MAP.items():
            for kw in keywords:
                if kw.lower() in text_lower:
                    tags.add(tag)
                    break
        return tags if tags else {"general"}

    @staticmethod
    def infer_task_type(query_text: str) -> TaskType:
        """从查询文本推断任务类型"""
        text_lower = query_text.lower()

        code_patterns = ["写", "编写", "实现", "代码", "函数", "类", "模块", "开发", "program",
                         "function", "class", "implement", "create", "添加"]
        test_patterns = ["测试", "test", "用例", "验证", "检查", "assert"]
        debug_patterns = ["报错", "错误", "调试", "bug", "修", "fix", "error", "exception",
                          "为什么", "不行", "失败", "debug"]
        plan_patterns = ["计划", "规划", "方案", "设计", "plan", "design", "架构", "怎么"]
        review_patterns = ["审查", "review", "检查", "代码质量", "重构", "refactor"]
        learn_patterns = ["学习", "了解", "介绍", "解释", "什么是", "怎么学", "教程", "learn",
                          "what", "how", "explain"]
        file_patterns = ["文件", "重命名", "移动", "复制", "删除", "目录", "file", "folder",
                         "rename", "move", "copy", "delete"]

        if any(p in text_lower for p in debug_patterns):
            return TaskType.DEBUG
        if any(p in text_lower for p in test_patterns):
            return TaskType.TEST
        if any(p in text_lower for p in plan_patterns):
            return TaskType.PLAN
        if any(p in text_lower for p in review_patterns):
            return TaskType.REVIEW
        if any(p in text_lower for p in learn_patterns):
            return TaskType.LEARN
        if any(p in text_lower for p in file_patterns):
            return TaskType.FILE_OPS
        if any(p in text_lower for p in code_patterns):
            return TaskType.CODE

        return TaskType.CODE  # 默认


# ============================================================
# Skill 自动触发引擎
# ============================================================

class SkillAutoTrigger:
    """Skill 自动触发引擎

    注册 Skill 的触发条件，根据上下文自动决定哪些 Skill 必须激活。
    """

    def __init__(self):
        self._triggers: dict[str, SkillTriggerDef] = {}

    # ---- 注册 ----

    def register(
        self,
        skill_name: str,
        rules: list[TriggerRule] | None = None,
        mandatory: bool = True,
        priority: int = 50,
        cooldown_seconds: int = 0,
        description: str = "",
    ) -> None:
        """注册一个 Skill 的触发条件"""
        self._triggers[skill_name] = SkillTriggerDef(
            skill_name=skill_name,
            rules=rules or [],
            mandatory=mandatory,
            priority=priority,
            cooldown_seconds=cooldown_seconds,
            description=description,
        )
        logger.debug(f"Skill 触发已注册: {skill_name} (mandatory={mandatory})")

    def unregister(self, skill_name: str) -> None:
        self._triggers.pop(skill_name, None)

    def list_triggers(self) -> list[SkillTriggerDef]:
        return list(self._triggers.values())

    # ---- 评估 ----

    def evaluate(self, ctx: TriggerContext) -> TriggerResult:
        """评估上下文，返回必须激活和推荐激活的 Skill 列表"""
        mandatory: list[tuple[str, str, int]] = []  # (name, reason, priority)
        recommended: list[tuple[str, str, int]] = []

        # 推断缺失的上下文（使用本地变量，不修改输入的 TriggerContext）
        task_type = ctx.task_type or DomainTagger.infer_task_type(ctx.query_text)
        domain_tags = ctx.domain_tags
        if not domain_tags or domain_tags == {"general"}:
            domain_tags = DomainTagger.tag(ctx.query_text)

        for trigger_def in self._triggers.values():
            for rule in trigger_def.rules:
                if rule.matches(
                    task_type=task_type,
                    context_state=ctx.context_state,
                    domain_tags=domain_tags,
                    query_text=ctx.query_text,
                    realm=ctx.realm,
                ):
                    reason = self._build_reason(ctx, rule, trigger_def)
                    entry = (trigger_def.skill_name, reason, trigger_def.priority)
                    if trigger_def.mandatory:
                        mandatory.append(entry)
                    else:
                        recommended.append(entry)
                    break  # 命中一条规则即可

        # 按优先级排序
        mandatory.sort(key=lambda x: x[2], reverse=True)
        recommended.sort(key=lambda x: x[2], reverse=True)

        return TriggerResult(
            mandatory_skills=[(m[0], m[1]) for m in mandatory],
            recommended_skills=[(r[0], r[1]) for r in recommended],
        )

    def get_mandatory_skills(self, ctx: TriggerContext) -> list[str]:
        """获取当前上下文下必须激活的 Skill 名称列表"""
        result = self.evaluate(ctx)
        return [s[0] for s in result.mandatory_skills]

    def get_all_triggered(self, ctx: TriggerContext) -> list[str]:
        """获取当前上下文下所有触发的 Skill 名称"""
        result = self.evaluate(ctx)
        return result.all_skill_names

    # ---- 辅助 ----

    @staticmethod
    def _build_reason(ctx: TriggerContext, rule: TriggerRule, td: SkillTriggerDef) -> str:
        """构建人类可读的触发原因"""
        parts = []
        if ctx.task_type:
            parts.append(f"任务类型={ctx.task_type.value}")
        if ctx.context_state:
            parts.append(f"上下文={ctx.context_state.value}")
        if rule.domain_tags:
            parts.append(f"领域={','.join(rule.domain_tags)}")
        if rule.keyword_patterns:
            parts.append(f"关键词匹配={','.join(rule.keyword_patterns[:3])}")
        return "; ".join(parts) if parts else td.description


# ============================================================
# 预置触发规则
# ============================================================

def build_default_triggers() -> SkillAutoTrigger:
    """构建带有预置触发规则的引擎

    这些规则映射自 superpowers 的技能触发逻辑 + spec-kit 的工作流门禁。
    """
    engine = SkillAutoTrigger()

    # ---- 筑基技能 (现有 GoldenFinger 技能) ----

    engine.register(
        "knowledge_absorption",
        rules=[
            TriggerRule(task_types=[TaskType.LEARN]),
            TriggerRule(keyword_patterns=["学习", "了解", "解释", "教程", "入门", "概念"]),
        ],
        mandatory=False,
        priority=60,
        description="学习类请求自动推荐知识汲取术",
    )

    engine.register(
        "code_assistant",
        rules=[
            TriggerRule(task_types=[TaskType.CODE, TaskType.DEBUG]),
            TriggerRule(keyword_patterns=["代码", "编程", "写", "实现", "bug", "报错"]),
        ],
        mandatory=True,
        priority=70,
        description="编码/调试任务必须激活代码辅助术",
    )

    engine.register(
        "file_operations",
        rules=[
            TriggerRule(task_types=[TaskType.FILE_OPS]),
            TriggerRule(keyword_patterns=["文件", "目录", "重命名", "移动", "复制"]),
        ],
        mandatory=True,
        priority=50,
        description="文件操作任务必须激活文件操作术",
    )

    # ---- Superpowers 技能 ----

    engine.register(
        "brainstorming",
        rules=[
            TriggerRule(
                task_types=[TaskType.PLAN],
                context_states=[ContextState.CLEAN_SLATE],
                keyword_patterns=["设计", "方案", "架构", "怎么", "如何", "重构", "改造"],
            ),
        ],
        mandatory=True,
        priority=95,
        description="任何代码编写前，必须先进行需求澄清（brainstorming）",
    )

    engine.register(
        "test-driven-development",
        rules=[
            TriggerRule(task_types=[TaskType.CODE, TaskType.TEST]),
            TriggerRule(keyword_patterns=["实现", "开发", "添加", "修改", "创建"]),
        ],
        mandatory=True,
        priority=90,
        description="任何代码生成任务必须遵循 TDD（测试先行）",
    )

    engine.register(
        "systematic-debugging",
        rules=[
            TriggerRule(
                task_types=[TaskType.DEBUG],
                context_states=[ContextState.ERROR],
            ),
            TriggerRule(keyword_patterns=["报错", "错误", "bug", "fix", "修", "失败", "异常"]),
        ],
        mandatory=True,
        priority=90,
        description="错误发生时强制使用系统调试术",
    )

    engine.register(
        "writing-plans",
        rules=[
            TriggerRule(task_types=[TaskType.PLAN]),
            TriggerRule(
                context_states=[ContextState.CLEAN_SLATE],
                keyword_patterns=["计划", "步骤", "怎么做"],
            ),
        ],
        mandatory=False,
        priority=80,
        description="规划类任务推荐使用计划编写技能",
    )

    engine.register(
        "executing-plans",
        rules=[
            TriggerRule(
                task_types=[TaskType.CODE],
                context_states=[ContextState.CLEAN_SLATE],
                keyword_patterns=["按计划", "执行", "开始实现"],
            ),
        ],
        mandatory=False,
        priority=80,
        description="计划就绪后可批量执行",
    )

    engine.register(
        "dispatching-parallel-agents",
        rules=[
            TriggerRule(context_states=[ContextState.MULTI_TASK]),
        ],
        mandatory=True,
        priority=85,
        description="多任务场景自动启用并行子代理调度",
    )

    engine.register(
        "requesting-code-review",
        rules=[
            TriggerRule(context_states=[ContextState.AFTER_IMPLEMENTATION]),
            TriggerRule(task_types=[TaskType.CODE]),
        ],
        mandatory=True,
        priority=75,
        description="实现完成后必须进行代码审查",
    )

    engine.register(
        "receiving-code-review",
        rules=[
            TriggerRule(task_types=[TaskType.REVIEW]),
            TriggerRule(keyword_patterns=["审查意见", "review", "修改建议"]),
        ],
        mandatory=True,
        priority=70,
        description="收到审查意见后使用审查响应技能",
    )

    engine.register(
        "using-git-worktrees",
        rules=[
            TriggerRule(
                context_states=[ContextState.MULTI_TASK],
                domain_tags=["infra", "cli"],
            ),
            TriggerRule(keyword_patterns=["并行开发", "多分支", "worktree"]),
        ],
        mandatory=False,
        priority=60,
        description="需要并行开发时推荐使用 Git Worktree",
    )

    engine.register(
        "finishing-a-development-branch",
        rules=[
            TriggerRule(context_states=[ContextState.AFTER_IMPLEMENTATION]),
        ],
        mandatory=True,
        priority=65,
        description="所有任务完成后使用分支完成工作流",
    )

    engine.register(
        "writing-skills",
        rules=[
            TriggerRule(
                task_types=[TaskType.PLAN],
                keyword_patterns=["新技能", "创建技能", "skill", "自动化", "工作流"],
            ),
        ],
        mandatory=False,
        priority=40,
        description="创建新技能时推荐使用技能编写指南",
    )

    return engine


# ============================================================
# 全局实例
# ============================================================

trigger_engine = build_default_triggers()
