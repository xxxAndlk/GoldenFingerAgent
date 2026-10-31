package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// 安全闸失败原因。调用方必须把它们转成人话返回给用户，不能静默重试。
var (
	// ErrEmergency 表示用户刚刚按了急停。
	ErrEmergency = errors.New("device: 急停中")
	// ErrDeclined 表示敏感操作未获人工确认。
	ErrDeclined = errors.New("device: 未获人工确认")
	// ErrConfirmUnavailable 表示无法向用户发起确认（设备不支持/超时）。
	ErrConfirmUnavailable = errors.New("device: 无法发起人工确认")
)

// GuardConfig 定义安全闸策略。默认值是产品红线，改动需同步文档与验收。
type GuardConfig struct {
	// SensitiveKeywords 命中即要求人工确认（付款/转账/删除/下单类）。
	SensitiveKeywords []string
	// SensitivePackages 是支付/银行类包名，命中即要求确认。
	SensitivePackages []string
	// EmergencyWindow 是急停后拒绝一切动作的时长。
	EmergencyWindow time.Duration
	// ConfirmTimeout 是等待用户点“继续/取消”的上限；超时按拒绝处理。
	ConfirmTimeout time.Duration
	Now            func() time.Time
}

// DefaultGuardConfig 返回默认红线策略。
func DefaultGuardConfig() GuardConfig {
	return GuardConfig{
		SensitiveKeywords: []string{"支付", "付款", "转账", "确认转账", "确认支付", "输入密码", "删除", "解绑", "下单", "立即购买"},
		SensitivePackages: []string{
			"com.tencent.mm", "com.eg.android.AlipayGphone", "com.unionpay", "com.tencent.mobileqq",
			"com.icbc", "com.ccb", "com.abchina", "com.cmbchina", "com.bankcomm",
		},
		EmergencyWindow: 10 * time.Second,
		ConfirmTimeout:  30 * time.Second,
	}
}

func (c GuardConfig) withDefaults() GuardConfig {
	def := DefaultGuardConfig()
	if len(c.SensitiveKeywords) == 0 {
		c.SensitiveKeywords = def.SensitiveKeywords
	}
	if len(c.SensitivePackages) == 0 {
		c.SensitivePackages = def.SensitivePackages
	}
	if c.EmergencyWindow <= 0 {
		c.EmergencyWindow = def.EmergencyWindow
	}
	if c.ConfirmTimeout <= 0 {
		c.ConfirmTimeout = def.ConfirmTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Guard 是设备操作的业务侧安全闸：敏感判定、人工确认与急停窗口都在 Go 里，
// 安卓侧只负责“发起确认的 UI”和“本地急停计时”这两个能力原语。
type Guard struct {
	cfg GuardConfig

	mu             sync.Mutex
	emergencyUntil time.Time
}

// NewGuard 创建安全闸。
func NewGuard(cfg GuardConfig) *Guard { return &Guard{cfg: cfg.withDefaults()} }

// MarkEmergency 记录一次急停；窗口期内所有动作被拒绝。
func (g *Guard) MarkEmergency() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.emergencyUntil = g.cfg.Now().Add(g.cfg.EmergencyWindow)
}

// InEmergency 报告当前是否处于急停窗口。
func (g *Guard) InEmergency() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.Now().Before(g.emergencyUntil)
}

// mutatingAction 判断动作是否会改变设备状态；只读观察不需要确认。
func mutatingAction(action string) bool {
	switch action {
	// 同时接受工具名与线上帧里的动作名：设备协议使用 camelCase
	// （click/longClick/gestureTap/setText/gestureSwipe/scrollForward/
	// scrollBackward/globalBack/globalHome/openApp），工具层另有一套名字。
	case "tap", "click", "longClick", "long_click",
		"input_text", "setText", "set_text",
		"swipe", "gestureTap", "gestureSwipe",
		"scrollForward", "scrollBackward", "scroll",
		"back", "globalBack", "globalHome",
		"open_app", "openApp":
		return true
	default:
		// screen_observe / screenshot / wait / confirm 都是只读或确认本身。
		return false
	}
}

