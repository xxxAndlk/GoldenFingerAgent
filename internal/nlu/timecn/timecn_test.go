package timecn

import (
	"testing"
	"time"
)

var cst = time.FixedZone("CST", 8*3600)

// anchor: 2026-03-05（周四）10:00 CST。
var anchor = time.Date(2026, 3, 5, 10, 0, 0, 0, cst)

func mustParse(t *testing.T, raw string) TimeResult {
	t.Helper()
	res, ok := Parse(raw, anchor, cst)
	if !ok {
		t.Fatalf("Parse(%q) failed to match any rule", raw)
	}
	return res
}

func assertTime(t *testing.T, raw string, want time.Time) {
	t.Helper()
	got := mustParse(t, raw)
	if !got.Abs.Equal(want) {
		t.Errorf("Parse(%q) = %v, want %v", raw, got.Abs, want)
	}
	if got.Method != "rule" {
		t.Errorf("Parse(%q) method = %q, want rule", raw, got.Method)
	}
}

func TestRelativeOffsets(t *testing.T) {
	assertTime(t, "20分钟后", anchor.Add(20*time.Minute))
	assertTime(t, "半小时后", anchor.Add(30*time.Minute))
	assertTime(t, "2个小时后", anchor.Add(2*time.Hour))
	assertTime(t, "2小时后", anchor.Add(2*time.Hour))
	assertTime(t, "3天后", time.Date(2026, 3, 8, 9, 0, 0, 0, cst))
}

func TestBareDayKeywordDefaultsTo9AM(t *testing.T) {
	assertTime(t, "后天", time.Date(2026, 3, 7, 9, 0, 0, 0, cst))
	assertTime(t, "明天", time.Date(2026, 3, 6, 9, 0, 0, 0, cst))
}

func TestDayKeywordsWithClock(t *testing.T) {
	assertTime(t, "明早7点", time.Date(2026, 3, 6, 7, 0, 0, 0, cst))
	assertTime(t, "明天早上7点", time.Date(2026, 3, 6, 7, 0, 0, 0, cst))
	assertTime(t, "后天下午3点", time.Date(2026, 3, 7, 15, 0, 0, 0, cst))
	assertTime(t, "今天晚上8点", time.Date(2026, 3, 5, 20, 0, 0, 0, cst))
	assertTime(t, "今晚9点", time.Date(2026, 3, 5, 21, 0, 0, 0, cst))
	assertTime(t, "大后天上午10点半", time.Date(2026, 3, 8, 10, 30, 0, 0, cst))
	assertTime(t, "明天中午12点", time.Date(2026, 3, 6, 12, 0, 0, 0, cst))
}

func TestBareClockRollsToTomorrowWhenPast(t *testing.T) {
	// 7:00 已过 10:00 锚点 → 明天 07:00。
	assertTime(t, "7点叫我", time.Date(2026, 3, 6, 7, 0, 0, 0, cst))
	// 15:00 今天还没到。
	assertTime(t, "下午3点提醒我", time.Date(2026, 3, 5, 15, 0, 0, 0, cst))
}

func TestChineseHourNumerals(t *testing.T) {
	assertTime(t, "明天下午三点", time.Date(2026, 3, 6, 15, 0, 0, 0, cst))
	assertTime(t, "明天上午九点一刻", time.Date(2026, 3, 6, 9, 15, 0, 0, cst))
}

func TestWeekdays(t *testing.T) {
	// 锚点是周四 2026-03-05。
	assertTime(t, "下周三", time.Date(2026, 3, 11, 9, 0, 0, 0, cst))
	assertTime(t, "下周三下午3点", time.Date(2026, 3, 11, 15, 0, 0, 0, cst))
	assertTime(t, "周六", time.Date(2026, 3, 7, 9, 0, 0, 0, cst))
	assertTime(t, "星期天早上8点", time.Date(2026, 3, 8, 8, 0, 0, 0, cst))
}

func TestDates(t *testing.T) {
	assertTime(t, "3月8日", time.Date(2026, 3, 8, 9, 0, 0, 0, cst))
	assertTime(t, "3月8号上午10点", time.Date(2026, 3, 8, 10, 0, 0, 0, cst))
	assertTime(t, "2027年1月5日", time.Date(2027, 1, 5, 9, 0, 0, 0, cst))
	// 已过去的日期顺延到下一年。
	assertTime(t, "3月1日", time.Date(2027, 3, 1, 9, 0, 0, 0, cst))
}

func TestPeriodEnds(t *testing.T) {
	assertTime(t, "月底", time.Date(2026, 3, 31, 9, 0, 0, 0, cst))
	assertTime(t, "年底", time.Date(2026, 12, 31, 9, 0, 0, 0, cst))
}

func TestRecurrence(t *testing.T) {
	got := mustParse(t, "每天早上8点")
	if got.Recurrence != "daily@08:00" {
		t.Errorf("recurrence = %q", got.Recurrence)
	}
	got = mustParse(t, "每周三晚上7点")
	if got.Recurrence != "weekly@W3T19:00" {
		t.Errorf("recurrence = %q", got.Recurrence)
	}
	got = mustParse(t, "每月5号上午9点")
	if got.Recurrence != "monthly@5T09:00" {
		t.Errorf("recurrence = %q", got.Recurrence)
	}
}

func TestUnparseableReturnsFalse(t *testing.T) {
	for _, raw := range []string{"", "随便说说", "有空的时候", "尽快"} {
		if _, ok := Parse(raw, anchor, cst); ok {
			t.Errorf("Parse(%q) should not match", raw)
		}
	}
}

func TestMidnightBoundary(t *testing.T) {
	late := time.Date(2026, 3, 5, 23, 30, 0, 0, cst)
	res, ok := Parse("20分钟后", late, cst)
	if !ok {
		t.Fatal("parse failed")
	}
	if !res.Abs.Equal(time.Date(2026, 3, 5, 23, 50, 0, 0, cst)) {
		t.Errorf("got %v", res.Abs)
	}
	res, ok = Parse("2小时后", late, cst)
	if !ok {
		t.Fatal("parse failed")
	}
	if !res.Abs.Equal(time.Date(2026, 3, 6, 1, 30, 0, 0, cst)) {
		t.Errorf("cross-midnight: got %v", res.Abs)
	}
}
