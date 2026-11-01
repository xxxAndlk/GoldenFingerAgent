'use strict';

// 只有明确询问工作/今天的进展时才启动轨迹总结子代理，避免每轮主对话都背上下文。
const SUMMARY_INTENT = /(?:工作轨迹|工作总结|工作内容|今天|今日|最近).{0,20}(?:做了什么|干了什么|做了哪些|干了哪些|做了|干了|做事|完成|进展|总结|忙什么)|(?:做了什么|干了什么|做了哪些|干了哪些).{0,20}(?:今天|今日|工作)|总结一下.{0,12}(?:今天|今日|工作)/i;

function wantsActivitySummary(text) {
  return SUMMARY_INTENT.test(String(text || '').trim());
}

module.exports = { wantsActivitySummary };
