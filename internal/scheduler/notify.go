package scheduler

import (
	"fmt"
	"strings"
)

// BuildNotification filters scheduler run outcomes so only events the official
// client does not already cover trigger a notification: run failures, pre-run
// validation failures, and skipped runs (excluding user/plugin cancellations).
// Turn completions, pending questions, and pending approvals are handled by the
// official client and are never duplicated here.
func BuildNotification(run RunRecord) (NotificationEvent, bool) {
	taskName := strings.TrimSpace(run.TaskName)
	if taskName == "" {
		taskName = "定时任务"
	}
	msg := strings.TrimSpace(run.Message)
	switch run.Status {
	case RunFailed:
		if run.FailureKind == FailurePreflight {
			if msg == "" {
				msg = "运行前校验未通过，请打开运行记录查看详情。"
			}
			return NotificationEvent{
				RunID:       run.ID,
				TaskID:      run.TaskID,
				TaskName:    taskName,
				SessionID:   run.SessionID,
				EventKind:   NotifyPreflightFailed,
				FailureKind: run.FailureKind,
				Title:       "KimiCode+ 定时任务 · 运行前校验失败",
				Body:        fmt.Sprintf("任务「%s」：%s", taskName, msg),
				Tag:         "ekd-sched-" + run.ID,
			}, true
		}
		if msg == "" {
			msg = "任务执行失败，请打开查看详情。"
		}
		return NotificationEvent{
			RunID:       run.ID,
			TaskID:      run.TaskID,
			TaskName:    taskName,
			SessionID:   run.SessionID,
			EventKind:   NotifyRunFailed,
			FailureKind: run.FailureKind,
			Title:       "KimiCode+ 定时任务 · 运行失败",
			Body:        fmt.Sprintf("任务「%s」：%s", taskName, msg),
			Tag:         "ekd-sched-" + run.ID,
		}, true

	case RunSkipped:
		if run.FailureKind == FailureCancelled {
			return NotificationEvent{}, false
		}
		if msg == "" {
			msg = "本次调度已被跳过。"
		}
		return NotificationEvent{
			RunID:       run.ID,
			TaskID:      run.TaskID,
			TaskName:    taskName,
			SessionID:   run.SessionID,
			EventKind:   NotifyRunSkipped,
			FailureKind: run.FailureKind,
			Title:       "KimiCode+ 定时任务 · 已跳过",
			Body:        fmt.Sprintf("任务「%s」：%s", taskName, msg),
			Tag:         "ekd-sched-" + run.ID,
		}, true

	default:
		// RunCompleted, RunAwaitingApproval, RunQueued, RunRunning are never notified here.
		return NotificationEvent{}, false
	}
}
