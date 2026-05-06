# 金手指 Agent 系统设计文档

> \*\*版本\*\*: v1.0  
> \*\*定位\*\*: 玄幻网文概念 × 现实个人成长 Agent 系统  
> \*\*核心范式\*\*: Harness（宿主框架）+ Skill（技能插件）+ Protocol（交互协议）

\---

## 一、核心设计哲学

### 1.1 隐喻映射表

|网文概念|现实映射|Agent 组件|
|-|-|-|
|金手指|个人成长 Agent 系统|完整 Harness|
|宿主|当前使用人|User Profile|
|功法|课程/技能学习路径|Skill Tree|
|修炼进度|学习进度/能力值|Progress Tracker|
|灵根/体质|天赋模型与身体基线|Talent Model|
|宗门/秘境|学习社群/专项训练营|Environment|
|神识/内视|自我认知与复盘|Introspection|
|传功/灌顶|知识快速注入|Fast Learning|
|法宝|效率工具集|Toolkit|
|地图/传送阵|导航与路径规划|Map System|

### 1.2 内外隔离原则

```
+-------------------------------------------------------------+
|                    外界数据层 (Outer World)                  |
|  +----------+  +----------+  +----------+  +----------+   |
|  | 互联网API |  | 地图服务  |  | 公开知识库|  | 第三方工具|   |
|  +-----+----+  +-----+----+  +-----+----+  +-----+----+   |
|        |             |             |             |          |
|        +-------------+-------------+-------------+          |
|                      |                                      |
|                 \[隔离网关 Gateway]                          |
|                      |                                      |
+----------------------+--------------------------------------+
|                    宿主数据层 (Inner World)                  |
|  +----------+  +----------+  +----------+  +----------+   |
|  | 宿主画像  |  | 能力模型  |  | 记忆宫殿  |  | 修炼进度  |   |
|  | Profile  |  | Ability  |  | Memory   |  | Progress |   |
|  +----------+  +----------+  +----------+  +----------+   |
|                                                             |
|                    \[金手指核心 Core]                         |
|                                                             |
+-------------------------------------------------------------+
```

**隔离规则**：

* 宿主原始数据永不出境，仅经脱敏/抽象化后的能力坐标可对外交互
* 外界数据入境需经「心魔试炼」过滤器（相关性+安全性校验）
* 所有外部API调用走代理层，不暴露宿主真实身份与精确位置

\---

## 二、系统架构总览

```
+--------------------------------------------------------------+
|                      交互界面层 (Interface)                     |
|   CLI / Web / 移动端 / 语音 / 脑机接口(预留)                   |
+--------------------------------------------------------------+
                              |
+--------------------------------------------------------------+
|                   金手指核心 Harness                          |
|  +-------------+ +-------------+ +-------------+             |
|  |   宿主引擎   | |   调度中枢   | |   安全沙箱   |             |
|  |  HostEngine | |  Scheduler  | |   Sandbox   |             |
|  +-------------+ +-------------+ +-------------+             |
|                                                              |
|  +-------------+ +-------------+ +-------------+             |
|  |   记忆宫殿   | |   修炼系统   | |   地图系统   |             |
|  |   Memory    | | Cultivation | |    Map      |             |
|  +-------------+ +-------------+ +-------------+             |
+--------------------------------------------------------------+
                              |
+--------------------------------------------------------------+
|                   技能插件层 Skills                           |
|  +----------+ +----------+ +----------+ +----------+        |
|  | 知识汲取  | | 身体淬炼  | | 社交推演  | | 财富运筹  |        |
|  | Knowledge| | Physical | |  Social  | |  Wealth  |        |
|  +----------+ +----------+ +----------+ +----------+        |
|  +----------+ +----------+ +----------+ +----------+        |
|  | 时间法则  | | 空间感知  | | 因果推演  | | 心魔抵御  |        |
|  |   Time   | |  Space   | | Causality| |  Demon   |        |
|  +----------+ +----------+ +----------+ +----------+        |
+--------------------------------------------------------------+
                              |
+--------------------------------------------------------------+
|                   协议与数据层 Protocol                        |
|  +----------+ +----------+ +----------+ +----------+        |
|  | 宿主协议  | | 技能协议  | | 记忆协议  | | 地图协议  |        |
|  |  Host    | |  Skill   | |  Memory  | |   Map    |        |
|  +----------+ +----------+ +----------+ +----------+        |
+--------------------------------------------------------------+
```

\---

## 三、Harness 宿主框架设计

### 3.1 核心职责

Harness 是金手指系统的「天道意志」，负责：

1. **生命周期管理**：宿主的注册、初始化、运行、休眠、涅槃（重置）
2. **资源调度**：协调各 Skill 的算力、存储、外部 API 配额
3. **安全沙箱**：确保内外隔离，防止宿主数据泄露与外部污染
4. **事件总线**：驱动「奇遇」「心魔」「突破」等网文事件的触发

### 3.2 宿主引擎 HostEngine

```python
class HostEngine:
    """
    宿主引擎：管理宿主的生命周期与核心状态
    """
    def \_\_init\_\_(self):
        self.host\_id: str = self.\_generate\_soul\_mark()
        self.state: HostState = HostState.INITIALIZING
        self.realm: Realm = Realm.MORTAL
        self.aura: AuraProfile = AuraProfile()

    def \_generate\_soul\_mark(self) -> str:
        """生成灵魂印记：基于宿主生物特征+时间戳的不可逆哈希"""
        import hashlib, time, uuid
        seed = f"{uuid.getnode()}-{time.time\_ns()}-{uuid.uuid4()}"
        return hashlib.sha3\_256(seed.encode()).hexdigest()\[:32]

    def initialize(self, host\_profile: dict) -> InitializationReport:
        """
        宿主初始化：灵根检测 + 体质扫描
        对应现实：能力基线测试 + 学习习惯分析
        """
        spirit\_root = self.\_detect\_spirit\_root(host\_profile)
        physique = self.\_scan\_physique(host\_profile)
        divine\_sense = self.\_measure\_divine\_sense(host\_profile)

        self.state = HostState.ACTIVE
        return InitializationReport(spirit\_root, physique, divine\_sense)

    def \_detect\_spirit\_root(self, profile: dict) -> SpiritRoot:
        """
        灵根检测算法：将认知能力映射为五行灵根
        - 金灵根：逻辑推理、数学能力
        - 木灵根：创造力、发散思维
        - 水灵根：语言能力、沟通共情
        - 火灵根：行动力、决策速度
        - 土灵根：耐心、细致、持久力
        """
        scores = {
            "metal": profile.get("logical\_score", 50),
            "wood": profile.get("creative\_score", 50),
            "water": profile.get("linguistic\_score", 50),
            "fire": profile.get("action\_score", 50),
            "earth": profile.get("patience\_score", 50)
        }
        dominant = max(scores, key=scores.get)
        purity = scores\[dominant] / 100.0
        return SpiritRoot(dominant=dominant, purity=purity, scores=scores)
```

