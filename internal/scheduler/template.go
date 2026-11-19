package scheduler

import (
	"strings"
	"time"

	"enhancedkimi/internal/storage"
)

func FormatScheduledSessionTitle(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return strings.TrimSpace(ScheduledSessionPrefix)
	}
	if strings.HasPrefix(trimmed, ScheduledSessionPrefix) {
		return trimmed
	}
	return ScheduledSessionPrefix + trimmed
}

// ExpandPrompt substitutes supported template variables in a single pass so
// values such as last_run_summary cannot trigger recursive expansion. Unknown
// placeholders are preserved verbatim.
func ExpandPrompt(tmpl string, now time.Time, loc *time.Location, lastRunAt, lastRunSummary string) string {
	if loc == nil {
		loc = time.Local
	}
	localNow := now.In(loc)
	lastAtText := "暂无"
	if parsed, err := storage.ParseSortableTime(lastRunAt); err == nil && !parsed.IsZero() {
		lastAtText = parsed.In(loc).Format("2006-01-02 15:04")
	} else if trimmed := strings.TrimSpace(lastRunAt); trimmed != "" {
		lastAtText = trimmed
	}
	lastSummaryText := strings.TrimSpace(lastRunSummary)
	if lastSummaryText == "" {
		lastSummaryText = "暂无"
	}
	replacer := strings.NewReplacer(
		"{{date}}", localNow.Format("2006-01-02"),
		"{{time}}", localNow.Format("15:04"),
		"{{last_run_at}}", lastAtText,
		"{{last_run_summary}}", lastSummaryText,
	)
	return replacer.Replace(tmpl)
}