// Verdict 是纯判定结果：是否需要人工确认、给用户的原因。
// 它不发起任何网络请求，便于单测与复用。
func (g *Guard) Verdict(screen *Screen, action string, params map[string]any) (needConfirm bool, reason string) {
	if !mutatingAction(action) {
		return false, ""
	}
	if screen != nil {
		if pkg := strings.TrimSpace(screen.Package); pkg != "" {
			for _, sensitive := range g.cfg.SensitivePackages {
				if strings.EqualFold(pkg, sensitive) {
					return true, fmt.Sprintf("当前页面属于支付/银行类应用（%s）", pkg)
				}
			}
		}
		if hit := g.matchKeyword(screenText(screen)); hit != "" {
			return true, fmt.Sprintf("当前页面出现敏感内容「%s」", hit)
		}
	}
	if hit := g.matchKeyword(paramsText(params)); hit != "" {
		return true, fmt.Sprintf("操作内容包含敏感词「%s」", hit)
	}
	return false, ""
}

func (g *Guard) matchKeyword(text string) string {
	if text == "" {
		return ""
	}
	for _, kw := range g.cfg.SensitiveKeywords {
		if kw != "" && strings.Contains(text, kw) {
			return kw
		}
	}
	return ""
}

func screenText(screen *Screen) string {
	var b strings.Builder
	for _, n := range screen.Nodes {
		b.WriteString(n.Text)
		b.WriteByte(' ')
		b.WriteString(n.ContentDescription)
		b.WriteByte(' ')
	}
	return b.String()
}

// paramsText 只取字符串型参数，避免把坐标数字当成内容判断。
func paramsText(params map[string]any) string {
	var b strings.Builder
	for _, v := range params {
		switch typed := v.(type) {
		case string:
			b.WriteString(typed)
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// Check 在真正下发动作前执行闸门：
//  1. 急停窗口内直接拒绝；
//  2. 敏感页面/内容要求设备弹出人工确认，用户点“继续”才放行；
//  3. 无法确认（设备不支持、超时、断线）一律拒绝，安全默认不放行。
func (g *Guard) Check(ctx context.Context, hub *Hub, action string, params map[string]any) error {
	if g == nil {
		return nil
	}
	if g.InEmergency() {
		return ErrEmergency
	}
	var screen *Screen
	if hub != nil {
		screen = hub.LatestScreen()
	}
	needConfirm, reason := g.Verdict(screen, action, params)
	if !needConfirm {
		return nil
	}
	if hub == nil {
		return fmt.Errorf("%w：%s", ErrConfirmUnavailable, reason)
	}
	ctx, cancel := context.WithTimeout(ctx, g.cfg.ConfirmTimeout)
	defer cancel()
	res, err := hub.Send(ctx, "confirm", map[string]any{
		"message": reason + "，是否继续？",
		"action":  action,
	})
	if err != nil {
		return fmt.Errorf("%w：%s（%v）", ErrConfirmUnavailable, reason, err)
	}
	if res == nil || !res.OK {
		return fmt.Errorf("%w：%s", ErrDeclined, reason)
	}
	// 设备可以在 data.approved=false 里明确表达“用户取消”。
	var payload struct {
		Approved *bool `json:"approved"`
	}
	if DecodeResultData(res.Data, &payload) == nil && payload.Approved != nil && !*payload.Approved {
		return fmt.Errorf("%w：%s", ErrDeclined, reason)
	}
	return nil
}

// DecodeResultData 解析设备回传的 data。
// 安卓侧把 data 作为 JSON 字符串放在 result 帧里（双重编码），
// 因此这里同时接受对象与字符串两种形态。
func DecodeResultData(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return errors.New("device: 空的 result data")
	}
	if err := json.Unmarshal(raw, out); err == nil {
		return nil
	}
	var inner string
	if err := json.Unmarshal(raw, &inner); err != nil {
		return err
	}
	if strings.TrimSpace(inner) == "" {
		return errors.New("device: 空的 result data")
	}
	return json.Unmarshal([]byte(inner), out)
}

// HumanMessage 把闸门错误转成给用户的话术（工具层直接返回它）。
func HumanMessage(err error) string {
	switch {
	case errors.Is(err, ErrEmergency):
		return "刚刚急停过，10 秒内不会再自动操作。需要的话稍等一下再让我继续。"
	case errors.Is(err, ErrDeclined):
		return "这一步需要您自己确认或自己点，我先停手。"
	case errors.Is(err, ErrConfirmUnavailable):
		return "这一步是敏感操作（付款/转账/删除/下单类），没能得到您的确认，所以我没有执行。"
	default:
		return "操作被安全策略拦下了：" + err.Error()
	}
}