### 3.3 境界系统 Realm

```python
from enum import Enum, auto

class Realm(Enum):
    """
    修炼境界：对应现实中的能力成长阶段
    每个境界分初阶、中阶、高阶、圆满
    """
    MORTAL = auto()          # 凡人：未激活系统
    QI\_REFINING = auto()     # 练气：建立学习习惯（1-3个月）
    FOUNDATION = auto()      # 筑基：掌握核心方法论（3-12个月）
    GOLDEN\_CORE = auto()     # 金丹：形成领域专长（1-3年）
    NASCENT\_SOUL = auto()    # 元婴：具备创新能力（3-5年）
    DEITY = auto()           # 化神：行业影响力（5-10年）
    TRIBULATION = auto()     # 渡劫：突破瓶颈期
    MAHAYANA = auto()        # 大乘：体系化输出
    TRUE\_IMMORTAL = auto()   # 真仙：开宗立派

class BreakthroughEngine:
    """
    突破引擎：检测宿主是否满足境界突破条件
    """
    def check\_breakthrough(self, host: HostProfile) -> BreakthroughReport:
        current = host.realm
        requirements = self.\_get\_requirements(current)

        checks = {
            "knowledge\_mastery": host.knowledge\_tree.coverage >= requirements.knowledge,
            "skill\_proficiency": host.skill\_tree.avg\_level >= requirements.skill,
            "physique\_index": host.physique.composite\_score >= requirements.physique,
            "mental\_state": host.mental.stability >= requirements.mental,
            "real\_world\_impact": host.impact\_score >= requirements.impact
        }

        ready = all(checks.values())

        if ready:
            return BreakthroughReport(
                status=BreakthroughStatus.READY,
                next\_realm=self.\_next\_realm(current),
                tribulation=self.\_generate\_tribulation(host),
                checks=checks
            )
        else:
            return BreakthroughReport(
                status=BreakthroughStatus.NOT\_READY,
                deficiencies={k: v for k, v in checks.items() if not v},
                checks=checks
            )
```

\---

## 四、Skill 技能系统设计

### 4.1 Skill 基类与协议

```python
from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import Dict, Any, Optional, List

@dataclass
class SkillManifest:
    """技能 manifest：类似 K8s 的声明式配置"""
    name: str
    display\_name: str
    category: str
    realm\_requirement: Realm
    dependencies: List\[str]
    resources: Dict\[str, Any]
    hooks: Dict\[str, str]

class BaseSkill(ABC):
    """
    技能基类：所有金手指能力的抽象接口
    遵循 Harness-Skill-Protocol 三层架构
    """
    def \_\_init\_\_(self, manifest: SkillManifest, harness: "Harness"):
        self.manifest = manifest
        self.harness = harness
        self.state = SkillState.DORMANT
        self.level = 0
        self.experience = 0
        self.affinity = 1.0

    @abstractmethod
    def activate(self, context: ExecutionContext) -> SkillResult:
        """激活技能：执行核心逻辑"""
        pass

    @abstractmethod
    def train(self, training\_data: Any) -> TrainingReport:
        """训练技能：通过实践提升熟练度"""
        pass

    def can\_activate(self, context: ExecutionContext) -> bool:
        """前置检查：境界/资源/冷却"""
        host = self.harness.get\_host()
        if host.realm.value < self.manifest.realm\_requirement.value:
            return False
        if not self.harness.resource\_manager.allocate(self.manifest.resources):
            return False
        if self.\_is\_on\_cooldown():
            return False
        return True

    def \_calculate\_affinity(self) -> float:
        """
        计算技能契合度：基于宿主灵根属性
        例：金灵根宿主使用逻辑类技能有加成
        """
        root = self.harness.host\_engine.aura.spirit\_root
        affinity\_map = {
            "metal": \["logical\_analysis", "data\_processing", "algorithm"],
            "wood": \["creative\_design", "content\_creation", "innovation"],
            "water": \["communication", "negotiation", "language\_learning"],
            "fire": \["project\_management", "decision\_making", "execution"],
            "earth": \["quality\_assurance", "research", "maintenance"]
        }
        if self.manifest.name in affinity\_map.get(root.dominant, \[]):
            return 1.0 + root.purity \* 0.5
        return 1.0
```

### 4.2 核心技能清单

#### 4.2.1 知识汲取术 (KnowledgeAbsorption)

