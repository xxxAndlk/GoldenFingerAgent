// Package stub provides dev/mock implementations of the extsvc interfaces.
package stub

import (
	"context"
	"log"
	"strings"
	"time"

	"goldenfinger/agent/internal/extsvc"
)

// Weather returns canned fixture data keyed by city (F7 dev stub).
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

// ASR returns a configured canned transcript (dev stub).
type ASR struct {
	Canned string
}

func (a ASR) Transcribe(ctx context.Context, audio []byte, format string) (string, error) {
	if a.Canned != "" {
		return a.Canned, nil
	}
	return "（语音识别 stub：这是一段模拟的转写文本）", nil
}

// TTS returns an empty WAV placeholder plus the input text (dev stub).
type TTS struct{}

func (TTS) Synth(ctx context.Context, text, voice string) ([]byte, string, error) {
	return []byte("RIFF....WAVEstub"), "wav", nil
}

// Push logs to stdout (dev stub).
type Push struct{}

func (Push) Push(ctx context.Context, deviceToken string, msg extsvc.PushMessage) error {
	log.Printf("[push stub] to=%s level=%d %s: %s", deviceToken, msg.Level, msg.Title, msg.Body)
	return nil
}

// SMS logs to stdout (dev stub).
type SMS struct{}

func (SMS) Send(ctx context.Context, phone, text string) error {
	log.Printf("[sms stub] to=%s %s", phone, strings.TrimSpace(text))
	return nil
}

// Dispatcher fans a reminder out over its channel (app/push/sms).
// The "app" channel is delivered via the chat/outbox callback in production;
// here it logs so tests can observe it.
type Dispatcher struct {
	Push extsvc.PushService
	SMS  extsvc.SMSService
	TTS  extsvc.TTSService
	// Outbox receives "app"-channel messages (wired to the web outbox later).
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
