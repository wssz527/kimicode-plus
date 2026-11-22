package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

const (
	reopenSessionPageSize = 100
	reopenSessionMaxPages = 100
	reopenSessionMaxItems = 10000
)

var (
	errReopenSessionsUnknown = errors.New("无法确认所有会话都已停止，Kimi Code 保持打开")
	errReopenSessionsBusy    = &NativeError{Code: "HOST_SESSIONS_BUSY", Message: "有会话正在运行或等待操作。请等会话结束后再重新打开 Kimi Code。"}
)

type reopenSessionItem struct {
	ID                 string          `json:"id"`
	Busy               *bool           `json:"busy"`
	MainTurnActive     *bool           `json:"main_turn_active"`
	PendingInteraction *string         `json:"pending_interaction"`
	Activity           json.RawMessage `json:"activity"`
}

type reopenSessionsPage struct {
	Items         []reopenSessionItem `json:"items"`
	HasMore       *bool               `json:"has_more"`
	NextPageToken string              `json:"next_page_token"`
}

// parseReopenActivity normalizes both the 1.4.2 string activity and the older
// object form. The boolean reports whether an activity field was present.
func parseReopenActivity(raw json.RawMessage) (string, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false, nil
	}
	var activity string
	if err := json.Unmarshal(raw, &activity); err == nil {
		if activity == "" {
			return "", true, errReopenSessionsUnknown
		}
		return activity, true, nil
	}
	var legacy struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &legacy); err != nil || legacy.Status == "" {
		return "", true, errReopenSessionsUnknown
	}
	return legacy.Status, true, nil
}

func validateReopenSessionItem(item reopenSessionItem) error {
	if strings.TrimSpace(item.ID) == "" {
		return errReopenSessionsUnknown
	}
	activity, hasActivity, err := parseReopenActivity(item.Activity)
	if err != nil {
		return err
	}
	if hasActivity && activity != "idle" {
		return errReopenSessionsBusy
	}
	if !hasActivity {
		if item.Busy == nil || item.MainTurnActive == nil {
			return errReopenSessionsUnknown
		}
		if *item.Busy || *item.MainTurnActive {
			return errReopenSessionsBusy
		}
	} else if (item.Busy != nil && *item.Busy) || (item.MainTurnActive != nil && *item.MainTurnActive) {
		return errReopenSessionsBusy
	}
	if item.PendingInteraction != nil {
		pending := *item.PendingInteraction
		switch {
		case pending == "":
			return errReopenSessionsUnknown
		case pending != "none":
			return errReopenSessionsBusy
		}
		return nil
	}
	// The 1.4.2 session list may omit pending_interaction; an explicit idle
	// activity is authoritative there, because anything waiting would surface as
	// a non-idle activity. Legacy records without activity must spell out
	// pending_interaction so an unknown field never silences a wait.
	if hasActivity {
		return nil
	}
	return errReopenSessionsUnknown
}

func validateReopenSessionsPage(raw json.RawMessage) (reopenSessionsPage, error) {
	unknown := errReopenSessionsUnknown
	var page reopenSessionsPage
	if err := json.Unmarshal(raw, &page); err != nil || page.Items == nil || page.HasMore == nil {
		return page, unknown
	}
	if *page.HasMore && page.NextPageToken == "" {
		return page, unknown
	}
	for _, item := range page.Items {
		if err := validateReopenSessionItem(item); err != nil {
			return page, err
		}
	}
	return page, nil
}

// checkReopenSessionsPage validates one page and reports whether another page
// must be requested. A page that claims more data without a usable token is
// treated as unknown because the remaining sessions cannot be inspected.
func checkReopenSessionsPage(raw json.RawMessage) (bool, string, error) {
	page, err := validateReopenSessionsPage(raw)
	if err != nil {
		return false, "", err
	}
	return *page.HasMore, page.NextPageToken, nil
}

// checkReopenSessions keeps the single-page contract used before pagination:
// the caller only knows the list is complete when has_more is false.
func checkReopenSessions(raw json.RawMessage) error {
	page, err := validateReopenSessionsPage(raw)
	if err != nil {
		return err
	}
	if *page.HasMore {
		return errReopenSessionsUnknown
	}
	return nil
}

// reopenSessionsScan walks every page while guarding against token loops and
// unbounded responses. A call site feeds pages in order until hasMore is false.
type reopenSessionsScan struct {
	seenTokens map[string]bool
	pages      int
	items      int
}

func newReopenSessionsScan() *reopenSessionsScan {
	return &reopenSessionsScan{seenTokens: map[string]bool{}}
}

func (s *reopenSessionsScan) checkPage(raw json.RawMessage) (bool, string, error) {
	if s.pages >= reopenSessionMaxPages {
		return false, "", errReopenSessionsUnknown
	}
	page, err := validateReopenSessionsPage(raw)
	if err != nil {
		return false, "", err
	}
	if len(page.Items) > reopenSessionMaxItems-s.items {
		return false, "", errReopenSessionsUnknown
	}
	s.pages++
	s.items += len(page.Items)
	if !*page.HasMore {
		return false, "", nil
	}
	if s.seenTokens[page.NextPageToken] {
		return false, "", errReopenSessionsUnknown
	}
	s.seenTokens[page.NextPageToken] = true
	return true, page.NextPageToken, nil
}