```python
class KnowledgeAbsorption(BaseSkill):
    """
    网文映射：功法学习 -> 现实映射：课程/知识学习

    功能：
    - 自动拆解学习目标为「经脉路线图」（知识图谱）
    - 根据灵根属性推荐最优学习路径
    - 监控学习进度，生成「功法熟练度」
    - 支持「顿悟」模式（深度学习时段）
    """

    def activate(self, context: ExecutionContext) -> SkillResult:
        target = context.params.get("target")

        # 1. 生成经脉路线图（知识图谱）
        meridian\_map = self.\_generate\_meridian\_map(target)

        # 2. 检测瓶颈穴位（难点预警）
        blockages = self.\_detect\_blockages(meridian\_map, context.host\_profile)

        # 3. 计算修炼时长预估
        time\_estimate = self.\_estimate\_cultivation\_time(
            meridian\_map,
            context.host\_profile.learning\_speed \* self.affinity
        )

        return SkillResult(
            success=True,
            data={
                "meridian\_map": meridian\_map,
                "blockages": blockages,
                "time\_estimate": time\_estimate,
                "recommended\_method": self.\_select\_method(context.host\_profile)
            },
            side\_effects=\["mental\_fatigue: +5"]
        )

    def \_generate\_meridian\_map(self, target: str) -> KnowledgeGraph:
        """
        生成经脉路线图：将知识体系映射为经络图
        - 主脉：核心知识线
        - 支脉： prerequisite 知识
        - 穴位：关键知识点/技能点
        - 周天循环：复习与巩固路径
        """
        raw\_knowledge = self.harness.gateway.query\_external(
            service="knowledge\_base",
            query=target,
            anonymize=True
        )

        graph = KnowledgeGraph()
        graph.main\_meridian = self.\_extract\_core\_path(raw\_knowledge)
        graph.branch\_meridians = self.\_extract\_prerequisites(raw\_knowledge)
        graph.acupoints = self.\_identify\_key\_concepts(raw\_knowledge)

        return graph
```

#### 4.2.2 身体淬炼术 (BodyRefinement)

```python
class BodyRefinement(BaseSkill):
    """
    网文映射：炼体/淬体 -> 现实映射：身体健康管理、运动训练

    功能：
    - 体质扫描：生成「身体属性面板」
    - 淬体方案：根据体质定制训练计划
    - 气血监控：连接可穿戴设备，实时监控
    - 瓶颈突破：体能平台期突破方案
    """

    def scan\_physique(self) -> PhysiquePanel:
        """体质扫描：生成属性面板"""
        data = self.harness.gateway.query\_external(
            service="health\_device",
            query="latest\_vitals",
            host\_bound=True
        )

        return PhysiquePanel(
            strength=data.get("muscle\_mass", 0),
            agility=data.get("vo2\_max", 0),
            endurance=data.get("resting\_hr", 0),
            vitality=data.get("sleep\_score", 0),
            defense=data.get("immune\_index", 0)
        )

    def generate\_refinement\_plan(self, goal: str) -> CultivationPlan:
        """生成淬体方案"""
        physique = self.scan\_physique()

        if goal == "减脂":
            method = "轻身术"
        elif goal == "增肌":
            method = "金刚诀"
        elif goal == "柔韧":
            method = "瑜伽经"
        else:
            method = "五禽戏"

        return CultivationPlan(
            method=method,
            schedule=self.\_generate\_schedule(physique, goal),
            milestones=self.\_set\_milestones(physique, goal),
            nutrition=self.\_generate\_nutrition\_plan(physique),
            recovery=self.\_generate\_recovery\_plan(physique)
        )
```

#### 4.2.3 神识记忆术 (DivineSenseMemory)

```python
class DivineSenseMemory(BaseSkill):
    """
    网文映射：神识/记忆宫殿 -> 现实映射：记忆增强与知识管理

    功能：
    - 记忆宫殿构建：将知识空间化存储
    - 神识检索：自然语言搜索记忆
    - 遗忘抵御：艾宾浩斯曲线+个性化复习
    - 顿悟触发：跨领域知识关联推荐
    """

    def construct\_palace(self, domain: str) -> MemoryPalace:
        """为特定领域构建记忆宫殿"""
        fragments = self.harness.memory.search(
            query=domain,
            type="episodic\_semantic\_blend"
        )

        palace = MemoryPalace(domain=domain)

        for fragment in fragments:
            room = palace.create\_room(
                name=fragment.topic,
                anchors=fragment.keywords,
                connections=fragment.related\_topics
            )
            for detail in fragment.details:
                room.place\_object(
                    object\_name=detail.concept,
                    visual\_anchor=detail.visualization,
                    emotional\_tag=detail.importance
                )

        return palace

    def divine\_search(self, query: str) -> List\[MemoryFragment]:
        """
        神识检索：用自然语言搜索记忆
        支持模糊匹配、语义关联、时间范围
        """
        query\_vector = self.harness.gateway.query\_external(
            service="embedding\_model",
            query=query,
            anonymize=True
        )

        results = self.harness.memory.vector\_search(
            vector=query\_vector,
            top\_k=10,
            filters={"host\_only": True}
        )

        return results
```

#### 4.2.4 缩地成寸 (MapNavigation)

```python
class MapNavigation(BaseSkill):
    """
    网文映射：缩地成寸/传送阵 -> 现实映射：地图导航与路径规划

    功能：
    - 实时位置感知与路线规划
    - 「指定内容导航」：搜索目的地并导航
    - 「秘境探索」：发现周边有价值地点
    - 「空间锚点」：标记常用地点快速传送
    """

    def \_\_init\_\_(self, manifest: SkillManifest, harness: "Harness"):
        super().\_\_init\_\_(manifest, harness)
        self.map\_provider = "amap"
        self.space\_anchors: Dict\[str, Anchor] = {}

    def navigate(self, destination: str, mode: str = "optimal") -> NavigationResult:
        """
        缩地成寸：导航到指定地点
        mode: optimal(最优)/fastest(最快)/cheapest(最省)/cultivation(修炼模式)
        """
        current = self.\_get\_host\_location(anonymized=True)
        dest\_coords = self.\_resolve\_destination(destination)

        if mode == "cultivation":
            route = self.\_select\_cultivation\_route(current, dest\_coords)
        else:
            route = self.\_query\_amap\_route(current, dest\_coords, mode)

        return NavigationResult(
            route=route,
            estimated\_time=route.duration,
            estimated\_cost=route.cost,
            cultivation\_value=self.\_calculate\_cultivation\_value(route),
            warnings=route.warnings
        )

    def set\_space\_anchor(self, name: str, location: str) -> Anchor:
        """
        设置空间锚点：标记常用地点
        类似游戏中的「回城卷轴」目标点
        """
        coords = self.\_resolve\_destination(location)
        anchor = Anchor(name=name, coords=coords, created\_at=now())
        self.space\_anchors\[name] = anchor

        self.harness.memory.store(
            category="space\_anchors",
            data=anchor.to\_dict(),
            access\_level="host\_only"
        )

        return anchor

    def teleport(self, anchor\_name: str) -> NavigationResult:
        """
        传送：导航到已标记的锚点
        现实中即一键导航到常用地点
        """
        if anchor\_name not in self.space\_anchors:
            return NavigationResult(success=False, error="锚点未设置")

        anchor = self.space\_anchors\[anchor\_name]
        return self.navigate(destination=anchor.coords, mode="fastest")

    def explore\_secret\_realms(self, radius: int = 5000) -> List\[SecretRealm]:
        """
        秘境探索：发现周边有价值地点
        根据宿主当前修炼需求推荐地点
        """
        current = self.\_get\_host\_location(anonymized=True)
        host\_needs = self.\_analyze\_host\_needs()

        nearby = self.\_query\_amap\_poi(
            location=current,
            radius=radius,
            keywords=self.\_needs\_to\_keywords(host\_needs)
        )

        realms = \[]
        for poi in nearby:
            realm = SecretRealm(
                name=poi.name,
                type=self.\_classify\_poi(poi),
                distance=poi.distance,
                value=self.\_calculate\_value(poi, host\_needs),
                coordinates=poi.location,
                tags=poi.tags
            )
            realms.append(realm)

        return sorted(realms, key=lambda x: x.value, reverse=True)\[:10]

    def \_query\_amap\_route(self, origin, destination, mode: str) -> Route:
        """调用高德地图 API（经隔离网关）"""
        return self.harness.gateway.query\_external(
            service="amap",
            endpoint="direction",
            params={
                "origin": f"{origin.lng},{origin.lat}",
                "destination": f"{destination.lng},{destination.lat}",
                "mode": mode,
                "key": "${AMAP\_KEY}"
            }
        )
```

