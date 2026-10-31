#!/usr/bin/env python3
"""把历史 Graphiti/Kuzu 图谱一次性导出成开放 JSON，供 Go 侧导入。

背景：项目已把长期记忆换成内置 Go 实现（internal/memory/native.go，
表为 mem_episode / mem_edge），Python 服务与 Kuzu 图谱只作为历史数据存档。
Kuzu 是私有文件格式，删掉 Python 环境后就再也读不出来，所以先用这个脚本
把内容落成 JSON（纯文本、无依赖），之后再删除 memory_service/ 与 data/graphiti/。

用法：
    .venv-memory/Scripts/python.exe scripts/export-graphiti-kuzu.py \
        --db data/graphiti/graphiti.kuzu \
        --out data/graphiti-export.json

只读打开数据库，不会修改原图谱。
"""

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path


def rows(conn, query):
    result = conn.execute(query)
    out = []
    while result.has_next():
        out.append(result.get_next())
    return out


def to_iso(value):
    if value is None:
        return None
    if isinstance(value, datetime):
        if value.tzinfo is None:
            value = value.replace(tzinfo=timezone.utc)
        return value.astimezone(timezone.utc).isoformat()
    return str(value)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--db", required=True, help="Kuzu 数据库目录")
    parser.add_argument("--out", required=True, help="输出 JSON 路径")
    args = parser.parse_args()

    import kuzu

    db_path = Path(args.db)
    if not db_path.exists():
        print(f"找不到 Kuzu 数据库：{db_path}", file=sys.stderr)
        return 1

    db = kuzu.Database(str(db_path), read_only=True)
    conn = kuzu.Connection(db)

    episodes = []
    for (uuid, name, group_id, created_at, source, source_desc, content, valid_at) in rows(
        conn,
        "MATCH (e:Episodic) RETURN e.uuid, e.name, e.group_id, e.created_at, "
        "e.source, e.source_description, e.content, e.valid_at",
    ):
        episodes.append({
            "uuid": uuid, "group_id": group_id, "name": name,
            "content": content, "source": source, "source_description": source_desc,
            "created_at": to_iso(created_at), "valid_at": to_iso(valid_at),
        })

    entities = []
    for (uuid, name, group_id, labels, created_at, summary, attributes) in rows(
        conn,
        "MATCH (n:Entity) RETURN n.uuid, n.name, n.group_id, n.labels, "
        "n.created_at, n.summary, n.attributes",
    ):
        entities.append({
            "uuid": uuid, "group_id": group_id, "name": name,
            "labels": list(labels or []), "summary": summary,
            "attributes": attributes, "created_at": to_iso(created_at),
        })

    # 事实保存在「边当节点」的 RelatesToNode_ 上，src/dst 通过 RELATES_TO 还原。
    facts = []
    for (fact_uuid, group_id, created_at, fact, episodes_ref, expired_at,
         valid_at, invalid_at, reference_time, attributes,
         src_uuid, src_name, dst_uuid, dst_name) in rows(
        conn,
        "MATCH (a:Entity)-[:RELATES_TO]->(n:RelatesToNode_)-[:RELATES_TO]->(b:Entity) "
        "RETURN n.uuid, n.group_id, n.created_at, n.fact, n.episodes, n.expired_at, "
        "n.valid_at, n.invalid_at, n.reference_time, n.attributes, "
        "a.uuid, a.name, b.uuid, b.name",
    ):
        facts.append({
            "uuid": fact_uuid, "group_id": group_id,
            "src_uuid": src_uuid, "src_name": src_name,
            "dst_uuid": dst_uuid, "dst_name": dst_name,
            "fact": fact, "episodes": list(episodes_ref or []),
            "created_at": to_iso(created_at), "reference_time": to_iso(reference_time),
            "valid_at": to_iso(valid_at), "invalid_at": to_iso(invalid_at),
            "expired_at": to_iso(expired_at), "attributes": attributes,
        })

    payload = {
        "exported_at": datetime.now(timezone.utc).isoformat(),
        "source": "graphiti/kuzu (历史数据，已由 internal/memory/native.go 取代)",
        "counts": {
            "episodes": len(episodes), "entities": len(entities), "facts": len(facts),
        },
        "episodes": episodes, "entities": entities, "facts": facts,
    }

    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"导出完成：{out} episodes={len(episodes)} entities={len(entities)} facts={len(facts)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
