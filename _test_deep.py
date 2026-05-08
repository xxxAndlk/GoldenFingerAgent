"""深层验证：检查各模块导入和基本功能"""
import sys, traceback

def test_import(name, code):
    try:
        exec(code, {})
        print(f"  [PASS] {name}")
        return True
    except Exception as e:
        print(f"  [FAIL] {name}: {e}")
        traceback.print_exc()
        return False

print("=== 模块导入测试 ===")
tests = [
    ("models", "from golden_finger.models import HostProfile, TaskPlan, ExecutionReport"),
    ("config", "from golden_finger.config import config"),
    ("llm", "from golden_finger.llm import LLMClient, LLMError"),
    ("harness", "from golden_finger.harness import GoldenFingerHarness"),
    ("api", "from golden_finger.api import app, get_harness, setup_static"),
    ("domain_analysis", "from golden_finger.domain_analysis import PlanGenerator, IntentClassifier"),
    ("domain_execution", "from golden_finger.domain_execution import ExecutionOrchestrator, SingleTaskExecutor, ToolExecutionGuard"),
    ("domain_verification", "from golden_finger.domain_verification import VerificationEngine, StructureChecker, ContentChecker, ReplayChecker, RollbackEngine"),
    ("domain_persistence", "from golden_finger.domain_persistence import PersistenceEngine, ExecutionSummarizer, GapAnalyzer, SkillUpdater, ProfileUpdater"),
    ("domain_isolation", "from golden_finger.domain_isolation import EgressAnonymizer, IngressFilter, ProfileEvolver, DataLevel"),
    ("skills.base", "from golden_finger.skills.base import BaseSkill"),
    ("skills.registry", "from golden_finger.skills.registry import SkillRegistry, skill_registry"),
    ("skills.knowledge", "from golden_finger.skills.knowledge import KnowledgeAbsorption"),
    ("skills.code_assistant", "from golden_finger.skills.code_assistant import CodeAssistant"),
    ("skills.file_operations", "from golden_finger.skills.file_operations import FileOperations"),
    ("tools.base", "from golden_finger.tools.base import BaseTool, ToolResult"),
    ("tools.builtin", "from golden_finger.tools.builtin import BUILTIN_TOOLS, FileReadTool, FileWriteTool, ShellExecTool, WebSearchTool"),
    ("tools.sandbox", "from golden_finger.tools.sandbox import Sandbox, SandboxError"),
    ("storage.sqlite_store", "from golden_finger.storage.sqlite_store import SQLiteStore"),
    ("storage.vector_store", "from golden_finger.storage.vector_store import VectorStore, vector_store"),
    ("tui.app", "from golden_finger.tui.app import GoldenFingerApp"),
]

results = {}
for name, code in tests:
    results[name] = test_import(name, code)

passed = sum(1 for v in results.values() if v)
total = len(results)
print(f"\n=== 结果: {passed}/{total} 通过 ===")

if passed < total:
    print("失败模块:")
    for k, v in results.items():
        if not v:
            print(f"  - {k}")
else:
    print("所有模块导入正常!")
