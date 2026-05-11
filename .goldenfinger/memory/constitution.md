# GoldenFinger 项目宪章

## 核心原则

1. **规范先行** — 任何代码变更前必须有对应的规格文档（spec.md）。模糊的需求通过 `/clarify` 澄清后再进入开发。
2. **测试强制** — 所有代码变更遵循 TDD RED-GREEN-REFACTOR 循环。没有测试的代码不得合入主分支。
3. **安全第一** — 所有工具调用经过沙箱校验。PII 数据永不落盘，仅在内存中处理。
4. **持续进化** — 每次执行后自动沉淀经验到技能知识库。GapAnalyzer 定期扫描缺失技能并建议创建。

## 开发工作流

1. 用户输入 → 触发 brainstorming skill (自动)
2. 需求澄清 → /propose 或 /specify 生成规格
3. 规格审查 → /clarify 消除歧义
4. 技术规划 → /plan 生成实现方案
5. 任务分解 → /tasks 生成有序任务 DAG
6. 编码实现 → /apply 或 /implement (含 TDD 门禁)
7. 代码审查 → CodeReviewGate (自动触发)
8. 归档沉淀 → /archive 更新基线规格

## 代码质量标准

- 函数最多 50 行，文件最多 500 行
- 所有公开 API 必须有类型标注
- 错误信息使用中文描述
- 日志级别：system/steps INFO，api_request/response DEBUG

## 测试标准

- 单元测试覆盖率 > 80%
- 集成测试覆盖所有工具调用路径
- 每个 bug 修复必须有回归测试

## 安全标准

- 所有 file_write 操作路径必须在 ALLOWED_DIRS 内
- shell_exec 命令必须通过 DANGEROUS_SHELL_PATTERNS 检查
- EgressAnonymizer 在输出前脱敏所有 PII
