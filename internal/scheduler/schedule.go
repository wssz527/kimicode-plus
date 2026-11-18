package scheduler

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

var weekdayNames = []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

func ValidateRule(rule ScheduleRule) (ScheduleRule, error) {
	rule.Kind = strings.TrimSpace(rule.Kind)
	rule.Date = strings.TrimSpace(rule.Date)
	rule.Time = strings.TrimSpace(rule.Time)
	rule.IntervalUnit = strings.TrimSpace(rule.IntervalUnit)
	rule.StartDate = strings.TrimSpace(rule.StartDate)
	rule.EndDate = strings.TrimSpace(rule.EndDate)

	var startDay, endDay time.Time
	if rule.StartDate != "" {
		parsed, err := parseDate(rule.StartDate)
		if err != nil {
			return rule, errors.New("开始日期格式无效，应为 YYYY-MM-DD")
		}
		startDay = parsed
	}
	if rule.EndDate != "" {
		parsed, err := parseDate(rule.EndDate)
		if err != nil {
			return rule, errors.New("结束日期格式无效，应为 YYYY-MM-DD")
		}
		endDay = parsed
	}
	if rule.StartDate != "" && rule.EndDate != "" && endDay.Before(startDay) {
		return rule, errors.New("结束日期不能早于开始日期")
	}

	switch rule.Kind {
	case "once":
		targetDate, err := parseDate(rule.Date)
		if err != nil {
			return rule, errors.New("单次调度需要有效的执行日期（YYYY-MM-DD）")
		}
		if _, _, err := parseClock(rule.Time); err != nil {
			return rule, err
		}
		if rule.StartDate != "" && targetDate.Before(startDay) {
			return rule, errors.New("单次执行日期不能早于开始日期")
		}
		if rule.EndDate != "" && targetDate.After(endDay) {
			return rule, errors.New("单次执行日期不能晚于结束日期")
		}
	case "hourly":
		if rule.Minute < 0 || rule.Minute > 59 {
			return rule, errors.New("每小时调度的分钟数必须在 0–59 之间")
		}
	case "daily", "weekdays":
		if _, _, err := parseClock(rule.Time); err != nil {
			return rule, err
		}
	case "weekly":
		if _, _, err := parseClock(rule.Time); err != nil {
			return rule, err
		}
		weekdays, err := normalizeWeekdays(rule.Weekdays)
		if err != nil {
			return rule, err
		}
		if len(weekdays) == 0 {
			return rule, errors.New("每周调度至少需要选择一个星期")
		}
		rule.Weekdays = weekdays
	case "monthly":
		if _, _, err := parseClock(rule.Time); err != nil {
			return rule, err
		}
		days, err := normalizeDaysOfMonth(rule.DaysOfMonth)
		if err != nil {
			return rule, err
		}
		if len(days) == 0 && !rule.LastDay {
			return rule, errors.New("每月调度至少需要选择一天或勾选月末最后一天")
		}
		rule.DaysOfMonth = days
	case "interval":
		if rule.IntervalUnit != "minutes" && rule.IntervalUnit != "hours" {
			return rule, errors.New("间隔单位必须是 minutes 或 hours")
		}
		if rule.IntervalValue <= 0 {
			return rule, errors.New("间隔数值必须大于 0")
		}
		if rule.IntervalUnit == "minutes" {
			if !rule.TestMode && rule.IntervalValue < 5 {
				return rule, errors.New("非测试模式下最小间隔为 5 分钟；开启测试模式可设为 1 分钟")
			}
			if rule.IntervalValue > 43200 {
				return rule, errors.New("分钟间隔不能超过 43200 分钟")
			}
		} else if rule.IntervalValue > 720 {
			return rule, errors.New("小时间隔不能超过 720 小时")
		}
	case "times":
		times, err := normalizeClocks(rule.Times)
		if err != nil {
			return rule, err
		}
		if len(times) == 0 {
			return rule, errors.New("多时点调度至少需要添加一个执行时间")
		}
		rule.Times = times
		weekdays, err := normalizeWeekdays(rule.Weekdays)
		if err != nil {
			return rule, err
		}
		rule.Weekdays = weekdays
	default:
		return rule, fmt.Errorf("不支持的调度类型：%s", rule.Kind)
	}
	return rule, nil
}

