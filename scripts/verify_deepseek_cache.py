import json
import sys
import urllib.request


def chat(base_url, api_key, model, messages):
    instructions = "\n".join(item["content"] for item in messages if item["role"] == "system")
    input_items = []
    for item in messages:
        if item["role"] == "system":
            continue
        content_type = "output_text" if item["role"] == "assistant" else "input_text"
        input_items.append({
            "type": "message",
            "role": item["role"],
            "content": [{"type": content_type, "text": item["content"]}],
        })
    request = urllib.request.Request(
        base_url.rstrip("/") + "/responses",
        data=json.dumps({
            "model": model,
            "instructions": instructions,
            "input": input_items,
            "temperature": 0,
            "max_output_tokens": 48,
        }).encode("utf-8"),
        headers={
            "Authorization": "Bearer " + api_key,
            "Content-Type": "application/json",
        },
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=120) as response:
        return json.load(response)


def output_text(response):
    return "".join(
        part.get("text", "")
        for item in response.get("output", [])
        if item.get("type") == "message"
        for part in item.get("content", [])
        if part.get("type") == "output_text"
    )


def main():
    with open(sys.argv[1], "r", encoding="utf-8") as file:
        config = json.load(file)["llm"]
    stable_prefix = (
        "你是缓存验证助手。以下规则在整个验证期间保持不变，不得改写或总结。"
        "回答应简洁、准确，仅处理当前最后一个用户问题。"
        "历史消息必须逐字保留，不能把时间、状态或其他动态内容插入系统消息。"
        "这是一段用于达到缓存前缀长度要求的固定文本，不包含任何私人信息。"
    ) * 12
    first_messages = [
        {"role": "system", "content": stable_prefix},
        {"role": "user", "content": "这是隔离的缓存验证第一轮，只回复：基线完成。"},
    ]
    first = chat(config["base_url"], config["api_key"], config["model"], first_messages)
    print("ROUND1 " + json.dumps(first.get("usage", {}), ensure_ascii=False))
    assistant_text = output_text(first) or "基线完成。"
    second_messages = first_messages + [
        {"role": "assistant", "content": assistant_text},
        {"role": "user", "content": "这是隔离的缓存验证第二轮，只回复：复用完成。"},
    ]
    second = chat(config["base_url"], config["api_key"], config["model"], second_messages)
    usage = second.get("usage", {})
    print("ROUND2 " + json.dumps(usage, ensure_ascii=False))
    details = usage.get("input_tokens_details")
    if details is None or "cached_tokens" not in details:
        raise SystemExit("CACHE_FIELDS_MISSING")
    hit = details["cached_tokens"]
    miss = usage.get("input_tokens", 0) - hit
    rate = hit / (hit + miss) if hit + miss else 0
    print(f"CACHE hit={hit} miss={miss} hit_rate={rate:.2%}")


if __name__ == "__main__":
    main()
