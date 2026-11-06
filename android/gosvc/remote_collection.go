package gosvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"goldenfinger/agent/internal/collection"
	"goldenfinger/agent/internal/relay"
)

// remoteCollection 把手机端的采集渠道工具接到电脑的真实采集管理器上。
//
// 手机不自己执行采集，也不在本机保存渠道配置：
// 它只发一条经过授权的中继命令，等电脑回执。
type remoteCollection struct {
	phone *relay.Phone
}

func (r *remoteCollection) ListCollectionChannels(ctx context.Context, ownerID string) (map[string]any, error) {
	if r == nil || r.phone == nil {
		return nil, errors.New("手机远程采集未启用")
	}
	result, err := r.phone.CollectionList(ctx)
	if err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, errors.New(firstNonEmptyString(result.Error, "电脑没有返回采集渠道状态"))
	}
	var body struct {
		Channels []map[string]any `json:"channels"`
		Detail   map[string]any   `json:"detail"`
	}
	if len(result.Data) > 0 {
		if err := json.Unmarshal(result.Data, &body); err != nil {
			return nil, fmt.Errorf("解析电脑端采集渠道失败: %w", err)
		}
	}
	if len(body.Detail) > 0 {
		return body.Detail, nil
	}
	// 没有详细视图时退回三态列表，手机也能看出每个渠道在哪台设备生效。
	return map[string]any{"channels": body.Channels}, nil
}

func (r *remoteCollection) ConfigureCollectionChannel(ctx context.Context, ownerID string, input collection.ConfigureInput) (map[string]any, string, error) {
	if r == nil || r.phone == nil {
		return nil, "", errors.New("手机远程采集未启用")
	}
	result, err := r.phone.CollectionConfigureInput(ctx, "", relay.CollectionInput{
		Channel:         input.Channel,
		Action:          input.Action,
		Enabled:         input.Action == "enable",
		Query:           input.Query,
		SourceID:        input.SourceID,
		URL:             input.URL,
		SiteID:          input.SiteID,
		Scope:           input.Scope,
		IntervalSeconds: input.IntervalSeconds,
	})
	if err != nil {
		return nil, "", err
	}
	if !result.OK {
		return nil, "", errors.New(firstNonEmptyString(result.Error, "电脑拒绝了这次采集配置"))
	}
	var body struct {
		Applied  bool           `json:"applied"`
		Data     map[string]any `json:"data"`
		Question string         `json:"question"`
	}
	if len(result.Data) > 0 {
		if err := json.Unmarshal(result.Data, &body); err != nil {
			return nil, "", fmt.Errorf("解析电脑端采集配置结果失败: %w", err)
		}
	}
	return body.Data, body.Question, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
