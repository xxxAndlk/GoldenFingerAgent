# 金手指 Agent 系统 — 实施路线图

> 基于《金手指 Agent 系统设计文档》v1.0 整理的可执行步骤，按阶段、模块、任务三级拆分。

---

## Phase 1: 觉醒期 (MVP)

目标：跑通 Harness → Skill → Protocol 最小闭环。

---

### 1.1 项目骨架搭建

- [ ] **1.1.1** 初始化项目仓库，确定目录结构
  ```
  golden-finger/
  ├── harness/          # 核心框架
  ├── skills/           # 技能插件
  ├── protocols/        # 协议定义
  ├── memory/           # 记忆模块
  ├── gateway/          # 隔离网关
  ├── tests/            # 测试
  └── docs/             # 文档
  ```
- [ ] **1.1.2** 配置开发环境：Python 3.11+、虚拟环境、依赖管理
- [ ] **1.1.3** 搭建本地数据存储（SQLite + ChromaDB）

### 1.2 Harness 核心框架

- [ ] **1.2.1** 实现 `HostEngine` 宿主引擎
  - `_generate_soul_mark()` — 灵魂印记生成
  - `initialize()` — 宿主初始化流程
  - `_detect_spirit_root()` — 灵根检测（五行能力映射）
  - `_scan_physique()` — 体质扫描
  - `_measure_divine_sense()` — 神识测量
- [ ] **1.2.2** 实现 `Realm` 境界枚举（MORTAL → TRUE_IMMORTAL）
- [ ] **1.2.3** 实现 `HostState` 状态机（INITIALIZING / ACTIVE / RESTING / SEALED）
- [ ] **1.2.4** 实现 `AuraProfile` 气息画像数据结构
- [ ] **1.2.5** 实现 `Scheduler` 调度中枢
  - 任务队列管理
  - Skill 激活调度
  - 资源分配
- [ ] **1.2.6** 实现 `EventBus` 事件总线
  - 事件注册与分发
  - 网文事件类型定义（awakening / daily_cultivation / qi_deviation）

### 1.3 宿主画像系统（基础版）

- [ ] **1.3.1** 实现 `HostProfile` 数据模型
  - 基本信息（host_id / soul_mark / created_at）
  - 灵根数据（SpiritRoot）
  - 体质数据（PhysiquePanel）
  - 境界信息（realm / realm_progress）
- [ ] **1.3.2** 实现 `ProfileEvolutionEngine` 画像演化引擎
  - 学习事件处理
  - 训练事件处理
  - 每日画像快照
- [ ] **1.3.3** 实现数据持久化（本地加密存储）

### 1.4 知识汲取术 Skill — KnowledgeAbsorption

- [ ] **1.4.1** 定义 `SkillManifest` 数据结构
- [ ] **1.4.2** 实现 `BaseSkill` 基类
  - `activate()` 抽象方法
  - `train()` 抽象方法
  - `can_activate()` 前置检查
  - `_calculate_affinity()` 灵根契合度
- [ ] **1.4.3** 实现 `KnowledgeAbsorption` 技能
  - `_generate_meridian_map()` — 经脉路线图（知识图谱）生成
  - `_detect_blockages()` — 瓶颈穴位检测
  - `_estimate_cultivation_time()` — 修炼时间预估
  - `_select_method()` — 最优学习方法推荐
- [ ] **1.4.4** 实现 `KnowledgeGraph` 知识图谱数据结构
  - 主脉（核心知识线）
  - 支脉（前置知识）
  - 穴位（关键知识点）
  - 周天循环（复习路径）
- [ ] **1.4.5** 扩写 `SkillState` 状态机（DORMANT / UNLOCKED / ACTIVE / COOLDOWN）

### 1.5 记忆宫殿（基础存储与检索）

- [ ] **1.5.1** 实现 `MemoryFragment` 数据模型
  - 记忆 ID / 时间戳 / 分类 / 内容 / 向量 / 情感标签 / 重要度 / 衰减率
- [ ] **1.5.2** 实现 `ShortTermMemory` 识海（容量 7±2，FIFO 淘汰）
- [ ] **1.5.3** 实现 `WorkingMemory` 灵台（当前任务上下文）
- [ ] **1.5.4** 实现 `LongTermMemory` 紫府（ChromaDB 向量存储）
- [ ] **1.5.5** 实现 `MemoryPalaceManager` 管理器
  - `store()` — 按重要度分级存储
  - `recall()` — 多级检索（短期→工作→长期）
