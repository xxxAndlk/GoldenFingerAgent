package llm

// ModelInfo 描述目录中的一个已知模型。
type ModelInfo struct {
	ID         string
	Provider   string // "deepseek" | "moonshot" | "dashscope" | "doubao" | ...
	ContextWin int
	EmbedDim   int // 0 for chat models
}

// catalog 保存常用模型的参考数据，配合
// OpenAI 兼容网关（DeepSeek / Moonshot / 通义 / 豆包）使用。
var catalog = map[string]ModelInfo{
	"deepseek-chat":          {ID: "deepseek-chat", Provider: "deepseek", ContextWin: 65536},
	"moonshot-v1-8k":         {ID: "moonshot-v1-8k", Provider: "moonshot", ContextWin: 8192},
	"qwen-plus":              {ID: "qwen-plus", Provider: "dashscope", ContextWin: 131072},
	"doubao-1-5-pro-32k":     {ID: "doubao-1-5-pro-32k", Provider: "doubao", ContextWin: 32768},
	"BAAI/bge-large-zh-v1.5": {ID: "BAAI/bge-large-zh-v1.5", Provider: "siliconflow", EmbedDim: 1024},
	"BAAI/bge-m3":            {ID: "BAAI/bge-m3", Provider: "siliconflow", EmbedDim: 1024},
}

// Lookup 返回指定模型 id 的目录信息（未知时返回零值）。
func Lookup(model string) ModelInfo { return catalog[model] }