#### 4.2.5 因果推演术 (CausalityDeduction)

```python
class CausalityDeduction(BaseSkill):
    """
    网文映射：推演天机 -> 现实映射：决策支持与趋势预测

    功能：
    - 行动后果推演：做某事的连锁反应预测
    - 机缘捕捉：发现潜在机会
    - 灾厄预警：风险识别与规避
    """

    def deduce(self, action: str, depth: int = 3) -> CausalityTree:
        """
        因果推演：预测行动的连锁反应
        depth: 推演深度（1-5）
        """
        tree = CausalityTree(root=action)

        for level in range(depth):
            current\_nodes = tree.get\_level(level)
            for node in current\_nodes:
                consequences = self.\_infer\_consequences(
                    action=node.action,
                    host\_profile=self.harness.get\_host(),
                    world\_state=self.\_get\_world\_context()
                )
                for consequence in consequences:
                    tree.add\_child(node, consequence)

        tree = self.\_annotate\_probabilities(tree)

        return tree

    def capture\_opportunity(self, domain: str) -> List\[Opportunity]:
        """机缘捕捉：发现宿主领域内的潜在机会"""
        host = self.harness.get\_host()

        market\_data = self.harness.gateway.query\_external(
            service="market\_intelligence",
            query=domain,
            anonymize=True
        )

        opportunities = \[]
        for trend in market\_data.trends:
            match\_score = self.\_calculate\_match(trend.required\_skills, host.skills)
            if match\_score > 0.7:
                opportunities.append(Opportunity(
                    name=trend.name,
                    type="emerging\_trend",
                    match\_score=match\_score,
                    difficulty=self.\_estimate\_difficulty(trend, host),
                    potential\_return=trend.market\_size,
                    action\_plan=self.\_generate\_action\_plan(trend, host)
                ))

        return opportunities
```

\---

## 五、Memory 记忆模块设计

### 5.1 记忆宫殿架构

```
+--------------------------------------------------------------+
|                     记忆宫殿 Memory Palace                    |
|                                                              |
|  +-------------+  +-------------+  +-------------+         |
|  |  识海 (表层)  |  |  灵台 (中层)  |  |  紫府 (深层)  |         |
|  | Short-term  |  |  Working   |  | Long-term  |         |
|  |  7+-2 件事   |  |  当前任务   |  |  终身记忆   |         |
|  +-------------+  +-------------+  +-------------+         |
|                                                              |
|  +-------------+  +-------------+  +-------------+         |
|  |  功法阁      |  |  历练录      |  |  心魔壁      |         |
|  | 知识/技能记忆 |  | 经历/事件记忆 |  | 失败/教训记忆 |         |
|  +-------------+  +-------------+  +-------------+         |
|                                                              |
|  +-------------+  +-------------+  +-------------+         |
|  |  人脉谱      |  |  资源库      |  |  悟道碑      |         |
|  | 关系网络记忆  |  | 资产/工具记忆 |  | 顿悟/洞察记忆 |         |
|  +-------------+  +-------------+  +-------------+         |
+--------------------------------------------------------------+
```

### 5.2 记忆协议

```python
from dataclasses import dataclass
from datetime import datetime
from typing import List, Any

@dataclass
class MemoryFragment:
    """记忆碎片：记忆的最小单元"""
    fragment\_id: str
    timestamp: datetime
    category: str
    content: Any
    embedding: List\[float]
    emotional\_tag: float
    importance: float
    access\_level: str
    associations: List\[str]
    decay\_rate: float

class MemoryPalaceManager:
    """
    记忆宫殿管理器：宿主的完整记忆系统
    """
    def \_\_init\_\_(self, host\_id: str):
        self.host\_id = host\_id
        self.short\_term = ShortTermMemory(capacity=7)
        self.working = WorkingMemory()
        self.long\_term = LongTermMemory()
        self.technique\_tower = TechniqueTower()
        self.experience\_log = ExperienceLog()
        self.demon\_wall = DemonWall()

    def store(self, fragment: MemoryFragment) -> str:
        """存储记忆：根据重要度决定存储位置"""
        if fragment.importance > 0.8:
            self.long\_term.store(fragment)
            self.technique\_tower.index(fragment)
        elif fragment.importance > 0.5:
            self.working.store(fragment)
        else:
            self.short\_term.store(fragment)

        self.\_build\_associations(fragment)

        return fragment.fragment\_id

    def recall(self, query: str, context: str = "") -> List\[MemoryFragment]:
        """
        回忆：基于查询和上下文检索记忆
        模拟「神识扫描」过程
        """
        query\_vec = self.\_embed(query)

        results = \[]
        results.extend(self.short\_term.similarity\_search(query\_vec, top\_k=3))

        if context:
            results.extend(self.working.context\_search(query\_vec, context, top\_k=5))

        results.extend(self.long\_term.semantic\_search(query\_vec, top\_k=10))

        host\_mood = self.\_get\_host\_mood()
        results = self.\_emotional\_ranking(results, host\_mood)

        return results

    def consolidate(self):
        """
        记忆固化：睡眠/休息时运行
        将工作记忆转长期记忆，强化重要关联
        """
        for fragment in self.working.get\_consolidation\_candidates():
            if self.\_should\_consolidate(fragment):
                self.long\_term.store(fragment)
                self.working.remove(fragment.fragment\_id)

        self.long\_term.strengthen\_associations()
        self.short\_term.decay()
```