func NextRun(rule ScheduleRule, after time.Time, loc *time.Location) (time.Time, bool) {
	if loc == nil {
		loc = time.Local
	}
	afterLoc := after.In(loc)

	var hasStart, hasEnd bool
	var startBound, endBound time.Time
	if rule.StartDate != "" {
		if d, err := parseDateIn(rule.StartDate, loc); err == nil {
			hasStart = true
			startBound = d
		}
	}
	if rule.EndDate != "" {
		if d, err := parseDateIn(rule.EndDate, loc); err == nil {
			hasEnd = true
			endBound = time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 999999999, loc)
		}
	}
	if hasEnd && !afterLoc.Before(endBound) {
		return time.Time{}, false
	}

	inWindow := func(t time.Time) bool {
		if !t.After(afterLoc) {
			return false
		}
		if hasStart && t.Before(startBound) {
			return false
		}
		if hasEnd && t.After(endBound) {
			return false
		}
		return true
	}

	anchorBase := afterLoc
	if hasStart && anchorBase.Before(startBound) {
		anchorBase = startBound.Add(-time.Nanosecond)
	}

	switch rule.Kind {
	case "once":
		d, err := parseDateIn(rule.Date, loc)
		if err != nil {
			return time.Time{}, false
		}
		hh, mm, err := parseClock(rule.Time)
		if err != nil {
			return time.Time{}, false
		}
		cand := time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, loc)
		if inWindow(cand) {
			return cand, true
		}
		return time.Time{}, false

	case "hourly":
		base := anchorBase
		for i := 0; i < 24*400; i++ {
			t := time.Date(base.Year(), base.Month(), base.Day(), base.Hour()+i, rule.Minute, 0, 0, loc)
			if hasEnd && t.After(endBound) {
				return time.Time{}, false
			}
			if inWindow(t) {
				return t, true
			}
		}
		return time.Time{}, false

	case "daily", "weekdays", "weekly":
		hh, mm, err := parseClock(rule.Time)
		if err != nil {
			return time.Time{}, false
		}
		allowedDays := map[time.Weekday]bool{}
		if rule.Kind == "weekdays" {
			for wd := time.Monday; wd <= time.Friday; wd++ {
				allowedDays[wd] = true
			}
		} else if rule.Kind == "weekly" {
			for _, wd := range rule.Weekdays {
				allowedDays[time.Weekday(wd)] = true
			}
		}
		startDay := time.Date(anchorBase.Year(), anchorBase.Month(), anchorBase.Day(), 0, 0, 0, 0, loc)
		for offset := 0; offset < 400; offset++ {
			day := startDay.AddDate(0, 0, offset)
			if hasEnd && day.After(endBound) {
				return time.Time{}, false
			}
			if rule.Kind != "daily" && !allowedDays[day.Weekday()] {
				continue
			}
			cand := time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, loc)
			if inWindow(cand) {
				return cand, true
			}
		}
		return time.Time{}, false

	case "monthly":
		hh, mm, err := parseClock(rule.Time)
		if err != nil {
			return time.Time{}, false
		}
		baseYear, baseMonth, _ := anchorBase.Date()
		for mOffset := 0; mOffset < 120; mOffset++ {
			firstOfMonth := time.Date(baseYear, baseMonth+time.Month(mOffset), 1, 0, 0, 0, 0, loc)
			if hasEnd && firstOfMonth.After(endBound) {
				return time.Time{}, false
			}
			y, m, _ := firstOfMonth.Date()
			dim := daysInMonth(y, m, loc)
			daySet := map[int]bool{}
			for _, d := range rule.DaysOfMonth {
				if d >= 1 && d <= dim {
					daySet[d] = true
				}
			}
			if rule.LastDay {
				daySet[dim] = true
			}
			days := make([]int, 0, len(daySet))
			for d := range daySet {
				days = append(days, d)
			}
			sort.Ints(days)
			for _, d := range days {
				cand := time.Date(y, m, d, hh, mm, 0, 0, loc)
				if hasEnd && cand.After(endBound) {
					return time.Time{}, false
				}
				if inWindow(cand) {
					return cand, true
				}
			}
		}
		return time.Time{}, false

	case "interval":
		if rule.IntervalValue <= 0 {
			return time.Time{}, false
		}
		step := time.Duration(rule.IntervalValue) * time.Minute
		if rule.IntervalUnit == "hours" {
			step = time.Duration(rule.IntervalValue) * time.Hour
		}
		var cand time.Time
		if hasStart {
			if afterLoc.Before(startBound) {
				cand = startBound
			} else {
				elapsed := afterLoc.Sub(startBound)
				steps := elapsed/step + 1
				cand = startBound.Add(steps * step)
			}
		} else {
			cand = afterLoc.Truncate(time.Minute).Add(step)
			if !cand.After(afterLoc) {
				cand = cand.Add(step)
			}
		}
		if hasEnd && cand.After(endBound) {
			return time.Time{}, false
		}
		if inWindow(cand) {
			return cand, true
		}
		return time.Time{}, false

	case "times":
		times, err := normalizeClocks(rule.Times)
		if err != nil || len(times) == 0 {
			return time.Time{}, false
		}
		allowedDays := map[time.Weekday]bool{}
		if len(rule.Weekdays) > 0 {
			for _, wd := range rule.Weekdays {
				allowedDays[time.Weekday(wd)] = true
			}
		}
		startDay := time.Date(anchorBase.Year(), anchorBase.Month(), anchorBase.Day(), 0, 0, 0, 0, loc)
		for offset := 0; offset < 400; offset++ {
			day := startDay.AddDate(0, 0, offset)
			if hasEnd && day.After(endBound) {
				return time.Time{}, false
			}
			if len(allowedDays) > 0 && !allowedDays[day.Weekday()] {
				continue
			}
			for _, clock := range times {
				hh, mm, err := parseClock(clock)
				if err != nil {
					continue
				}
				cand := time.Date(day.Year(), day.Month(), day.Day(), hh, mm, 0, 0, loc)
				if hasEnd && cand.After(endBound) {
					return time.Time{}, false
				}
				if inWindow(cand) {
					return cand, true
				}
			}
		}
		return time.Time{}, false
	}

	return time.Time{}, false
}

