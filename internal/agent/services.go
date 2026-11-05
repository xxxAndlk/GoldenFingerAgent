package agent

import (
	"context"
	"time"

	"goldenfinger/agent/internal/compliance"
	"goldenfinger/agent/internal/device"
	"goldenfinger/agent/internal/extsvc"
	"goldenfinger/agent/internal/intent"
	"goldenfinger/agent/internal/memory"
	"goldenfinger/agent/internal/nlu"
	"goldenfinger/agent/internal/store"
	"goldenfinger/agent/internal/task"
	"goldenfinger/agent/internal/worktask"
)

// ToolServices 打包工具执行器共享的领域服务。
// （这里不含业务规则——规则在领域包中。）
type ToolServices struct {
	Memory    *memory.Service
	Tasks     *task.Service
	Intents   *intent.Service
	Weather   extsvc.WeatherService
	Search    extsvc.SearchService
	Fetch     extsvc.FetchService
	Workspace string
	Guard     *compliance.Guard
	Repos     *store.Repos
	Now       func() time.Time
	LocOf     func(tz string) *time.Location
	Th        nlu.Thresholds
	// Device 是设备操控通道；nil 表示未装配（设备工具返回"设备未连接"）。
	Device *device.Hub
	// DeviceGuard 是设备操作安全闸（敏感确认/急停）；nil 时退化为无闸门，
	// 仅用于测试，生产装配必须提供。
	DeviceGuard *device.Guard
	// Artifacts 是 WorkTask 交付物服务；nil 时 create_artifact 不可用。
	Artifacts *worktask.ArtifactService
	// Delivery 是跨端文件交付队列；nil 时 deliver_artifact 不可用。
	Delivery DeliveryQueue
}

// DeliveryQueue 把 Artifact 版本加入持久上传队列（*deliveryclient.Worker 满足）。
type DeliveryQueue interface {
	Enqueue(ctx context.Context, ownerID, artifactID string, version int,
		targetDeviceID string, allowExecutable bool) (string, error)
}
