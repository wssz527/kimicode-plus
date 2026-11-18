package quota

import (
	"math"
	"sort"
	"strings"
)

// Parsed is one adapter's answer before it becomes a card.
type Parsed struct {
	Available *bool
	Records   []Record
	Parts     []Part
}

// Part is what a balance is made of. Informational: these are not amounts that
// add up with `remaining`.
type Part struct {
	Label  string  `json:"label"`
	Amount float64 `json:"amount"`
	Unit   *string `json:"unit"`
}

// Record is one reading with its own unit. Exactly one of {Limit, Used} and
// {Remaining} is meaningful for a given record; a percentage window states
// Limit=100 with Used as the percentage, which a renderer can draw without ever
// treating it as money.
type Record struct {
	Kind        string   `json:"kind"`
	Unit        *string  `json:"unit"`
	WindowLabel *string  `json:"windowLabel"`
	Limit       *float64 `json:"limit"`
	Used        *float64 `json:"used"`
	Remaining   *float64 `json:"remaining"`
	PeriodStart *int64   `json:"periodStart"`
	ResetAt     *int64   `json:"resetAt"`
}

func percentWindow(label string, usedPercent float64, resetAt *int64) Record {
	limit := 100.0
	used := math.Min(100, math.Max(0, round6(usedPercent)))
	remaining := round6(100 - used)
	unit := "percent"
	window := label
	return Record{Kind: "coding_plan", Unit: &unit, WindowLabel: &window,
		Limit: &limit, Used: &used, Remaining: &remaining, ResetAt: resetAt}
}

// ParseDeepSeekBalance reads the documented DeepSeek reply. Amounts are strings
// in the official reply, and the order of `balance_infos` is not stable between
// reads, so the records are sorted: a currency with money left first, then by
// code. A card that reorders itself on every refresh is worse than a card that
// is slightly arbitrary.
func ParseDeepSeekBalance(document any) (Parsed, string) {
	object, ok := document.(map[string]any)
	if !ok {
		return Parsed{}, "invalid_response"
	}
	infos, ok := object["balance_infos"].([]any)
	if !ok {
		return Parsed{}, "invalid_response"
	}
	type reading struct {
		currency string
		total    float64
		granted  *float64
		topped   *float64
	}
	readings := []reading{}
	for _, row := range infos {
		info, ok := row.(map[string]any)
		if !ok {
			continue
		}
		currency := strings.ToUpper(strings.TrimSpace(stringOf(info["currency"])))
		if !currencyPattern.MatchString(currency) {
			continue
		}
		total := ToAmount(info["total_balance"])
		if total == nil {
			continue
		}
		readings = append(readings, reading{currency: currency, total: *total,
			granted: ToAmount(info["granted_balance"]), topped: ToAmount(info["topped_up_balance"])})
	}
	if len(readings) == 0 {
		return Parsed{}, "invalid_response"
	}
	sort.SliceStable(readings, func(i, j int) bool {
		left, right := readings[i], readings[j]
		if (left.total > 0) != (right.total > 0) {
			return left.total > 0
		}
		return left.currency < right.currency
	})
	parts := []Part{}
	records := []Record{}
	for _, reading := range readings {
		if reading.granted != nil && reading.topped != nil && *reading.granted != 0 {
			unit := reading.currency
			parts = append(parts, Part{Label: "赠金", Amount: *reading.granted, Unit: &unit})
			parts = append(parts, Part{Label: "充值", Amount: *reading.topped, Unit: &unit})
		}
		unit := reading.currency
		remaining := reading.total
		records = append(records, Record{Unit: &unit, Remaining: &remaining})
	}
	parsed := Parsed{Records: records, Parts: parts}
	if flag, ok := object["is_available"].(bool); ok {
		parsed.Available = &flag
	}
	return parsed, ""
}

