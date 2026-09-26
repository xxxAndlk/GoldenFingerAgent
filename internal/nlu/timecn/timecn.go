// Package timecn 是确定性的中文相对/绝对时间解析器。
// 规则优先（本文件），LLM 兜底位于 nlu 包，且接受前总是校验 LLM 的答案。
package timecn

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TimeResult 是解析后的时间表达式。
type TimeResult struct {
	Abs        time.Time
	Deadline   *time.Time
	Recurrence string // "", "daily@HH:MM", "weekly@WnTHH:MM" (W1=Monday..W7=Sunday)
	Raw        string
	Method     string // "rule"
}

var (
	reMinutesLater = regexp.MustCompile(`(\d+|半)\s*个?\s*分钟(?:钟)?后`)
	reHoursLater   = regexp.MustCompile(`(\d+|两|半)\s*个?\s*小时后`)
	reDaysLater    = regexp.MustCompile(`(\d+)\s*天后`)

	reDayKeyword = regexp.MustCompile(`(大后天|后天|明天|明日|今天|今日|今晚|今晨|明早|明晚|明晨|早上|上午|中午|正午|下午|傍晚|晚上|夜里|夜间|凌晨)`)
	reWeekday    = regexp.MustCompile(`(下下个?|下个?|本|这个?)?\s*(?:周|星期|礼拜)([一二三四五六日天])`)
	reDateMD     = regexp.MustCompile(`(\d{1,2})\s*月\s*(\d{1,2})\s*[日号]?`)
	reDateYMD    = regexp.MustCompile(`(\d{4})\s*年\s*(\d{1,2})\s*月\s*(\d{1,2})\s*[日号]?`)

	reHourMin = regexp.MustCompile(`(?:凌晨|早上|上午|中午|正午|下午|傍晚|晚上|夜里|夜间)?\s*(\d{1,2}|十[一二]?|[一二两三四五六七八九十])\s*[点時时]\s*(半|一刻|三刻|\d{1,2}\s*分?)?`)
	reClock   = regexp.MustCompile(`(\d{1,2}):(\d{2})`)

	reEveryDay   = regexp.MustCompile(`每天|每日|天天`)
	reEveryWeek  = regexp.MustCompile(`每(?:个)?(?:周|星期|礼拜)([一二三四五六日天])`)
	reEveryMonth = regexp.MustCompile(`每月\s*(\d{1,2})\s*[日号]?`)
	reMonthEnd   = regexp.MustCompile(`月底|月末`)
	reYearEnd    = regexp.MustCompile(`年底|年末`)
	reMinuteMark = regexp.MustCompile(`(\d{1,2})\s*分(?:钟)?`)
)

// weekdayMap 把中文星期字符映射为 Go 的星期（周日=0）。
var weekdayMap = map[rune]time.Weekday{
	'一': time.Monday, '二': time.Tuesday, '三': time.Wednesday, '四': time.Thursday,
	'五': time.Friday, '六': time.Saturday, '日': time.Sunday, '天': time.Sunday,
}

// weekdayOf 从类似 "三" 或 "天" 的捕获组中提取星期。
func weekdayOf(s string) (time.Weekday, bool) {
	for _, r := range s {
		wd, ok := weekdayMap[r]
		return wd, ok
	}
	return 0, false
}

// Parse 依次尝试确定性规则。ok=false 表示没有规则命中（调用方可以
// 回落到 LLM，然后再重新校验）。
func Parse(raw string, now time.Time, loc *time.Location) (TimeResult, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return TimeResult{}, false
	}
	if loc == nil {
		loc = now.Location()
	}
	now = now.In(loc)
	res := TimeResult{Raw: raw, Method: "rule"}

	// 1) 显式重复规则优先于一次性解析。
	if rec, abs, ok := parseRecurrence(raw, now, loc); ok {
		res.Recurrence = rec
		res.Abs = abs
		return res, true
	}

	// 2) 相对偏移：X分钟后 / X小时后 / X天后。
	if t, ok := parseRelative(raw, now); ok {
		res.Abs = t
		return res, true
	}

	// 3) 以日期关键词/星期/日期为锚点的日历表达。
	if t, ok := parseCalendar(raw, now, loc); ok {
		res.Abs = t
		return res, true
	}

	return TimeResult{}, false
}

func parseRelative(raw string, now time.Time) (time.Time, bool) {
	if m := reMinutesLater.FindStringSubmatch(raw); m != nil {
		if m[1] == "半" {
			return now.Add(30 * time.Minute), true
		}
		n, _ := strconv.Atoi(m[1])
		if n > 0 {
			return now.Add(time.Duration(n) * time.Minute), true
		}
	}
	if m := reHoursLater.FindStringSubmatch(raw); m != nil {
		switch m[1] {
		case "半":
			return now.Add(30 * time.Minute), true
		case "两":
			return now.Add(2 * time.Hour), true
		default:
			n, _ := strconv.Atoi(m[1])
			if n > 0 {
				return now.Add(time.Duration(n) * time.Hour), true
			}
		}
	}
	if m := reDaysLater.FindStringSubmatch(raw); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n > 0 {
			tod := timeOfDayDefault(raw, 9, 0)
			d := now.AddDate(0, 0, n)
			return time.Date(d.Year(), d.Month(), d.Day(), tod.hour, tod.min, 0, 0, locOf(now)), true
		}
	}
	return time.Time{}, false
}

