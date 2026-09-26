// Package stub 提供 extsvc 接口的开发/模拟实现。
package stub

import (
	"context"
	"log"
	"strings"
	"time"

	"goldenfinger/agent/internal/extsvc"
)

// Weather 返回按城市索引的固定夹具数据（F7 开发桩）。
type Weather struct{}

var weatherFixtures = map[string]extsvc.Weather{
	"北京": {City: "北京", Text: "晴", TempC: 22, Humidity: 35, Wind: "北风3级"},
	"上海": {City: "上海", Text: "多云", TempC: 25, Humidity: 60, Wind: "东风2级"},
	"广州": {City: "广州", Text: "小雨", TempC: 28, Humidity: 80, Wind: "南风2级"},
	"深圳": {City: "深圳", Text: "阴", TempC: 27, Humidity: 70, Wind: "东南风2级"},
}

func (Weather) Now(ctx context.Context, city string) (*extsvc.Weather, error) {
	w, ok := weatherFixtures[city]
	if !ok {
		w = extsvc.Weather{City: city, Text: "晴", TempC: 24, Humidity: 50, Wind: "微风"}
	}
	w.UpdatedAt = time.Now()
	return &w, nil
}

func (Weather) Forecast(ctx context.Context, city string, days int) ([]extsvc.DailyWeather, error) {
	if days <= 0 {
		days = 3
	}
	base := weatherFixtures[city]
	out := make([]extsvc.DailyWeather, 0, days)
	for i := 0; i < days; i++ {
		out = append(out, extsvc.DailyWeather{
			Date:     time.Now().AddDate(0, 0, i).Format("2006-01-02"),
			TextDay:  base.Text,
			TempMaxC: base.TempC + 2,
			TempMinC: base.TempC - 5,
		})
	}
	return out, nil
}

// Search 对任何查询返回固定示例结果（开发桩）。
type Search struct{}

var searchFixtures = []extsvc.SearchResult{
	{Name: "Firecrawl Search 官方文档", URL: "https://docs.firecrawl.dev", Snippet: "Firecrawl Search API，专为 AI Agent 设计的网页搜索与抓取服务。", Summary: "Firecrawl 提供 v2/search 搜索 API，返回干净的标题、链接与摘要。", SiteName: "Firecrawl", DatePublished: "2026-01-01"},
	{Name: "金手指管家示例结果", URL: "https://example.com", Snippet: "这是一条 dev stub 返回的固定搜索结果。", Summary: "未配置 firecrawl api_key 时返回的示例数据，配置后走真实搜索。", SiteName: "示例站点", DatePublished: "2026-01-02"},
}

func (Search) Search(ctx context.Context, query string, count int) ([]extsvc.SearchResult, error) {
	if count <= 0 {
		count = 8
	}
	if count > len(searchFixtures) {
		count = len(searchFixtures)
	}
	return searchFixtures[:count], nil
}

// ASR 返回配置好的固定转写文本（开发桩）。
type ASR struct {
	Canned string
}

func (a ASR) Transcribe(ctx context.Context, audio []byte, format string) (string, error) {
	if a.Canned != "" {
		return a.Canned, nil
	}
	return "（语音识别 stub：这是一段模拟的转写文本）", nil
}

// TTS 返回一个空的 WAV 占位符加上输入文本（开发桩）。
type TTS struct{}

func (TTS) Synth(ctx context.Context, text, voice string) ([]byte, string, error) {
	return []byte("RIFF....WAVEstub"), "wav", nil
}

// Push 记录到 stdout（开发桩）。
type Push struct{}

func (Push) Push(ctx context.Context, deviceToken string, msg extsvc.PushMessage) error {
	log.Printf("[push stub] to=%s level=%d %s: %s", deviceToken, msg.Level, msg.Title, msg.Body)
	return nil
}

// SMS 记录到 stdout（开发桩）。
type SMS struct{}

func (SMS) Send(ctx context.Context, phone, text string) error {
	log.Printf("[sms stub] to=%s %s", phone, strings.TrimSpace(text))
	return nil
}

// Dispatcher 把提醒按渠道（app/push/sms）分发。
// 生产环境中 "app" 渠道通过聊天/outbox 回调投递；
// 这里记录日志以便测试观察。
type Dispatcher struct {
	Push extsvc.PushService
	SMS  extsvc.SMSService
	TTS  extsvc.TTSService
	// Outbox 接收 "app" 渠道的消息（日后接到 web outbox）。
	Outbox func(body string)
}

func (d Dispatcher) Deliver(ctx context.Context, to, channel, body string) error {
	switch channel {
	case "push":
		return d.Push.Push(ctx, to, extsvc.PushMessage{Title: "管家提醒", Body: body, Level: 2})
	case "sms":
		return d.SMS.Send(ctx, to, body)
	default: // "app"
		if d.Outbox != nil {
			d.Outbox(body)
		} else {
			log.Printf("[app outbox stub] %s", body)
		}
	}
	return nil
}
