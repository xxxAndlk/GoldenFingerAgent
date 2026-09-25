package llm

// ModelInfo describes one known model in the catalog.
type ModelInfo struct {
	ID         string
	Provider   string // "deepseek" | "moonshot" | "dashscope" | "doubao" | ...
	ContextWin int
	EmbedDim   int // 0 for chat models
}

// catalog holds reference data for models commonly used with the
// OpenAI-compatible gateway (DeepSeek / Moonshot / 通义 / 豆包).
var catalog = map[string]ModelInfo{
	"deepseek-chat":          {ID: "deepseek-chat", Provider: "deepseek", ContextWin: 65536},
	"moonshot-v1-8k":         {ID: "moonshot-v1-8k", Provider: "moonshot", ContextWin: 8192},
	"qwen-plus":              {ID: "qwen-plus", Provider: "dashscope", ContextWin: 131072},
	"doubao-1-5-pro-32k":     {ID: "doubao-1-5-pro-32k", Provider: "doubao", ContextWin: 32768},
	"BAAI/bge-large-zh-v1.5": {ID: "BAAI/bge-large-zh-v1.5", Provider: "siliconflow", EmbedDim: 1024},
	"BAAI/bge-m3":            {ID: "BAAI/bge-m3", Provider: "siliconflow", EmbedDim: 1024},
}

// Lookup returns catalog info for a model id (zero value when unknown).
func Lookup(model string) ModelInfo { return catalog[model] }
