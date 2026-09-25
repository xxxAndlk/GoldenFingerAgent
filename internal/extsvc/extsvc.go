// Package extsvc: external capability interfaces (weather/ASR/TTS/push/SMS).
// Everything here is 外接 — production wires cloud services (讯飞/阿里云/和风…);
// dev uses the stub package. The agent core only ever sees these interfaces.
package extsvc

import (
	"context"
	"time"
)

// Weather is one day/time weather reading (F7).
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

// WeatherService wraps a weather API (和风/高德).
type WeatherService interface {
	Now(ctx context.Context, city string) (*Weather, error)
	Forecast(ctx context.Context, city string, days int) ([]DailyWeather, error)
}

// ASRService converts audio to text (讯飞/阿里云语音).
type ASRService interface {
	Transcribe(ctx context.Context, audio []byte, format string) (string, error)
}

// TTSService converts text to speech audio.
type TTSService interface {
	Synth(ctx context.Context, text, voice string) (audio []byte, format string, err error)
}

// PushMessage is a push notification payload.
type PushMessage struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Level int    `json:"level"`
}

// PushService delivers via vendor channels (个推/极光/微信服务通知).
type PushService interface {
	Push(ctx context.Context, deviceToken string, msg PushMessage) error
}

// SMSService delivers high-severity text/voice messages (阿里云/腾讯云通信).
type SMSService interface {
	Send(ctx context.Context, phone, text string) error
}
