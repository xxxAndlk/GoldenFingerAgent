package voice

import (
	"strings"
	"testing"
	"time"
)

// fakeClock 让指令窗口/上下文窗口可被确定性地测试。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestService() (*Service, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
	return NewService(Config{}, clock.now), clock
}

func types(actions []Action) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, a.Type)
	}
	return out
}

func TestWakeWordWithCommandCapturesAndChats(t *testing.T) {
	svc, _ := newTestService()
	actions := svc.OnText("", "小金小金放首歌")
	if got := strings.Join(types(actions), ","); got != "chat" {
		t.Fatalf("actions=%v", actions)
	}
	if actions[0].Text != "放首歌" {
		t.Fatalf("chat text=%q", actions[0].Text)
	}
	// 说出口的指令本身也算情景（与原 Kotlin 行为一致：先暂存，4s 后或下一句到来时落库）。
	flushed := svc.Flush("")
	if len(flushed) != 1 || flushed[0].Text != "放首歌" {
		t.Fatalf("flush=%v", flushed)
	}
}

func TestWakeWordAloneOpensCommandWindow(t *testing.T) {
	svc, clock := newTestService()
	actions := svc.OnText("", "小金小金")
	if len(actions) != 1 || actions[0].Type != ActionSpeak || actions[0].Text != AckText {
		t.Fatalf("actions=%v", actions)
	}

	clock.advance(5 * time.Second)
	actions = svc.OnText("", "今天几号")
	if got := strings.Join(types(actions), ","); got != "chat" {
		t.Fatalf("窗口内应作为指令: %v", actions)
	}
	svc.Flush("")

	// 窗口用掉后，同一句话不再触发提问。
	clock.advance(time.Second)
	actions = svc.OnText("", "今天几号")
	if got := strings.Join(types(actions), ""); got != "" {
		t.Fatalf("窗口应已关闭: %v", actions)
	}
}

func TestCommandWindowExpires(t *testing.T) {
	svc, clock := newTestService()
	if actions := svc.OnText("", "小金小金"); len(actions) != 1 {
		t.Fatalf("actions=%v", actions)
	}
	clock.advance(11 * time.Second)
	actions := svc.OnText("", "今天几号")
	for _, a := range actions {
		if a.Type == ActionChat {
			t.Fatalf("窗口应已过期，不应提问: %v", actions)
		}
	}
}

func TestWakeWordStripsLeadingPunctuation(t *testing.T) {
	svc, _ := newTestService()
	actions := svc.OnText("", "小金小金，给我讲个笑话")
	if len(actions) != 1 || actions[0].Type != ActionChat || actions[0].Text != "给我讲个笑话" {
		t.Fatalf("actions=%v", actions)
	}
	// 空格也要忽略（语音识别常插入空格）。
	actions = svc.OnText("", "小金 小金 明 天 天气")
	if len(actions) != 1 || actions[0].Text != "明天天气" {
		t.Fatalf("actions=%v", actions)
	}
}

func TestConsecutiveUtterancesMergeAsContext(t *testing.T) {
	svc, clock := newTestService()
	if actions := svc.OnText("", "明天要去医院"); len(actions) != 0 {
		t.Fatalf("第一句应暂存等后文: %v", actions)
	}
	clock.advance(2 * time.Second)
	// 第二句到达：第一句带着“后文”落库，第二句自己暂存。
	actions := svc.OnText("", "挂的是内科")
	if len(actions) != 1 || actions[0].Type != ActionCapture {
		t.Fatalf("actions=%v", actions)
	}
	if actions[0].Text != "明天要去医院" || actions[0].After != "挂的是内科" {
		t.Fatalf("合并结果不对: %+v", actions[0])
	}
	if !svc.Pending("") {
		t.Fatal("第二句应处于暂存状态")
	}
}