// ParseMoonshotBalance reads the documented Moonshot reply. `code` 0 is the
// documented success value; anything else is a refusal whose text is not read.
// `cash_balance` may be negative (money owed), so only the available figure is
// used as `remaining`.
func ParseMoonshotBalance(document any) (Parsed, string) {
	object, ok := document.(map[string]any)
	if !ok {
		return Parsed{}, "invalid_response"
	}
	if code, ok := object["code"].(float64); !ok || code != 0 {
		return Parsed{}, "auth_rejected"
	}
	if status, ok := object["status"].(bool); ok && !status {
		return Parsed{}, "invalid_response"
	}
	data, ok := object["data"].(map[string]any)
	if !ok {
		return Parsed{}, "invalid_response"
	}
	available := ToAmount(data["available_balance"])
	if available == nil {
		return Parsed{}, "invalid_response"
	}
	parts := []Part{}
	if voucher := ToAmount(data["voucher_balance"]); voucher != nil {
		parts = append(parts, Part{Label: "代金券", Amount: *voucher})
	}
	if cash := ToAmount(data["cash_balance"]); cash != nil {
		parts = append(parts, Part{Label: "现金", Amount: *cash})
	}
	return Parsed{Records: []Record{{Remaining: available}}, Parts: parts}, ""
}

// planWindowLabel names a plan window by the interval it covers. Zhipu's
// TIME_LIMIT is the MCP monthly allowance and must win over the number/unit
// mapping: that type carries `1 month`, which the general branch would otherwise
// read as one minute and label "5h".
func planWindowLabel(limit map[string]any) string {
	if stringOf(limit["type"]) == "TIME_LIMIT" {
		return "MCP"
	}
	number := ToAmount(limit["number"])
	if number == nil {
		number = ToAmount(limit["windowSize"])
	}
	if number == nil {
		number = ToAmount(limit["window"])
	}
	unit := strings.ToLower(strings.TrimSpace(stringOf(limit["unit"])))
	if unit == "" {
		unit = strings.ToLower(strings.TrimSpace(stringOf(limit["windowUnit"])))
	}
	if number != nil && *number > 0 {
		minutes := *number
		switch {
		case strings.HasPrefix(unit, "hour") || unit == "h":
			minutes *= 60
		case strings.HasPrefix(unit, "day") || unit == "d":
			minutes *= 1440
		}
		switch {
		case minutes <= 360:
			return "5h"
		case minutes <= 11000:
			return "1w"
		default:
			return "1M"
		}
	}
	return "额度"
}

// ParseBigModelQuota reads the Zhipu/BigModel coding-plan reply. The field names
// are not fully documented, so the parse is tolerant, and an entry whose usage
// cannot be read is skipped rather than reported as 0%.
func ParseBigModelQuota(document any) (Parsed, string) {
	root, ok := document.(map[string]any)
	if !ok {
		return Parsed{}, "invalid_response"
	}
	data, _ := root["data"].(map[string]any)
	limits, _ := data["limits"].([]any)
	records := []Record{}
	for _, row := range limits {
		limit, ok := row.(map[string]any)
		if !ok {
			continue
		}
		percent := ToAmount(limit["percentage"])
		if percent == nil {
			percent = ToAmount(limit["usagePercent"])
		}
		if percent == nil {
			percent = ToAmount(limit["rate"])
		}
		if percent == nil {
			used := ToAmount(limit["usage"])
			if used == nil {
				used = ToAmount(limit["used"])
			}
			if used == nil {
				used = ToAmount(limit["currentValue"])
			}
			total := ToAmount(limit["total"])
			if total == nil {
				total = ToAmount(limit["limit"])
			}
			if used != nil && total != nil && *total > 0 {
				value := *used / *total * 100
				percent = &value
			}
		}
		if percent == nil {
			continue
		}
		var resetAt *int64
		if stamp := ToTimestamp(limit["nextResetTime"]); stamp != nil {
			resetAt = stamp
		}
		records = append(records, percentWindow(planWindowLabel(limit), *percent, resetAt))
	}
	plan := stringOf(data["planName"])
	if plan == "" {
		plan = stringOf(data["plan"])
	}
	if plan == "" {
		plan = stringOf(data["packageName"])
	}
	if len(records) == 0 && plan == "" {
		return Parsed{}, "invalid_response"
	}
	parsed := Parsed{Records: records, Parts: []Part{}}
	if plan != "" {
		parsed.Parts = append(parsed.Parts, Part{Label: "套餐", Amount: 0, Unit: nil})
	}
	return parsed, ""
}

// PlanNameOf returns a plan name a parsed reply carried, or "". It is kept
// separate from Parsed because a name is not an amount and must not become one.
func PlanNameOf(document any) string {
	root, ok := document.(map[string]any)
	if !ok {
		return ""
	}
	data, _ := root["data"].(map[string]any)
	for _, key := range []string{"planName", "plan", "plan_type", "packageName", "tier"} {
		if name := strings.TrimSpace(stringOf(data[key])); name != "" {
			return name
		}
	}
	return ""
}