func FormatScheduleSummary(rule ScheduleRule) string {
	var base string
	switch rule.Kind {
	case "once":
		base = fmt.Sprintf("单次 %s %s", rule.Date, rule.Time)
	case "hourly":
		base = fmt.Sprintf("每小时 :%02d", rule.Minute)
	case "daily":
		base = fmt.Sprintf("每天 %s", rule.Time)
	case "weekdays":
		base = fmt.Sprintf("每个工作日 %s", rule.Time)
	case "weekly":
		base = fmt.Sprintf("每%s %s", formatWeekdays(rule.Weekdays), rule.Time)
	case "monthly":
		parts := make([]string, 0, len(rule.DaysOfMonth)+1)
		for _, d := range rule.DaysOfMonth {
			parts = append(parts, fmt.Sprintf("%d 日", d))
		}
		if rule.LastDay {
			parts = append(parts, "最后一天")
		}
		base = fmt.Sprintf("每月 %s %s", strings.Join(parts, "、"), rule.Time)
	case "interval":
		unit := "分钟"
		if rule.IntervalUnit == "hours" {
			unit = "小时"
		}
		base = fmt.Sprintf("每 %d %s", rule.IntervalValue, unit)
		if rule.TestMode {
			base += "（测试模式）"
		}
	case "times":
		prefix := "每天"
		if len(rule.Weekdays) > 0 {
			prefix = "每" + formatWeekdays(rule.Weekdays)
		}
		base = fmt.Sprintf("%s %s", prefix, strings.Join(rule.Times, "、"))
	default:
		base = rule.Kind
	}
	var rangeParts []string
	if rule.StartDate != "" {
		rangeParts = append(rangeParts, rule.StartDate+" 起")
	}
	if rule.EndDate != "" {
		rangeParts = append(rangeParts, "至 "+rule.EndDate)
	}
	if len(rangeParts) > 0 {
		base += "（" + strings.Join(rangeParts, "，") + "）"
	}
	return base
}

func formatWeekdays(days []int) string {
	parts := make([]string, 0, len(days))
	for _, wd := range days {
		if wd >= 0 && wd < len(weekdayNames) {
			parts = append(parts, weekdayNames[wd])
		}
	}
	if len(parts) == 0 {
		return "周"
	}
	return strings.Join(parts, "、")
}

func parseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.Local)
}

func parseDateIn(s string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, loc)
}

func parseClock(s string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("时间格式无效，应为 HH:MM")
	}
	hh, err1 := strconv.Atoi(parts[0])
	mm, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, 0, errors.New("时间格式无效，应为 24 小时制 HH:MM")
	}
	return hh, mm, nil
}

func normalizeWeekdays(raw []int) ([]int, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(raw))
	for _, wd := range raw {
		if wd < 0 || wd > 6 {
			return nil, errors.New("星期值必须在 0（周日）到 6（周六）之间")
		}
		if !seen[wd] {
			seen[wd] = true
			out = append(out, wd)
		}
	}
	sort.Ints(out)
	return out, nil
}

func normalizeDaysOfMonth(raw []int) ([]int, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(raw))
	for _, d := range raw {
		if d < 1 || d > 31 {
			return nil, errors.New("每月日期必须在 1 到 31 之间")
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Ints(out)
	return out, nil
}

func normalizeClocks(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		hh, mm, err := parseClock(item)
		if err != nil {
			return nil, err
		}
		norm := fmt.Sprintf("%02d:%02d", hh, mm)
		if !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	sort.Strings(out)
	return out, nil
}

func daysInMonth(year int, month time.Month, loc *time.Location) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
}