### 5.3 宿主画像 HostProfile

```python
@dataclass
class HostProfile:
    """
    宿主画像：金手指系统的核心数据资产
    随时间持续演化，陪伴宿主成长
    """
    host\_id: str
    soul\_mark: str
    created\_at: datetime

    spirit\_root: SpiritRoot
    physique: PhysiquePanel

    realm: Realm
    realm\_progress: float
    total\_cultivation\_time: int

    knowledge\_tree: KnowledgeTree
    skill\_tree: SkillTree

    mental\_state: MentalState
    energy\_level: float
    mood: float

    breakthrough\_history: List\[BreakthroughRecord]
    daily\_logs: List\[DailyLog]

    current\_goals: List\[Goal]
    active\_projects: List\[Project]
    habit\_streaks: Dict\[str, int]

class ProfileEvolutionEngine:
    """
    画像演化引擎：驱动宿主画像的持续更新
    """
    def evolve(self, profile: HostProfile, events: List\[Event]) -> HostProfile:
        """根据事件驱动画像演化"""
        for event in events:
            if event.type == "learning":
                self.\_update\_knowledge\_tree(profile, event)
            elif event.type == "training":
                self.\_update\_physique(profile, event)
            elif event.type == "breakthrough":
                self.\_process\_breakthrough(profile, event)
            elif event.type == "social":
                self.\_update\_relationships(profile, event)
            elif event.type == "reflection":
                self.\_update\_mental\_state(profile, event)

        profile.composite\_score = self.\_calculate\_composite(profile)

        return profile
```

\---

## 六、Protocol 协议设计

### 6.1 宿主协议 HostProtocol

```yaml
# host\_protocol.yaml
protocol\_version: "1.0"
protocol\_name: "GoldenFingerHost"

host\_schema:
  identity:
    host\_id: "uuid\_v4"
    soul\_mark: "sha3\_256\_hash"
    created\_at: "iso\_timestamp"

  privacy\_levels:
    - level: 0
      name: "紫府级"
      desc: "绝对隐私，仅宿主可见，永不出境"
      examples: \["真实姓名", "精确位置", "收入细节", "健康原始数据"]
    - level: 1
      name: "灵台级"
      desc: "技能可用，经脱敏后供内部 Skill 使用"
      examples: \["能力坐标", "学习偏好", "作息规律"]
    - level: 2
      name: "识海级"
      desc: "可对外交互，完全抽象化"
      examples: \["境界等级", "通用能力标签", "匿名化兴趣图谱"]

lifecycle\_events:
  - event: "awakening"
    trigger: "first\_activation"
    actions: \["generate\_soul\_mark", "detect\_spirit\_root", "scan\_physique"]

  - event: "daily\_cultivation"
    trigger: "daily\_login"
    actions: \["load\_profile", "check\_streaks", "generate\_daily\_quests"]

  - event: "breakthrough"
    trigger: "realm\_requirements\_met"
    actions: \["generate\_tribulation", "notify\_host", "update\_privileges"]

  - event: "closed\_seclusion"
    trigger: "host\_command"
    actions: \["block\_distractions", "activate\_deep\_work", "schedule\_breaks"]

  - event: "qi\_deviation"
    trigger: "burnout\_detected"
    actions: \["force\_rest", "deactivate\_skills", "alert\_emergency\_contact"]

data\_egress\_rules:
  - rule: "external\_api\_call"
    condition: "always\_require\_gateway"
    anonymization: "strip\_host\_id, fuzz\_location, abstract\_profile"
    audit: "log\_all\_requests"

  - rule: "skill\_to\_skill"
    condition: "same\_harness\_only"
    permission: "host\_explicit\_consent"
```

### 6.2 技能协议 SkillProtocol

```yaml
# skill\_protocol.yaml
protocol\_version: "1.0"
protocol\_name: "GoldenFingerSkill"

skill\_manifest\_schema:
  required\_fields:
    - name: "技能唯一标识，snake\_case"
    - display\_name: "显示名称，可包含网文风格"
    - category: "knowledge | physical | social | wealth | mental | utility"
    - realm\_requirement: "最低境界要求"
    - version: "语义化版本"

  optional\_fields:
    - description: "技能描述"
    - author: "开发者"
    - dependencies: "依赖的其他 skill 列表"
    - resources: "运行所需资源"
    - hooks: "生命周期钩子"

skill\_lifecycle:
  states:
    - DORMANT
    - UNLOCKED
    - ACTIVE
    - COOLDOWN
    - OVERLOADED
    - SEALED

  transitions:
    - {from: DORMANT, to: UNLOCKED, trigger: "realm\_up"}
    - {from: UNLOCKED, to: ACTIVE, trigger: "host\_activate"}
    - {from: ACTIVE, to: COOLDOWN, trigger: "skill\_use"}
    - {from: COOLDOWN, to: ACTIVE, trigger: "cooldown\_expire"}
    - {from: ACTIVE, to: OVERLOADED, trigger: "excessive\_use"}
    - {from: OVERLOADED, to: ACTIVE, trigger: "rest\_sufficient"}

execution\_context:
  host\_profile: "当前宿主画像快照（只读）"
  session\_history: "本次会话历史"
  environment\_state: "环境状态（时间/地点/设备）"
  resource\_quota: "可用资源配额"

skill\_result\_schema:
  success: "bool"
  data: "Any | 成功时返回的数据"
  error: "str | 失败时错误信息"
  side\_effects: "List\[str] | 副作用描述"
  experience\_gained: "float | 获得的经验值"
  realm\_progress\_delta: "float | 境界进度变化"
```