func TestFlushPersistsPending(t *testing.T) {
	svc, _ := newTestService()
	svc.OnText("", "记得买牛奶")
	if !svc.Pending("") {
		t.Fatal("应暂存")
	}
	actions := svc.Flush("")
	if len(actions) != 1 || actions[0].Type != ActionCapture || actions[0].Text != "记得买牛奶" {
		t.Fatalf("actions=%v", actions)
	}
	if svc.Pending("") {
		t.Fatal("flush 后不应再有暂存")
	}
	// 重复 flush 是空操作，不会写重复片段。
	if again := svc.Flush(""); len(again) != 0 {
		t.Fatalf("重复 flush 不应产生动作: %v", again)
	}
}

func TestContextBeforeUsesRecentWithinWindow(t *testing.T) {
	svc, clock := newTestService()
	svc.OnText("", "第一句")
	svc.Flush("")
	clock.advance(time.Second)
	svc.OnText("", "第二句")
	svc.Flush("")
	clock.advance(time.Second)

	actions := svc.OnText("", "第三句")
	// 第三句暂存，此时还没有 capture 动作；flush 后才带上下文。
	_ = actions
	actions = svc.Flush("")
	if len(actions) != 1 {
		t.Fatalf("actions=%v", actions)
	}
	if actions[0].Before != "第一句。第二句" {
		t.Fatalf("before=%q", actions[0].Before)
	}

	// 超过 30s 的旧片段不再进入上下文。
	clock.advance(31 * time.Second)
	svc.OnText("", "很久以后的一句")
	actions = svc.Flush("")
	if len(actions) != 1 {
		t.Fatalf("actions=%v", actions)
	}
	if actions[0].Before != "" {
		t.Fatalf("过窗上下文应被清掉，实际 before=%q", actions[0].Before)
	}
}

func TestContextSegmentsAreCapped(t *testing.T) {
	svc, clock := newTestService()
	for _, text := range []string{"一", "二", "三", "四", "五"} {
		svc.OnText("", text)
		svc.Flush("")
		clock.advance(time.Second)
	}
	svc.OnText("", "六")
	actions := svc.Flush("")
	if len(actions) != 1 {
		t.Fatalf("actions=%v", actions)
	}
	// 最多 4 段，且按时间顺序。
	if actions[0].Before != "二。三。四。五" {
		t.Fatalf("before=%q", actions[0].Before)
	}
}

func TestDevicesAreIsolated(t *testing.T) {
	svc, _ := newTestService()
	svc.OnText("a", "手机A说的话")
	if svc.Pending("b") {
		t.Fatal("设备 B 不应看到 A 的暂存片段")
	}
	actions := svc.Flush("b")
	if len(actions) != 0 {
		t.Fatalf("设备 B flush 应为空: %v", actions)
	}
	actions = svc.Flush("a")
	if len(actions) != 1 || actions[0].Text != "手机A说的话" {
		t.Fatalf("actions=%v", actions)
	}
}

func TestWakeWordFlushesPendingFirst(t *testing.T) {
	svc, clock := newTestService()
	svc.OnText("", "随手说的一句话")
	clock.advance(time.Second)
	actions := svc.OnText("", "小金小金")
	// 先落库上一句，再播报唤醒应答。
	if got := strings.Join(types(actions), ","); got != "capture,speak" {
		t.Fatalf("actions=%v", actions)
	}
	if actions[0].Text != "随手说的一句话" || actions[0].After != "" {
		t.Fatalf("capture=%+v", actions[0])
	}
}

func TestMergeWaitAndEmptyInput(t *testing.T) {
	svc, _ := newTestService()
	if got := svc.MergeWait(); got != 4*time.Second {
		t.Fatalf("merge wait=%v", got)
	}
	if actions := svc.OnText("", "   "); len(actions) != 0 {
		t.Fatalf("空白文本不应产生动作: %v", actions)
	}
}

func TestCustomWakeWord(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	svc := NewService(Config{WakeWord: "你好管家", CommandWindow: time.Minute}, clock.now)
	actions := svc.OnText("", "你好管家关灯")
	if len(actions) != 1 || actions[0].Type != ActionChat || actions[0].Text != "关灯" {
		t.Fatalf("actions=%v", actions)
	}
	if actions := svc.OnText("", "小金小金关灯"); len(actions) != 1 || actions[0].Type == ActionChat {
		t.Fatalf("默认唤醒词不应再生效: %v", actions)
	}
}
