// Package timecn is a deterministic Chinese relative/absolute time parser.
// Rule-first (this file), LLM fallback lives in the nlu package and always
// validates the LLM's answer before accepting it.
package timecn

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TimeResult is a parsed time expression.
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

// weekdayMap maps Chinese weekday chars to Go weekdays (Sunday=0).
var weekdayMap = map[rune]time.Weekday{
	'一': time.Monday, '二': time.Tuesday, '三': time.Wednesday, '四': time.Thursday,
	'五': time.Friday, '六': time.Saturday, '日': time.Sunday, '天': time.Sunday,
}

// weekdayOf extracts the weekday from a capture group like "三" or "天".
func weekdayOf(s string) (time.Weekday, bool) {
	for _, r := range s {
		wd, ok := weekdayMap[r]
		return wd, ok
	}
	return 0, false
}

// Parse tries deterministic rules. ok=false means no rule matched (caller may
// fall back to LLM and then re-validate).
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

	// 1) explicit recurrence wins over one-shot parsing.
	if rec, abs, ok := parseRecurrence(raw, now, loc); ok {
		res.Recurrence = rec
		res.Abs = abs
		return res, true
	}

	// 2) relative offsets: X分钟后 / X小时后 / X天后.
	if t, ok := parseRelative(raw, now); ok {
		res.Abs = t
		return res, true
	}

	// 3) calendar expressions anchored on day keywords / weekday / dates.
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

// rePeriod matches bare time-of-day period words (day keywords handled separately).
var rePeriod = regexp.MustCompile(`(凌晨|早上|上午|中午|正午|下午|傍晚|晚上|夜里|夜间)`)

// periodInfo extracts the day offset and period word from raw's keywords.
// Day keywords that embed a period (明早/今晚…) seed the period when no bare
// period word is present.
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
	// A bare period word anywhere wins over an implied one.
	if m := rePeriod.FindStringSubmatch(raw); m != nil {
		period = m[1]
	}
	return dayOffset, hasDay, period
}

// timeOfDayDefault extracts an explicit clock time from raw, else returns the default.
func timeOfDayDefault(raw string, defHour, defMin int) tod {
	_, _, period := periodInfo(raw)
	hour, min, found := extractClock(raw, period)
	if !found {
		return tod{defHour, defMin}
	}
	return tod{hour, min}
}

// extractClock finds H点(半|MM分)? or HH:MM and applies the meridiem implied by period.
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
	// Meridiem adjustment. "下午/傍晚/晚上/夜里" shift afternoon hours.
	switch {
	case strings.Contains(period, "下午"), strings.Contains(period, "傍晚"),
		strings.Contains(period, "晚上"), strings.Contains(period, "夜里"),
		strings.Contains(period, "夜间"):
		if h < 12 {
			h += 12
		}
	case strings.Contains(period, "凌晨"):
		// keep small hours as-is
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
	// Chinese numerals 一..十[一二]
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
	// Full dates first.
	if m := reDateYMD.FindStringSubmatch(raw); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		tod := timeOfDayDefault(raw, 9, 0)
		return time.Date(y, time.Month(mo), d, tod.hour, tod.min, 0, 0, loc), true
	}

	dayOffset, hasDay, _ := periodInfo(raw)

	// Weekday expressions.
	if m := reWeekday.FindStringSubmatch(raw); m != nil {
		wd, ok := weekdayOf(m[2])
		if ok {
			tod := timeOfDayDefault(raw, 9, 0)
			target := nextWeekday(now, wd, m[1])
			return time.Date(target.Year(), target.Month(), target.Day(), tod.hour, tod.min, 0, 0, loc), true
		}
	}

	// M月D日 within the current year (roll to next year if already past).
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

	// Month/year end markers.
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

	// Day keyword or bare time of day: anchor on today + dayOffset.
	if hasDay || reHourMin.MatchString(raw) || reClock.MatchString(raw) {
		tod := timeOfDayDefault(raw, -1, -1)
		if tod.hour < 0 {
			if !hasDay {
				return time.Time{}, false
			}
			// Bare day keyword with no clock ("后天") defaults to 9:00.
			tod.hour, tod.min = 9, 0
		}
		d := now.AddDate(0, 0, dayOffset)
		t := time.Date(d.Year(), d.Month(), d.Day(), tod.hour, tod.min, 0, 0, loc)
		if !hasDay && t.Before(now) {
			// Bare clock with no day keyword: a past time rolls to tomorrow.
			// Explicit "今天/今晚" keeps the past time (user asked for today).
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	}
	return time.Time{}, false
}

// nextWeekday returns the next occurrence of wd.
// prefix: "下下" = week after next, "下" = next week, otherwise the upcoming one.
func nextWeekday(now time.Time, wd time.Weekday, prefix string) time.Time {
	days := (int(wd) - int(now.Weekday()) + 7) % 7
	if days == 0 {
		days = 7 // "周三" said on Wednesday means next Wednesday
	}
	switch {
	case strings.HasPrefix(prefix, "下下"):
		days += 7
	case strings.HasPrefix(prefix, "下"):
		// "下周三" = Wednesday of next week (ISO-style: jump to next Monday-based week).
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