type tod struct{ hour, min int }

// rePeriod 匹配单纯的时段词（日期关键词另行处理）。
var rePeriod = regexp.MustCompile(`(凌晨|早上|上午|中午|正午|下午|傍晚|晚上|夜里|夜间)`)

// periodInfo 从 raw 的关键词中提取日期偏移与时段词。
// 内嵌时段的日期关键词（明早/今晚…）在没有独立时段词时作为时段来源。
func periodInfo(raw string) (dayOffset int, hasDay bool, period string) {
	for _, m := range reDayKeyword.FindAllStringSubmatch(raw, -1) {
		switch m[1] {
		case "大后天":
			dayOffset, hasDay = 3, true
		case "后天":
			dayOffset, hasDay = 2, true
		case "明天", "明日", "明早", "明晚", "明晨":
			dayOffset, hasDay = 1, true
		case "今天", "今日", "今晚", "今晨":
			dayOffset, hasDay = 0, true
		}
		switch m[1] {
		case "明早", "明晨", "今晨":
			period = "早上"
		case "明晚", "今晚":
			period = "晚上"
		case "早上", "上午", "中午", "正午", "下午", "傍晚", "晚上", "夜里", "夜间", "凌晨":
			period = m[1]
		}
	}
	// 任何位置的独立时段词都优先于隐含的时段。
	if m := rePeriod.FindStringSubmatch(raw); m != nil {
		period = m[1]
	}
	return dayOffset, hasDay, period
}

// timeOfDayDefault 从 raw 中提取显式时钟时间，否则返回默认值。
func timeOfDayDefault(raw string, defHour, defMin int) tod {
	_, _, period := periodInfo(raw)
	hour, min, found := extractClock(raw, period)
	if !found {
		return tod{defHour, defMin}
	}
	return tod{hour, min}
}

// extractClock 查找 H点(半|MM分)? 或 HH:MM，并应用时段隐含的上午/下午调整。
func extractClock(raw, period string) (int, int, bool) {
	if m := reClock.FindStringSubmatch(raw); m != nil {
		h, _ := strconv.Atoi(m[1])
		mm, _ := strconv.Atoi(m[2])
		if h < 24 && mm < 60 {
			return h, mm, true
		}
	}
	m := reHourMin.FindStringSubmatch(raw)
	if m == nil {
		return 0, 0, false
	}
	h := parseHourToken(m[1])
	min := 0
	if m[2] != "" {
		switch {
		case m[2] == "半":
			min = 30
		case m[2] == "一刻":
			min = 15
		case m[2] == "三刻":
			min = 45
		default:
			minStr := reMinuteMark.FindString(m[2])
			if minStr != "" {
				min, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(minStr, "分钟"), "分"))
			}
		}
	}
	// 上午/下午调整。"下午/傍晚/晚上/夜里" 把下午的小时数偏移。
	switch {
	case strings.Contains(period, "下午"), strings.Contains(period, "傍晚"),
		strings.Contains(period, "晚上"), strings.Contains(period, "夜里"),
		strings.Contains(period, "夜间"):
		if h < 12 {
			h += 12
		}
	case strings.Contains(period, "凌晨"):
		// 凌晨的小时数保持原样
	case strings.Contains(period, "中午"), strings.Contains(period, "正午"):
		if h < 11 {
			h += 12
		}
	}
	return h, min, true
}

func parseHourToken(tok string) int {
	if n, err := strconv.Atoi(tok); err == nil {
		return n
	}
	// 中文数字 一..十[一二]
	conv := map[string]int{
		"一": 1, "二": 2, "两": 2, "三": 3, "四": 4, "五": 5, "六": 6,
		"七": 7, "八": 8, "九": 9, "十": 10, "十一": 11, "十二": 12,
	}
	if n, ok := conv[tok]; ok {
		return n
	}
	return 0
}