### 6.3 记忆协议 MemoryProtocol

```yaml
# memory\_protocol.yaml
protocol\_version: "1.0"
protocol\_name: "GoldenFingerMemory"

memory\_schema:
  fragment:
    fragment\_id: "uuid"
    timestamp: "iso\_timestamp"
    category: "knowledge | experience | reflection | social | health | goal"
    content\_type: "text | structured | embedding | binary"
    privacy\_level: "0 | 1 | 2"

  association:
    type: "prerequisite | extension | similar | contradictory | causal"
    strength: "0.0 \~ 1.0"
    bidirectional: "bool"

retrieval\_protocol:
  methods:
    - name: "divine\_sense"
      params: \["query\_vector", "top\_k", "category\_filter"]
    - name: "recall"
      params: \["fragment\_id", "timestamp\_range"]
    - name: "introspection"
      params: \["theme", "depth", "time\_range"]
    - name: "dream\_walk"
      params: \["seed\_fragment", "walk\_steps"]

  ranking\_factors:
    - recency: "时间衰减权重"
    - relevance: "语义相似度"
    - importance: "重要度标签"
    - emotional\_resonance: "情感共鸣（与当前情绪匹配度）"
    - host\_context: "当前任务相关性"

consolidation\_protocol:
  trigger\_conditions:
    - "每日睡眠时段"
    - "主动冥想命令"
    - "短期记忆容量达到 80%"

  process:
    - "筛选候选碎片（importance > 0.6）"
    - "强化高频关联路径"
    - "合并相似碎片"
    - "生成抽象层次记忆"
    - "清理过期低价值碎片"
```

### 6.4 地图协议 MapProtocol

```yaml
# map\_protocol.yaml
protocol\_version: "1.0"
protocol\_name: "GoldenFingerMap"

location\_privacy:
  raw\_coordinates: "level\_0"
  fuzzy\_coordinates: "level\_1"
  abstract\_location: "level\_2"

navigation\_request:
  origin: "current | anchor\_name | coordinates"
  destination: "text\_query | anchor\_name | coordinates"
  mode: "optimal | fastest | cheapest | cultivation | exploration"
  constraints:
    - "max\_duration"
    - "max\_cost"
    - "required\_pois"
    - "avoidance\_zones"

anchor\_schema:
  anchor\_id: "uuid"
  name: "str"
  raw\_coords: "encrypted"
  display\_coords: "fuzzed"
  category: "home | work | gym | study | leisure | custom"
  access\_count: "int"
  last\_access: "timestamp"
  emotional\_valence: "float"

exploration\_request:
  center: "current | anchor\_name | coordinates"
  radius: "int | meters"
  filters:
    categories: \["cafe", "library", "gym", "park", "museum"]
    min\_rating: "float"
    open\_now: "bool"

  cultivation\_mode: "bool"
```

\---

## 七、网文概念映射详解

### 7.1 功法系统 -> 学习系统

|网文元素|现实映射|系统实现|
|-|-|-|
|功法名称|课程/技能名称|`SkillManifest.display\_name`|
|功法品阶|课程难度/权威性|`difficulty: beginner|
|功法层数|课程章节/阶段|`layers: List\[Layer]`|
|修炼进度|学习进度|`progress: 0.0 \~ 1.0`|
|功法熟练度|技能熟练度|`proficiency: 0 \~ 100`|
|功法冲突|课程时间冲突|`conflict\_detection`|
|功法融合|跨学科项目|`fusion\_projects`|
|自创功法|构建个人知识体系|`custom\_knowledge\_system`|

```python
class Technique(SkillManifest):
    """
    功法：学习路径的网文化封装
    """
    technique\_name: str
    tier: TechniqueTier
    layers: List\[TechniqueLayer]
    cultivation\_method: str

class TechniqueLayer:
    """功法层数：课程阶段"""
    layer\_number: int
    name: str
    content: List\[KnowledgePoint]
    exercises: List\[Exercise]
    breakthrough\_test: Test

    def check\_completion(self, host: HostProfile) -> bool:
        """检查该层是否修炼圆满"""
        return all(
            host.knowledge\_tree.is\_mastered(kp.id)
            for kp in self.content
        ) and self.breakthrough\_test.is\_passed(host)
```

### 7.2 战斗系统 -> 面试/考核系统

|网文元素|现实映射|系统实现|
|-|-|-|
|战力值|综合竞争力评分|`combat\_power: composite\_score`|
|招式|面试回答模板|`moves: List\[Move]`|
|法宝|项目作品集|`artifacts: List\[Project]`|
|对手|目标公司/岗位|`opponent: TargetJob`|
|切磋|模拟面试|`sparring: MockInterview`|
|生死战|正式面试|`life\_death\_battle: RealInterview`|
|越级挑战|跳级申请|`realm\_skip\_challenge`|

### 7.3 宗门系统 -> 社群/组织系统

|网文元素|现实映射|系统实现|
|-|-|-|
|宗门|学习社群/公司|`sect: Community`|
|掌门|导师/领导|`leader: Mentor`|
|长老|资深成员|`elders: List\[Senior]`|
|内门弟子|核心成员|`inner\_disciples: List\[CoreMember]`|
|外门弟子|普通成员|`outer\_disciples: List\[Member]`|
|宗门任务|团队项目|`sect\_missions: List\[Project]`|
|宗门大比|竞赛/考核|`sect\_competition: Competition`|
|宗门资源|共享知识库|`sect\_library: SharedKnowledge`|

\---

## 八、系统交互流程

### 8.1 每日修炼流程

```
\[宿主唤醒]
    |
    v
\[Harness 加载宿主画像]
    |
    v
