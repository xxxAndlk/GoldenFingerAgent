package agent

import (
	"time"

	"goldenfinger/agent/internal/compliance"
	"goldenfinger/agent/internal/extsvc"
	"goldenfinger/agent/internal/intent"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
)

// ToolServices 打包工具执行器共享的领域服务。
// （这里不含业务规则——规则在领域包中。）
type ToolServices struct {
	Memory  *memory.Service
	Tasks   *task.Service
	Intents *intent.Service
	Weather extsvc.WeatherService
	Search  extsvc.SearchService
	Guard   *compliance.Guard
	Repos   *store.Repos
	Now     func() time.Time
	LocOf   func(tz string) *time.Location
	Th      nlu.Thresholds
}
