// Package extsvc: 外部能力接口（天气/ASR/TTS/推送/SMS）。
// 这里的一切都是外接——生产环境接入云端服务（讯飞/阿里云/和风…）；
// 开发环境使用 stub 包。代理核心只看到这些接口。
package extsvc

import (
	"context"
	"time"
)

// Weather 是某天/某时的天气读数（F7）。
type Weather struct {
	City      string    `json:"city"`
	Text      string    `json:"text"` // "多云"
	TempC     float64   `json:"temp_c"`
	Humidity  float64   `json:"humidity"`
	Wind      string    `json:"wind"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DailyWeather struct {
	Date     string  `json:"date"` // YYYY-MM-DD
	TextDay  string  `json:"text_day"`
	TempMaxC float64 `json:"temp_max_c"`
	TempMinC float64 `json:"temp_min_c"`
}

// WeatherService 封装天气 API（和风/高德）。
type WeatherService interface {
	Now(ctx context.Context, city string) (*Weather, error)
	Forecast(ctx context.Context, city string, days int) ([]DailyWeather, error)
}

// SearchResult 是一次网页搜索的命中（Bocha Web Search API）。
type SearchResult struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	Snippet       string `json:"snippet"`
	Summary       string `json:"summary"`
	SiteName      string `json:"siteName"`
	DatePublished string `json:"datePublished"`
}

// SearchService 封装网页搜索 API（博查 Bocha Web Search）。
type SearchService interface {
	Search(ctx context.Context, query string, count int) ([]SearchResult, error)
}

// ASRService 把音频转成文本（讯飞/阿里云语音）。
type ASRService interface {
	Transcribe(ctx context.Context, audio []byte, format string) (string, error)
}

// TTSService 把文本转成语音音频。
type TTSService interface {
	Synth(ctx context.Context, text, voice string) (audio []byte, format string, err error)
}

// PushMessage 是推送通知的载荷。
type PushMessage struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Level int    `json:"level"`
}

// PushService 通过厂商渠道投递（个推/极光/微信服务通知）。
type PushService interface {
	Push(ctx context.Context, deviceToken string, msg PushMessage) error
}

// SMSService 投递高严重度的文本/语音消息（阿里云/腾讯云通信）。
type SMSService interface {
	Send(ctx context.Context, phone, text string) error
}