// ParseMiniMaxPlan reads the MiniMax coding-plan reply. The live shape is
// `model_remains` with remaining percentages (2026-08 captured), and the flat
// fields are the earlier spelling. A non-zero `base_resp.status_code` means the
// key was refused even though HTTP answered 200.
func ParseMiniMaxPlan(document any) (Parsed, string) {
	root, ok := document.(map[string]any)
	if !ok {
		return Parsed{}, "invalid_response"
	}
	if base, ok := root["base_resp"].(map[string]any); ok {
		if code := ToAmount(base["status_code"]); code != nil && *code != 0 {
			return Parsed{}, "auth_rejected"
		}
	}
	records := []Record{}
	if rows, ok := root["model_remains"].([]any); ok && len(rows) > 0 {
		entry, _ := rows[0].(map[string]any)
		for _, row := range rows {
			candidate, ok := row.(map[string]any)
			if ok && stringOf(candidate["model_name"]) == "general" {
				entry = candidate
				break
			}
		}
		if entry != nil {
			var reset5h, resetWeek *int64
			if stamp := ToTimestamp(entry["end_time"]); stamp != nil {
				reset5h = stamp
			}
			if window := ToAmount(entry["current_interval_remaining_percent"]); window != nil {
				records = append(records, percentWindow("5h", 100-*window, reset5h))
			}
			if stamp := ToTimestamp(entry["weekly_end_time"]); stamp != nil {
				resetWeek = stamp
			}
			if window := ToAmount(entry["current_weekly_remaining_percent"]); window != nil {
				records = append(records, percentWindow("1w", 100-*window, resetWeek))
			}
		}
	}
	if len(records) == 0 {
		data := root
		if nested, ok := root["data"].(map[string]any); ok {
			data = nested
		}
		percent := ToAmount(data["percentage"])
		if percent == nil {
			percent = ToAmount(data["usage_percent"])
		}
		if percent == nil {
			percent = ToAmount(data["usedPercent"])
		}
		if percent == nil {
			used := ToAmount(data["used"])
			if used == nil {
				used = ToAmount(data["used_quota"])
			}
			total := ToAmount(data["total"])
			if total == nil {
				total = ToAmount(data["total_quota"])
			}
			if total == nil {
				total = ToAmount(data["limit"])
			}
			if used != nil && total != nil && *total > 0 {
				value := *used / *total * 100
				percent = &value
			}
			if percent == nil {
				remains := ToAmount(data["remains"])
				if remains == nil {
					remains = ToAmount(data["remaining"])
				}
				if remains != nil && total != nil && *total > 0 {
					value := (*total - *remains) / *total * 100
					percent = &value
				}
			}
		}
		var resetAt *int64
		for _, key := range []string{"next_reset_time", "nextResetTime", "resetTime"} {
			if stamp := ToTimestamp(data[key]); stamp != nil {
				resetAt = stamp
				break
			}
		}
		if percent != nil {
			records = append(records, percentWindow(planWindowLabel(data), *percent, resetAt))
		}
	}
	if len(records) == 0 && PlanNameOf(document) == "" {
		return Parsed{}, "invalid_response"
	}
	return Parsed{Records: records, Parts: []Part{}}, ""
}

// ParseStepFunBalance reads the documented StepFun accounts reply: the prepaid
// balance plus the voucher and cash parts, all in yuan.
func ParseStepFunBalance(document any) (Parsed, string) {
	object, ok := document.(map[string]any)
	if !ok {
		return Parsed{}, "invalid_response"
	}
	balance := ToAmount(object["balance"])
	if balance == nil {
		return Parsed{}, "invalid_response"
	}
	parts := []Part{}
	unit := "CNY"
	if voucher := ToAmount(object["total_voucher_balance"]); voucher != nil {
		parts = append(parts, Part{Label: "赠金", Amount: *voucher, Unit: &unit})
	}
	if cash := ToAmount(object["total_cash_balance"]); cash != nil {
		parts = append(parts, Part{Label: "现金", Amount: *cash, Unit: &unit})
	}
	return Parsed{Records: []Record{{Unit: &unit, Remaining: balance}}, Parts: parts}, ""
}