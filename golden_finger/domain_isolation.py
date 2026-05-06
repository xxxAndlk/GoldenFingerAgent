"""金手指 Agent 系统 — ⑤ 内外结界（隔离安全域）

贯穿全流程：
- 数据三级分级（紫府/灵台/识海）
- 出境数据脱敏
- 入境内容安全检查
- 宿主画像持续演化
"""

import re
from typing import Any

from .models import HostProfile


# ============================================================
# 数据分级
# ============================================================

class DataLevel:
    """数据隐私等级"""
    ZIFU = 0   # 紫府级：绝对隐私，永不出境
    LINGTAI = 1  # 灵台级：脱敏后内部使用
    SHIHAI = 2   # 识海级：完全抽象化后可出境


# 敏感字段分级表
FIELD_LEVELS: dict[str, int] = {
    "host_id": DataLevel.ZIFU,
    "soul_mark": DataLevel.ZIFU,
    "真实姓名": DataLevel.ZIFU,
    "身份证号": DataLevel.ZIFU,
    "精确坐标": DataLevel.ZIFU,
    "收入数据": DataLevel.ZIFU,
    "健康原始值": DataLevel.ZIFU,
    "银行账户": DataLevel.ZIFU,
    "生物特征": DataLevel.ZIFU,

    "能力坐标": DataLevel.LINGTAI,
    "学习偏好": DataLevel.LINGTAI,
    "作息规律": DataLevel.LINGTAI,
    "兴趣向量": DataLevel.LINGTAI,
    "技能熟练度": DataLevel.LINGTAI,
    "情绪曲线": DataLevel.LINGTAI,

    "境界等级": DataLevel.SHIHAI,
    "通用标签": DataLevel.SHIHAI,
    "匿名ID": DataLevel.SHIHAI,
    "兴趣领域": DataLevel.SHIHAI,
}


class EgressAnonymizer:
    """出境数据脱敏器"""

    # PII 正则模式（使用 (?<!\d)...(?!\d) 替代 \b，兼容中文字符）
    PII_PATTERNS = [
        (re.compile(r'(?<!\d)\d{17}[\dXx](?!\d)'), '[身份证号已脱敏]'),
        (re.compile(r'(?<!\d)1[3-9]\d{9}(?!\d)'), '[手机号已脱敏]'),
        (re.compile(r'(?<!\w)[\w.-]+@[\w.-]+\.\w+(?!\w)'), '[邮箱已脱敏]'),
        (re.compile(r'(?<!\d)\d{16,19}(?!\d)'), '[银行卡号已脱敏]'),
        (re.compile(r'(?<!\d)(?:\d{1,3}\.){3}\d{1,3}(?!\d)'), '[IP地址已脱敏]'),
    ]

    @classmethod
    def anonymize_text(cls, text: str) -> str:
        """对文本进行脱敏处理"""
        for pattern, replacement in cls.PII_PATTERNS:
            text = pattern.sub(replacement, text)
        return text

    @classmethod
    def anonymize_coordinates(cls, lat: float, lng: float) -> tuple[float, float]:
        """坐标模糊化：精度降至约 100m"""
        import random
        random.seed(f"{lat:.4f}{lng:.4f}")
        return (
            round(lat + random.uniform(-0.001, 0.001), 4),
            round(lng + random.uniform(-0.001, 0.001), 4),
        )

    @classmethod
    def anonymize_host_context(cls, profile: HostProfile) -> dict[str, Any]:
        """从宿主画像生成可出境的上下文（仅识海级）"""
        return {
            "realm": profile.realm.display,
            "realm_stage": profile.realm_stage.value,
            "interests": profile.interest_tags[:5],
            "skill_count": len(profile.skill_levels),
            "total_tasks": profile.total_tasks_completed,
        }


class IngressFilter:
    """入境内容安全过滤器"""

    @classmethod
    def check_safety(cls, content: str) -> tuple[bool, str]:
        """检查入境内容是否安全

        Returns:
            (is_safe, reason)
        """
        if not content:
            return True, ""

        # 检测常见攻击模式
        checks = [
            ("XSS 注入", r"<script[\s>]"),
            ("SQL 注入", r"(?i)(DROP\s+TABLE|UNION\s+SELECT|1\s*=\s*1)"),
            ("命令注入", r"(?i)(;\s*rm\s+-rf|;\s*wget|;\s*curl)"),
            ("路径穿越", r"\.\./\.\./\.\./"),
            ("模板注入", r"\{\{.*?\}\}|\$\{.*?\}"),
        ]

        for name, pattern in checks:
            if re.search(pattern, content):
                return False, f"检测到 {name} 攻击"

        return True, ""


class ProfileEvolver:
    """宿主画像演化器：从交互中提取画像更新信号"""

    @classmethod
    def extract_signals(cls, query: str, response: str, profile: HostProfile) -> dict[str, Any]:
        """从一次交互中提取画像更新信号"""
        signals: dict[str, Any] = {}

        # 1. 兴趣标签提取（基于关键词）
        tech_keywords = {
            "python": "Python", "java": "Java", "javascript": "JavaScript",
            "react": "React", "vue": "Vue", "ai": "AI", "机器学习": "机器学习",
            "深度学习": "深度学习", "docker": "Docker", "linux": "Linux",
            "数据库": "数据库", "算法": "算法", "产品经理": "产品",
            "写作": "写作", "英语": "英语", "健身": "健身",
        }
        for kw, tag in tech_keywords.items():
            if kw in query.lower() and tag not in profile.interest_tags:
                interest_list: list[str] = signals.setdefault("interest_tags", [])
                interest_list.append(tag)

        # 2. 学习方向推断
        learning_signals = ["学习", "教程", "怎么学", "入门", "进阶", "推荐书"]
        if any(s in query for s in learning_signals):
            signals["is_learning_query"] = True

        return signals

    @classmethod
    def apply_signals(cls, profile: HostProfile, signals: dict[str, Any]):
        """将演化信号应用到宿主画像"""
        if "interest_tags" in signals:
            for tag in signals["interest_tags"]:
                if tag not in profile.interest_tags:
                    profile.interest_tags.append(tag)
                    # 只保留最近 20 个
                    if len(profile.interest_tags) > 20:
                        profile.interest_tags = profile.interest_tags[-20:]

        profile.total_tasks_completed += 1