func parseCalendar(raw string, now time.Time, loc *time.Location) (time.Time, bool) {
	// 先处理完整日期。
	if m := reDateYMD.FindStringSubmatch(raw); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		tod := timeOfDayDefault(raw, 9, 0)
		return time.Date(y, time.Month(mo), d, tod.hour, tod.min, 0, 0, loc), true
	}

	dayOffset, hasDay, _ := periodInfo(raw)

	// 星期表达。
	if m := reWeekday.FindStringSubmatch(raw); m != nil {
		wd, ok := weekdayOf(m[2])
		if ok {
			tod := timeOfDayDefault(raw, 9, 0)
			target := nextWeekday(now, wd, m[1])
			return time.Date(target.Year(), target.Month(), target.Day(), tod.hour, tod.min, 0, 0, loc), true
		}
	}

	// 当年内的 M月D日（若已过去则顺延到下一年）。
	if m := reDateMD.FindStringSubmatch(raw); m != nil {
		mo, _ := strconv.Atoi(m[1])
		d, _ := strconv.Atoi(m[2])
		if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
			tod := timeOfDayDefault(raw, 9, 0)
			t := time.Date(now.Year(), time.Month(mo), d, tod.hour, tod.min, 0, 0, loc)
			if t.Before(now) {
				t = t.AddDate(1, 0, 0)
			}
			return t, true
		}
	}

	// 月/年末标记。
	if reMonthEnd.MatchString(raw) {
		tod := timeOfDayDefault(raw, 9, 0)
		firstOfNext := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, loc)
		lastDay := firstOfNext.AddDate(0, 0, -1)
		return time.Date(lastDay.Year(), lastDay.Month(), lastDay.Day(), tod.hour, tod.min, 0, 0, loc), true
	}
	if reYearEnd.MatchString(raw) {
		tod := timeOfDayDefault(raw, 9, 0)
		return time.Date(now.Year(), 12, 31, tod.hour, tod.min, 0, 0, loc), true
	}

	// 日期关键词或单纯时段：以今天 + dayOffset 为锚点。
	if hasDay || reHourMin.MatchString(raw) || reClock.MatchString(raw) {
		tod := timeOfDayDefault(raw, -1, -1)
		if tod.hour < 0 {
			if !hasDay {
				return time.Time{}, false
			}
				// 无时钟的裸日期关键词（"后天"）默认 9:00。
			tod.hour, tod.min = 9, 0
		}
		d := now.AddDate(0, 0, dayOffset)
		t := time.Date(d.Year(), d.Month(), d.Day(), tod.hour, tod.min, 0, 0, loc)
		if !hasDay && t.Before(now) {
				// 无日期关键词的裸时钟：已过去的时间顺延到明天。
				// 显式 "今天/今晚" 保留已过去的时间（用户明确要今天）。
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	}
	return time.Time{}, false
}

// nextWeekday 返回 wd 的下一次出现。
// 前缀："下下" = 下下周，"下" = 下周，否则是最近的一次。
func nextWeekday(now time.Time, wd time.Weekday, prefix string) time.Time {
	days := (int(wd) - int(now.Weekday()) + 7) % 7
	if days == 0 {
		days = 7 // 周三当天说"周三"指下周三
	}
	switch {
	case strings.HasPrefix(prefix, "下下"):
		days += 7
	case strings.HasPrefix(prefix, "下"):
			// "下周三" = 下下周的周三（ISO 风格：跳到下一个以周一为起点的一周）。
		daysToNextMon := (8 - int(now.Weekday())) % 7
		if daysToNextMon == 0 {
			daysToNextMon = 7
		}
		return now.AddDate(0, 0, daysToNextMon+int(wd)-1)
	}
	return now.AddDate(0, 0, days)
}

func parseRecurrence(raw string, now time.Time, loc *time.Location) (string, time.Time, bool) {
	// 每周X [time] → weekly@WnTHH:MM
	if m := reEveryWeek.FindStringSubmatch(raw); m != nil {
		wd, ok := weekdayOf(m[1])
		if !ok {
			return "", time.Time{}, false
		}
		tod := timeOfDayDefault(raw, 8, 0)
		iso := int(wd)
		if iso == 0 {
			iso = 7
		}
		rec := "weekly@W" + strconv.Itoa(iso) + "T" + two(tod.hour) + ":" + two(tod.min)
		return rec, nextWeekday(now, wd, ""), true
	}
	// 每月D日 [time]
	if m := reEveryMonth.FindStringSubmatch(raw); m != nil {
		d, _ := strconv.Atoi(m[1])
		tod := timeOfDayDefault(raw, 8, 0)
		rec := "monthly@" + strconv.Itoa(d) + "T" + two(tod.hour) + ":" + two(tod.min)
		t := time.Date(now.Year(), now.Month(), d, tod.hour, tod.min, 0, 0, loc)
		if t.Before(now) {
			t = t.AddDate(0, 1, 0)
		}
		return rec, t, true
	}
	// 每天 [time]
	if reEveryDay.MatchString(raw) {
		tod := timeOfDayDefault(raw, 8, 0)
		rec := "daily@" + two(tod.hour) + ":" + two(tod.min)
		t := time.Date(now.Year(), now.Month(), now.Day(), tod.hour, tod.min, 0, 0, loc)
		if t.Before(now) {
			t = t.AddDate(0, 0, 1)
		}
		return rec, t, true
	}
	return "", time.Time{}, false
}

func two(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func locOf(t time.Time) *time.Location { return t.Location() }
