package openai

import "strings"

// EffortLevels 是允许的推理强度取值，与 DeepSeek 的 low|high|max 三档对齐。
var EffortLevels = []string{"low", "high", "max"}

// normalizeEffort 把界面上的输入归一化成协议允许的值；
// 空值或无法识别时返回空串，表示不显式声明、交给服务端默认。
func normalizeEffort(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "low", "minimal", "fast", "低":
		return "low"
	case "high", "deep", "高":
		return "high"
	case "max", "最高":
		return "max"
	case "medium", "mid", "balanced", "中":
		// 旧配置里的 medium 归到 high：界面已收敛为 low/high/max 三档。
		return "high"
	default:
		return ""
	}
}