- [ ] **1.5.6** 实现 `_embed()` 文本向量化（调用 embedding 模型）

### 1.6 隔离网关 Gateway（基础版）

- [ ] **1.6.1** 实现 `Gateway` 核心类
  - `query_external()` — 统一外部调用入口
  - 请求日志记录
  - 基本脱敏处理
- [ ] **1.6.2** 实现 `Anonymizer` 匿名化器
  - `strip_host_id()` — 剥离宿主标识
  - `fuzz_location()` — 位置模糊化（精度降至 100m）
  - `abstract_profile()` — 画像抽象化
- [ ] **1.6.3** 实现 `SafetyFilter` 安全过滤器（基础版）
  - 关键词过滤
  - 响应长度限制

### 1.7 高德地图集成

- [ ] **1.7.1** 实现 `AMapIntegration` 类
  - `geocode()` — 地理编码
  - `direction()` — 路径规划
  - `search_poi()` — 周边搜索
- [ ] **1.7.2** 实现 API Key 安全存储（环境变量 → Gateway 注入）
- [ ] **1.7.3** 编写高德 API Mock，支持离线开发测试

---

## Phase 2: 筑基期

目标：境界系统完整化，新增身体/神识/地图三大 Skill。

---

### 2.1 境界系统完整实现

- [ ] **2.1.1** 实现 `BreakthroughEngine` 突破引擎
  - `check_breakthrough()` — 检测突破条件
  - `_get_requirements()` — 获取当前境界→下一境要求
  - `_generate_tribulation()` — 生成突破试炼
- [ ] **2.1.2** 定义各境界突破条件表
  - 知识覆盖率阈值
  - 技能熟练度阈值
  - 体质综合指数阈值
  - 精神状态阈值
  - 现实影响力阈值
- [ ] **2.1.3** 实现 `BreakthroughReport` 突破报告
  - 就绪状态 / 不足项 / 试炼内容

### 2.2 身体淬炼术 Skill — BodyRefinement

- [ ] **2.2.1** 实现 `BodyRefinement` 技能
  - `scan_physique()` — 体质扫描（力量/敏捷/耐力/活力/防御）
- [ ] **2.2.2** 实现可穿戴设备数据接入（Apple Health / 小米运动 等）
- [ ] **2.2.3** 实现 `generate_refinement_plan()` — 淬体方案生成
  - 减脂 → 轻身术
  - 增肌 → 金刚诀
  - 柔韧 → 瑜伽经
  - 通用 → 五禽戏
- [ ] **2.2.4** 实现训练计划生成器
  - `_generate_schedule()` — 训练排期
  - `_set_milestones()` — 里程碑设定
  - `_generate_nutrition_plan()` — 营养方案
  - `_generate_recovery_plan()` — 恢复方案
- [ ] **2.2.5** 实现身体数据趋势分析（周/月维度）

### 2.3 神识记忆术 Skill — DivineSenseMemory

- [ ] **2.3.1** 实现 `DivineSenseMemory` 技能
  - `construct_palace()` — 记忆宫殿构建
  - `divine_search()` — 神识检索（自然语言 → 向量搜索）
- [ ] **2.3.2** 实现 `MemoryPalace` 数据结构
  - 房间（Room）/ 锚点（Anchor）/ 连接（Connection）
- [ ] **2.3.3** 实现遗忘抵御系统
  - 艾宾浩斯遗忘曲线建模
  - 个性化复习提醒
  - `decay_rate` 动态调整
- [ ] **2.3.4** 实现顿悟触发机制
  - 跨领域知识关联检测
  - 高关联度碎片自动推荐

### 2.4 缩地成寸 Skill — MapNavigation

- [ ] **2.4.1** 实现 `MapNavigation` 技能
  - `navigate()` — 多模式导航（最优/最快/最省/修炼）
  - `set_space_anchor()` — 空间锚点设置
  - `teleport()` — 一键导航至锚点
- [ ] **2.4.2** 实现 `explore_secret_realms()` — 秘境探索
  - 周边 POI 搜索
  - 按宿主需求排序
  - 修炼价值评分
- [ ] **2.4.3** 实现修炼模式路线
  - 途经学习/锻炼相关地点
  - 路程时间融入学习时段
- [ ] **2.4.4** 实现空间锚点管理（CRUD + 加密存储）

### 2.5 突破引擎与试炼系统

- [ ] **2.5.1** 实现试炼类型
  - 知识试炼：综合测试/项目考核
  - 身体试炼：体能挑战目标
  - 心魔试炼：压力场景模拟
  - 因果试炼：真实项目实战
