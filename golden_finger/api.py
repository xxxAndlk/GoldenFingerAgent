"""金手指 Agent 系统 — FastAPI 后端

提供 REST API + SSE 流式推送，供 Vue 前端消费。
"""

import json
import os
from pathlib import Path

from fastapi import FastAPI, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import StreamingResponse
from fastapi.staticfiles import StaticFiles

from .harness import GoldenFingerHarness
from .config import config

app = FastAPI(title="Golden Finger Agent", version="0.1.0")

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)

# 全局 Harness 实例（单例）
_harness: GoldenFingerHarness | None = None


def get_harness() -> GoldenFingerHarness:
    global _harness
    if _harness is None:
        _harness = GoldenFingerHarness()
    return _harness


@app.get("/api/health")
async def health():
    return {"status": "ok"}


@app.get("/api/status")
async def status():
    h = get_harness()
    return h.get_status()


@app.post("/api/query")
async def query(request: Request):
    """提交问题，SSE 流式返回五域流水线事件"""
    body = await request.json()
    query_text = body.get("query", "").strip()
    if not query_text:
        return StreamingResponse(
            iter(["data: {\"error\": \"查询不能为空\"}\n\n"]),
            media_type="text/event-stream",
        )

    h = get_harness()

    async def event_stream():
        try:
            async for event in h.run_query_stream(query_text):
                domain = event.get("domain", "")
                status = event.get("status", "")
                event_type = f"{domain}_{status}"

                # 构建 SSE 数据
                data = {"domain": domain, "status": status}

                if domain == "analysis" and status == "completed":
                    plan = event.get("plan")
                    if plan:
                        data["plan"] = {
                            "plan_id": plan.plan_id,
                            "complexity": plan.complexity.value,
                            "tasks": [
                                {
                                    "task_id": t.task_id,
                                    "description": t.description,
                                    "matched_skill": t.matched_skill,
                                    "depends_on": t.depends_on,
                                }
                                for t in plan.tasks
                            ],
                            "execution_order": plan.execution_order,
                        }

                elif domain == "execution":
                    if status == "tool_call":
                        data["tool_event"] = event["event"]
                    elif status == "completed":
                        report = event.get("report")
                        if report:
                            data["total_duration_ms"] = report.total_duration_ms
                            data["anomalies"] = report.anomalies

                elif domain == "verification" and status == "completed":
                    ver = event.get("verification")
                    if ver:
                        data["overall_pass"] = ver.overall_pass
                        data["action"] = ver.action
                        data["failed_checks"] = [
                            {"name": c.check_name, "detail": c.detail}
                            for c in ver.structure_checks + ver.content_checks + ver.replay_checks
                            if not c.passed
                        ]

                elif domain == "complete":
                    # 获取最终回复
                    try:
                        result = await h.run_query(query_text)
                        if not result.get("error") and result.get("execution"):
                            data["final_text"] = h.get_final_response(result["execution"])
                    except Exception:
                        pass

                elif domain == "error" or status == "error":
                    data["error"] = event.get("error", "")

                yield f"event: {event_type}\ndata: {json.dumps(data, ensure_ascii=False)}\n\n"

            yield "event: done\ndata: {}\n\n"

        except Exception as e:
            yield f"event: error\ndata: {{\"error\": \"{str(e)}\"}}\n\n"

    return StreamingResponse(
        event_stream(),
        media_type="text/event-stream",
        headers={
            "Cache-Control": "no-cache",
            "Connection": "keep-alive",
            "X-Accel-Buffering": "no",
        },
    )


def setup_static(app: FastAPI, static_dir: str):
    """挂载 Vue 前端静态文件"""
    if os.path.isdir(static_dir):
        app.mount("/", StaticFiles(directory=static_dir, html=True), name="static")