\[识海扫描：检查今日状态]
    +- 精力值检测
    +- 待办事项同步
    +- 习惯连续性检查
    |
    v
\[生成今日修炼任务]
    +- 主线任务（当前核心目标）
    +- 支线任务（技能训练）
    +- 日常任务（习惯维持）
    +- 奇遇任务（随机机会）
    |
    v
\[宿主选择任务开始修炼]
    |
    v
\[激活对应 Skill]
    +- KnowledgeAbsorption -> 学习课程
    +- BodyRefinement -> 运动训练
    +- MapNavigation -> 外出导航
    +- ...
    |
    v
\[Skill 执行并反馈]
    +- 进度更新
    +- 经验值增加
    +- 副作用记录（疲劳等）
    |
    v
\[Harness 更新宿主画像]
    +- 记忆存储
    +- 属性重新计算
    +- 突破检测
    |
    v
\[生成修炼报告]
    +- 今日收益
    +- 境界进度
    +- 明日建议
```

### 8.2 突破流程

```
\[突破条件检测]
    |
    v
\[满足条件] --> \[生成突破试炼]
    |              +- 知识试炼：综合测试
    |              +- 身体试炼：体能挑战
    |              +- 心魔试炼：压力面试/难题
    |              +- 因果试炼：项目实战
    |
    v
\[宿主接受试炼]
    |
    v
\[执行试炼]
    +- 成功 -> 境界提升 + 新 Skill 解锁 + 权限提升
    +- 失败 -> 积累突破经验 + 弱点分析 + 下次准备建议
```

\---

## 九、API 与集成设计

### 9.1 高德地图集成

```python
class AMapIntegration:
    """
    高德地图集成：实现缩地成寸、秘境探索
    所有调用经隔离网关，坐标脱敏
    """

    def \_\_init\_\_(self, gateway: Gateway):
        self.gateway = gateway
        self.base\_url = "https://restapi.amap.com/v3"

    def geocode(self, address: str) -> Coordinates:
        """地理编码：地址转坐标"""
        result = self.gateway.query\_external(
            service="amap",
            endpoint="geocode/geo",
            params={"address": address, "key": "${AMAP\_KEY}"}
        )
        return Coordinates(
            lng=result\["geocodes"]\[0]\["location"].split(",")\[0],
            lat=result\["geocodes"]\[0]\["location"].split(",")\[1]
        )

    def direction(self, origin: Coordinates, destination: Coordinates,
                  mode: str = "driving") -> Route:
        """路径规划"""
        result = self.gateway.query\_external(
            service="amap",
            endpoint="direction",
            params={
                "origin": f"{origin.lng},{origin.lat}",
                "destination": f"{destination.lng},{destination.lat}",
                "mode": mode,
                "key": "${AMAP\_KEY}"
            }
        )
        return self.\_parse\_route(result)

    def search\_poi(self, location: Coordinates, keywords: str,
                   radius: int = 5000) -> List\[POI]:
        """周边搜索：秘境探索"""
        result = self.gateway.query\_external(
            service="amap",
            endpoint="place/around",
            params={
                "location": f"{location.lng},{location.lat}",
                "keywords": keywords,
                "radius": radius,
                "key": "${AMAP\_KEY}"
            }
        )
        return \[self.\_parse\_poi(p) for p in result\["pois"]]
```

### 9.2 外部知识库集成

```python
class KnowledgeBaseIntegration:
    """
    外部知识库：经隔离网关访问
    用于功法阁建设、经脉路线图生成
    """

    def search(self, query: str, host\_profile: HostProfile) -> SearchResult:
        """
        搜索知识：自动根据宿主境界过滤难度
        练气期不返回化神期内容
        """
        realm\_aware\_query = self.\_adapt\_query\_to\_realm(query, host\_profile.realm)

        result = self.gateway.query\_external(
            service="knowledge\_base",
            query=realm\_aware\_query,
            filters={"difficulty\_max": host\_profile.realm.value},
            anonymize=True
        )

        return result
```

\---

## 十、安全与隐私设计

### 10.1 数据隔离矩阵

|数据类型|存储位置|访问控制|出境规则|
|-|-|-|-|
|真实身份信息|本地加密存储|宿主密码+生物特征|禁止出境|
|精确地理位置|本地缓存|宿主授权+时效限制|100m模糊后出境|
|健康原始数据|本地/私有云|宿主完全控制|仅统计特征出境|
|学习记录详情|系统数据库|Skill内部分享|脱敏后出境|
|能力坐标|系统数据库|匿名化后共享|可出境|
|境界等级|系统数据库|公开|可出境|

### 10.2 心魔防御系统

```python
class DemonDefenseSystem:
    """
    心魔防御：检测并防御对宿主有害的内容/行为
    """

    def scan\_incoming(self, data: Any, source: str) -> SafetyReport:
        """入境扫描：检测外部数据是否安全"""
        checks = {
            "privacy\_leak": self.\_check\_privacy\_leak(data),
            "toxic\_content": self.\_check\_toxicity(data),
            "misinformation": self.\_check\_facts(data),
            "addictive\_design": self.\_check\_addictive\_patterns(data)
        }

        if not all(checks.values()):
            return SafetyReport(
                safe=False,
                blocked\_reasons=\[k for k, v in checks.items() if not v],
                sanitized\_version=self.\_sanitize(data)
            )

        return SafetyReport(safe=True)

    def monitor\_host\_state(self, host: HostProfile) -> MentalHealthReport:
        """宿主状态监控：检测走火入魔征兆"""
        indicators = {
            "sleep\_deprivation": host.sleep\_avg < 6,
            "overwork": host.daily\_work\_hours > 12,
            "isolation": host.social\_interaction\_7d < 2,
            "mood\_crash": host.mood\_7d\_avg < -0.5,
            "burnout\_risk": self.\_calculate\_burnout\_risk(host)
        }

        if any(indicators.values()):
            return MentalHealthReport(
                alert\_level=self.\_calculate\_alert\_level(indicators),
                recommendations=self.\_generate\_interventions(indicators),
                should\_force\_rest=indicators\["burnout\_risk"] > 0.8
            )

        return MentalHealthReport(alert\_level="normal")