- [ ] **2.5.2** 实现试炼结果评估
  - 成功 → 境界提升 + 新 Skill 解锁
  - 失败 → 弱点分析 + 积累突破经验
- [ ] **2.5.3** 实现 `Tribulation` 试炼生成算法
  - 基于宿主弱点定制
  - 难度自适应

### 2.6 因果推演术 Skill — CausalityDeduction（基础版）

- [ ] **2.6.1** 实现 `CausalityDeduction` 技能
  - `deduce()` — 因果推演（行动→连锁反应）
- [ ] **2.6.2** 实现 `CausalityTree` 因果树
  - 多层级节点
  - 概率标注
  - 可视化输出
- [ ] **2.6.3** 实现 `capture_opportunity()` — 机缘捕捉（基础版）
  - 领域趋势匹配
  - 宿主技能匹配度计算

---

## Phase 3: 金丹期

目标：多 Skill 协同 + 社群系统 + 战斗系统 + 心魔防御。

---

### 3.1 多 Skill 协同（功法融合）

- [ ] **3.1.1** 实现 Skill 间依赖管理
  - 前置 Skill 检查
  - 资源冲突检测
- [ ] **3.1.2** 实现功法融合
  - 跨学科知识图谱合并
  - 融合项目自动生成
- [ ] **3.1.3** 实现自创功法功能
  - 宿主自定义知识体系
  - 学习路径编辑器

### 3.2 宗门/社群系统

- [ ] **3.2.1** 实现 `Sect`（宗门）数据模型
  - 掌门 / 长老 / 内门弟子 / 外门弟子
  - 宗门资源库
  - 宗门任务
- [ ] **3.2.2** 实现宗门任务系统
  - 任务创建与分配
  - 进度追踪
  - 贡献值计算
- [ ] **3.2.3** 实现宗门大比（竞赛/考核）
  - 匿名化排行榜
  - 能力坐标对比
- [ ] **3.2.4** 实现宗门知识库共享
  - 脱敏知识贡献
  - 宗门图书馆

### 3.3 战斗系统（面试/考核模拟）

- [ ] **3.3.1** 实现战力值计算 `composite_score`
  - 知识维度权重
  - 技能维度权重
  - 身体维度权重
  - 心态维度权重
- [ ] **3.3.2** 实现招式系统 `Move`
  - 面试回答模板库
  - 常见问题拆解
  - 招式熟练度
- [ ] **3.3.3** 实现法宝系统 `Artifact`
  - 项目作品集管理
  - 证书/资质记录
  - 作品评分
- [ ] **3.3.4** 实现切磋模式 `Sparring`
  - AI 模拟面试官
  - 实时反馈与评分
  - 弱点分析报告
- [ ] **3.3.5** 实现正式面试追踪 `LifeDeathBattle`
  - 面试日程管理
  - 面试复盘记录
  - 成功率预测

### 3.4 心魔防御系统

- [ ] **3.4.1** 实现 `DemonDefenseSystem`
  - `scan_incoming()` — 入境内容安全扫描
  - `monitor_host_state()` — 宿主状态监控
- [ ] **3.4.2** 实现入境扫描规则
  - 隐私泄露检测
  - 有害内容过滤
  - 虚假信息识别
  - 成瘾设计检测
- [ ] **3.4.3** 实现宿主状态监控
  - 睡眠不足检测
  - 过劳检测
  - 社交孤立检测
  - 情绪崩溃预警
  - burnout 风险评估
- [ ] **3.4.4** 实现干预建议生成
  - 强制休息触发
  - 紧急联系人通知（走火入魔时）
  - 恢复方案推荐

### 3.5 多端同步

- [ ] **3.5.1** 实现同步协议
- [ ] **3.5.2** 手机端适配（移动 Web / 小程序）
- [ ] **3.5.3** 手表端适配（健康数据采集）
- [ ] **3.5.4** 数据冲突解决策略

---

## Phase 4: 元婴期

目标：AI 导师 + 预测干预 + 开放生态。

---

### 4.1 AI 导师（个性化修炼指导）

- [ ] **4.1.1** 实现 LLM 驱动的修炼导师
  - 基于宿主画像的个性化建议
  - 瓶颈突破指导
  - 学习路径动态调整
- [ ] **4.1.2** 实现导师对话历史管理
- [ ] **4.1.3** 实现导师人格定制（严格/温和/幽默 等）

### 4.2 预测性干预

