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

// ToolServices bundles the domain services shared by tool executors.
// (No business rules here — rules live in the domain packages.)
type ToolServices struct {
	Memory  *memory.Service
	Tasks   *task.Service
	Intents *intent.Service
	Weather extsvc.WeatherService
	Guard   *compliance.Guard
	Repos   *store.Repos
	Now     func() time.Time
	LocOf   func(tz string) *time.Location
	Th      nlu.Thresholds
}
