"""金手指 Agent 系统 — FastAPI 后端

提供 REST API + SSE 流式推送，供 Vue 前端消费。
"""

import asyncio
import json
import os
import queue
import time
from pathlib import Path

from fastapi import FastAPI, Request, Query
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import StreamingResponse
from fastapi.staticfiles import StaticFiles

from .harness import GoldenFingerHarness
from .config import config
from .logging import log_manager, log_system, log_api_request, log_api_response, is_shutdown_sentinel

app = FastAPI(title="Golden Finger Agent", version="0.1.0")

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)

# 全局 Harness 实例（单例）
_harness: GoldenFingerHarness | None = None


# ---- HTTP 请求/响应日志中间件 ----

@app.middleware("http")
async def log_http_middleware(request: Request, call_next):
    """记录所有 HTTP 请求和响应"""
    t0 = time.monotonic()
    method = request.method
    path = request.url.path

    # 跳过日志端点自身，避免无限循环
    skip_paths = ("/api/logs",)

    if path not in skip_paths:
        body_preview = ""
        if method == "POST" and "application/json" in request.headers.get("content-type", ""):
            try:
                raw = await request.body()
                body_preview = raw.decode("utf-8", errors="replace")[:300]
                # 重新构造 request 以便后续 handler 可以读取 body
                async def receive():
                    return {"type": "http.request", "body": raw}
                request._receive = receive
            except Exception:
                body_preview = ""

        log_api_request(f"HTTP {method} {path}",
                         method=method, path=path,
                         query=str(request.query_params) if request.query_params else None,
                         body=body_preview if body_preview else None)

    response = await call_next(request)
    elapsed_ms = int((time.monotonic() - t0) * 1000)

    if path not in skip_paths:
        log_api_response(f"HTTP {method} {path} → {response.status_code}",
                          method=method, path=path,
                          status_code=response.status_code,
                          duration_ms=elapsed_ms)

    return response


def get_harness() -> GoldenFingerHarness:
    global _harness
    if _harness is None:
        _harness = GoldenFingerHarness()
        log_system("API 服务 Harness 初始化完成",
                    llm_provider=config.llm_provider,
                    data_dir=str(config.data_dir))
    return _harness


@app.get("/api/health")
async def health():
    return {"status": "ok"}


@app.get("/api/status")
async def status():
    h = get_harness()
    return h.get_status()


@app.get("/api/logs")
async def query_logs(
    category: str | None = Query(None, description="分类过滤，逗号分隔: system,step,api_request,api_response"),
    level: str | None = Query(None, description="日志级别: DEBUG,INFO,WARNING,ERROR"),
    q: str | None = Query(None, description="关键词搜索"),
    limit: int = Query(200, ge=1, le=1000),
    offset: int = Query(0, ge=0),
):
    """查询历史日志（分页+过滤）"""
    categories = [c.strip() for c in category.split(",")] if category else None
    entries, total = log_manager.query(
        categories=categories, level=level, q=q, limit=limit, offset=offset
    )
    return {"total": total, "limit": limit, "offset": offset, "entries": entries}


@app.get("/api/logs/stream")
async def stream_logs(
    category: str | None = Query(None, description="分类过滤，逗号分隔"),
):
    """SSE 实时日志推送"""
    categories = [c.strip() for c in category.split(",")] if category else None

    async def event_stream():
        q = log_manager.subscribe()
        try:
            # 先发送当前缓冲区的最近 50 条日志
            entries, _ = log_manager.query(categories=categories, limit=50, offset=0)
            for entry in entries:
                yield f"data: {json.dumps(entry, ensure_ascii=False)}\n\n"

            # 然后从线程安全队列中轮询新日志
            loop = asyncio.get_event_loop()
            heartbeat_count = 0
            while True:
                try:
                    entry = await asyncio.wait_for(
                        loop.run_in_executor(None, lambda: q.get(timeout=1.0)),
                        timeout=30,
                    )
                    if is_shutdown_sentinel(entry):
                        break
                    if categories is None or entry.category in categories:
                        yield f"data: {json.dumps(entry.to_dict(), ensure_ascii=False)}\n\n"
                    heartbeat_count = 0
                except (asyncio.TimeoutError, queue.Empty):
                    heartbeat_count += 1
                    if heartbeat_count % 5 == 0:
                        yield ": heartbeat\n\n"
        finally:
            log_manager.unsubscribe(q)

    return StreamingResponse(
        event_stream(),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "Connection": "keep-alive"},
    )


@app.get("/api/logs/stats")
async def get_logs_stats():
    """日志统计概览"""
    return log_manager.stats()


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
    log_system(f"收到查询请求: {query_text[:80]}", query=query_text[:200])

    # 缓存执行报告，避免在 complete 阶段重复执行流水线
    execution_report = None

    async def event_stream():
        nonlocal execution_report
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
                            execution_report = report
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
                    # 从缓存的执行报告提取最终回复，不再重复执行流水线
                    if execution_report:
                        try:
                            data["final_text"] = h.get_final_response(execution_report)
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