- [ ] **4.2.1** 机缘捕捉增强版
  - 实时市场/行业情报接入
  - 机会与宿主技能自动匹配
  - 行动方案自动生成
- [ ] **4.2.2** 灾厄预警系统
  - 行业风险识别
  - 个人瓶颈预警
  - 规避策略推荐

### 4.3 开放 Skill 市场

- [ ] **4.3.1** 定义第三方 Skill 开发规范
- [ ] **4.3.2** 实现 Skill 注册/发现机制
- [ ] **4.3.3** 实现 Skill 沙箱（安全隔离运行）
- [ ] **4.3.4** 实现 Skill 评分与评论系统

### 4.4 跨宿主匿名化对比

- [ ] **4.4.1** 实现匿名化排行榜
  - 境界排行榜
  - 各维度能力排行榜
- [ ] **4.4.2** 实现群体均值参考
  - 同龄/同领域对比
  - 进度基准线

---

## 贯穿所有阶段的持续任务

### P0. 协议层完善

- [ ] **P0.1** 宿主协议 `HostProtocol` 实现
  - 生命周期事件定义
  - 隐私等级管理（紫府级/灵台级/识海级）
  - 数据出境规则引擎
- [ ] **P0.2** 技能协议 `SkillProtocol` 实现
  - `SkillManifest` 验证
  - 技能生命周期管理
  - 执行上下文传递规范
- [ ] **P0.3** 记忆协议 `MemoryProtocol` 实现
  - 记忆 CRUD 规范
  - 检索协议（divine_sense / recall / introspection / dream_walk）
  - 记忆固化流程
- [ ] **P0.4** 地图协议 `MapProtocol` 实现
  - 位置隐私等级定义
  - 导航请求规范
  - 锚点管理规范

### P1. 安全与隐私

- [ ] **P1.1** 数据隔离矩阵实现
  - 真实身份 → 本地加密存储
  - 精确位置 → 本地缓存 + 时效限制
  - 健康数据 → 仅统计特征出境
- [ ] **P1.2** 存储加密（SQLite 加密 / 向量库加密）
- [ ] **P1.3** 传输加密（TLS + 请求签名）
- [ ] **P1.4** 审计日志系统

### P2. 测试与质量

- [ ] **P2.1** 单元测试（每个模块）
- [ ] **P2.2** 集成测试（Harness + Skill 联调）
- [ ] **P2.3** API Mock 体系（高德/LLM/知识库 Mock）
- [ ] **P2.4** 端到端测试（完整觉醒→修炼→突破流程）

### P3. DevOps

- [ ] **P3.1** Dockerfile + docker-compose 就绪
- [ ] **P3.2** CI/CD 流水线
- [ ] **P3.3** 环境变量管理（AMAP_KEY / ENCRYPTION_KEY 等）
- [ ] **P3.4** 监控与告警

---

## 架构层级速查

```
Layer 1 — 交互界面层
  └── CLI / Web / 移动端 / 语音

Layer 2 — Harness 核心层
  ├── HostEngine      宿主引擎
  ├── Scheduler       调度中枢
  ├── Sandbox         安全沙箱
  ├── Memory          记忆宫殿
  ├── Cultivation     修炼系统（Realm + Breakthrough）
  └── Map             地图系统

Layer 3 — Skills 技能层
  ├── KnowledgeAbsorption   知识汲取术
  ├── BodyRefinement        身体淬炼术
  ├── DivineSenseMemory     神识记忆术
  ├── MapNavigation         缩地成寸
  ├── CausalityDeduction    因果推演术
  ├── SocialDeduction       社交推演术（待设计）
  ├── WealthManagement      财富运筹术（待设计）
  ├── TimePerception        时间法则（待设计）
  ├── SpacePerception       空间感知（待设计）
  └── DemonDefense          心魔抵御

Layer 4 — Protocol 协议层
  ├── HostProtocol      宿主协议
  ├── SkillProtocol     技能协议
  ├── MemoryProtocol    记忆协议
  └── MapProtocol       地图协议

Layer 5 — Gateway 网关层（内外隔离）
  ├── Anonymizer        匿名化器
  ├── SafetyFilter      入境过滤器
  └── APIGateway        外部 API 代理
```

---

> **排序原则**：
> - 纵向按 Phase 1→4 递进，每 Phase 内部先 Harness 核心 → 再 Skill 扩展 → 最后系统集成
> - 横向每个模块内部按 数据模型 → 核心逻辑 → 对外接口 的顺序实现
> - P0~P3 跨阶段任务随主流程并行推进