```

\---

## 十一、部署与运行架构

### 11.1 本地优先架构

```
+--------------------------------------------------------------+
|                      宿主设备 (本地)                           |
|  +--------------+  +--------------+  +--------------+       |
|  |  金手指核心   |  |   记忆数据库  |  |   本地模型   |       |
|  |   Harness    |  |  (SQLite/    |  |  (LLM边缘端) |       |
|  |              |  |   VectorDB)  |  |              |       |
|  +------+-------+  +--------------+  +--------------+       |
|         |                                                    |
|  +------+-------+                                            |
|  |   隔离网关    | <-- 所有外部请求必经                       |
|  |   Gateway    |                                            |
|  +------+-------+                                            |
+---------+-----------------------------------------------------+
          |
          v
+--------------------------------------------------------------+
|                      外部服务层                                |
|  +----------+ +----------+ +----------+ +----------+        |
|  | 高德地图  | | 云端LLM  | | 知识库   | | 健康设备  |        |
|  +----------+ +----------+ +----------+ +----------+        |
+--------------------------------------------------------------+
```

### 11.2 容器化部署

```dockerfile
# Dockerfile
FROM python:3.11-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY harness/ ./harness/
COPY skills/ ./skills/
COPY protocols/ ./protocols/
COPY memory/ ./memory/

VOLUME \["/app/data/host\_profiles", "/app/data/memory\_palace"]

ENV HARNESS\_MODE=production
ENV GATEWAY\_STRICT\_MODE=true
ENV MEMORY\_ENCRYPTION=true

EXPOSE 8080

CMD \["python", "-m", "harness.main"]
```

```yaml
# docker-compose.yaml
version: "3.8"

services:
  golden-finger:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - host\_data:/app/data/host\_profiles
      - memory\_data:/app/data/memory\_palace
    environment:
      - AMAP\_KEY=${AMAP\_KEY}
      - OPENAI\_API\_KEY=${OPENAI\_API\_KEY}
      - ENCRYPTION\_KEY=${ENCRYPTION\_KEY}
    networks:
      - golden-finger-net
    restart: unless-stopped

  chromadb:
    image: chromadb/chroma:latest
    volumes:
      - chroma\_data:/chroma/chroma
    networks:
      - golden-finger-net

volumes:
  host\_data:
  memory\_data:
  chroma\_data:

networks:
  golden-finger-net:
    driver: bridge
```

\---

## 十二、开发路线图

### Phase 1: 觉醒期 (MVP)

* \[ ] Harness 核心框架搭建
* \[ ] 宿主画像系统（基础版）
* \[ ] 知识汲取术 Skill（课程学习路径）
* \[ ] 记忆宫殿（基础存储与检索）
* \[ ] 高德地图集成（基础导航）
* \[ ] 内外隔离网关（基础版）

### Phase 2: 筑基期

* \[ ] 境界系统完整实现
* \[ ] 身体淬炼术 Skill（健康数据接入）
* \[ ] 神识记忆术 Skill（记忆宫殿高级功能）
* \[ ] 缩地成寸 Skill（空间锚点+秘境探索）
* \[ ] 突破引擎与试炼系统
* \[ ] 因果推演术 Skill（基础版）

### Phase 3: 金丹期

* \[ ] 多 Skill 协同（功法融合）
* \[ ] 宗门/社群系统
* \[ ] 战斗系统（面试/考核模拟）
* \[ ] 心魔防御系统
* \[ ] 多端同步（手机/手表/PC）

### Phase 4: 元婴期

* \[ ] AI 导师（个性化修炼指导）
* \[ ] 预测性干预（机缘捕捉+灾厄预警）
* \[ ] 开放 Skill 市场（第三方开发）
* \[ ] 跨宿主匿名化对比（排行榜/均值）

\---

## 十三、附录

### A. 术语表

|术语|英文|含义|
|-|-|-|
|金手指|GoldenFinger|本系统的代称|
|宿主|Host|系统使用者|
|天道|Harness|系统核心框架|
|功法|Technique|学习路径/课程|
|灵根|SpiritRoot|认知风格/天赋模型|
|体质|Physique|身体基线数据|
|神识|DivineSense|记忆与认知能力|
|识海|SeaOfConsciousness|短期记忆区|
|灵台|SpiritPlatform|工作记忆区|
|紫府|PurpleMansion|长期记忆区|
|经脉|Meridian|知识图谱路径|
|穴位|Acupoint|关键知识点|
|周天|ZhouTian|复习循环|
|突破|Breakthrough|能力阶段跃升|
|渡劫|Tribulation|突破试炼|
|心魔|InnerDemon|心理障碍/ burnout|
|法宝|Artifact|效率工具|
|缩地成寸|MapNavigation|地图导航|
|空间锚点|SpaceAnchor|常用地点标记|
|秘境|SecretRealm|有价值的周边地点|
|机缘|Opportunity|潜在机会|
|因果|Causality|决策推演|

### B. 接口速查

```python
# Harness 主入口
harness = GoldenFingerHarness()
host = harness.awaken(host\_profile)

# Skill 调用
result = harness.activate\_skill(
    skill\_name="knowledge\_absorption",
    context=ExecutionContext(params={"target": "Python进阶"})
)

# 记忆操作
harness.memory.store(fragment)
memories = harness.memory.recall(query="上次学的递归")

# 导航
route = harness.activate\_skill(
    skill\_name="map\_navigation",
    context=ExecutionContext(params={
        "destination": "国家图书馆",
        "mode": "cultivation"
    })
)

# 画像查询
profile = harness.get\_host\_profile()
print(f"当前境界: {profile.realm.name}")
print(f"战力值: {profile.composite\_score}")
```

\---

> \*\*后记\*\*
>
> 此系统设计旨在将网文的沉浸感与成长爽感引入现实个人发展。通过 Harness-Skill-Protocol 的 Agent 架构，实现了一个既具备玄幻美学、又严格遵循工程规范的「金手指」系统。
>
> 核心原则：\*\*数据主权归宿主，成长路径可量化，外界交互经隔离，记忆陪伴共进化\*\*。

