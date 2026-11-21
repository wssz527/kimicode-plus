package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func reopenBusy(err error) bool {
	var native *NativeError
	return errors.As(err, &native) && native.Code == "HOST_SESSIONS_BUSY"
}

func TestReopenSessionActivity(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		allow     bool
		busy      bool
	}{
		{"empty", `{"items":[],"has_more":false}`, true, false},
		{"legacy idle", `{"items":[{"id":"s","busy":false,"main_turn_active":false,"pending_interaction":"none"}],"has_more":false}`, true, false},
		{"legacy busy", `{"items":[{"id":"s","busy":true,"main_turn_active":false,"pending_interaction":"none"}],"has_more":false}`, false, true},
		{"legacy turn", `{"items":[{"id":"s","busy":false,"main_turn_active":true,"pending_interaction":"none"}],"has_more":false}`, false, true},
		{"legacy approval", `{"items":[{"id":"s","busy":false,"main_turn_active":false,"pending_interaction":"approval"}],"has_more":false}`, false, true},
		{"legacy empty pending", `{"items":[{"id":"s","busy":false,"main_turn_active":false,"pending_interaction":""}],"has_more":false}`, false, false},
		{"another page", `{"items":[],"has_more":true}`, false, false},
		{"unknown pagination", `{"items":[]}`, false, false},
		{"missing activity", `{"items":[{"id":"s","busy":false}],"has_more":false}`, false, false},
		{"missing id", `{"items":[{"busy":false,"main_turn_active":false,"pending_interaction":"none"}],"has_more":false}`, false, false},
		{"malformed", `{"items":`, false, false},
		{"activity idle", `{"items":[{"id":"s","activity":"idle","pending_interaction":"none"}],"has_more":false}`, true, false},
		{"activity idle without pending", `{"items":[{"id":"s","activity":"idle"}],"has_more":false}`, true, false},
		{"activity turn", `{"items":[{"id":"s","activity":"turn","pending_interaction":"none"}],"has_more":false}`, false, true},
		{"activity waiting", `{"items":[{"id":"s","activity":"waiting"}],"has_more":false}`, false, true},
		{"activity running", `{"items":[{"id":"s","activity":"running"}],"has_more":false}`, false, true},
		{"activity empty", `{"items":[{"id":"s","activity":"","pending_interaction":"none"}],"has_more":false}`, false, false},
		{"activity object idle", `{"items":[{"id":"s","activity":{"status":"idle"},"pending_interaction":"none"}],"has_more":false}`, true, false},
		{"activity object turn", `{"items":[{"id":"s","activity":{"status":"turn"},"pending_interaction":"none"}],"has_more":false}`, false, true},
		{"activity object empty", `{"items":[{"id":"s","activity":{},"pending_interaction":"none"}],"has_more":false}`, false, false},
		{"activity wrong type", `{"items":[{"id":"s","activity":7,"pending_interaction":"none"}],"has_more":false}`, false, false},
		{"activity idle with legacy busy", `{"items":[{"id":"s","activity":"idle","busy":true,"pending_interaction":"none"}],"has_more":false}`, false, true},
		{"activity idle with pending approval", `{"items":[{"id":"s","activity":"idle","pending_interaction":"approval"}],"has_more":false}`, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := checkReopenSessions(json.RawMessage(tt.raw))
			if (err == nil) != tt.allow || reopenBusy(err) != tt.busy {
				t.Fatalf("unexpected reopen decision: %v", err)
			}
		})
	}
}

func TestReopenSessionsPageReportsPagination(t *testing.T) {
	hasMore, token, err := checkReopenSessionsPage(json.RawMessage(`{"items":[{"id":"s","activity":"idle"}],"has_more":true,"next_page_token":"p2"}`))
	if err != nil || !hasMore || token != "p2" {
		t.Fatalf("pagination not reported: %v %q %v", hasMore, token, err)
	}
	hasMore, token, err = checkReopenSessionsPage(json.RawMessage(`{"items":[],"has_more":false}`))
	if err != nil || hasMore || token != "" {
		t.Fatalf("final page not reported: %v %q %v", hasMore, token, err)
	}
}

func TestReopenSessionsScanWalksEveryPage(t *testing.T) {
	const page1 = `{"items":[{"id":"a","activity":"idle"},{"id":"b","activity":"idle"}],"has_more":true,"next_page_token":"t2"}`
	const page2 = `{"items":[{"id":"c","activity":"idle"}],"has_more":true,"next_page_token":"t3"}`
	const page3 = `{"items":[{"id":"d","activity":"idle"}],"has_more":false}`
	scan := newReopenSessionsScan()
	for i, raw := range []string{page1, page2, page3} {
		hasMore, token, err := scan.checkPage(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("page %d rejected: %v", i+1, err)
		}
		if want := i < 2; hasMore != want {
			t.Fatalf("page %d hasMore = %v", i+1, hasMore)
		}
		if hasMore && token == "" {
			t.Fatalf("page %d lost its token", i+1)
		}
	}
	if scan.pages != 3 || scan.items != 4 {
		t.Fatalf("scan counters: pages=%d items=%d", scan.pages, scan.items)
	}
}

func TestReopenSessionsScanStopsOnBusyLaterPage(t *testing.T) {
	const page1 = `{"items":[{"id":"a","activity":"idle"}],"has_more":true,"next_page_token":"t2"}`
	const page2 = `{"items":[{"id":"b","activity":"turn"}],"has_more":false}`
	scan := newReopenSessionsScan()
	if _, _, err := scan.checkPage(json.RawMessage(page1)); err != nil {
		t.Fatal(err)
	}
	_, _, err := scan.checkPage(json.RawMessage(page2))
	if !reopenBusy(err) {
		t.Fatalf("busy second page reported %v", err)
	}
}

func TestReopenSessionsScanRejectsBrokenPagination(t *testing.T) {
	for _, tt := range []struct {
		name  string
		pages []string
	}{
		{"missing token", []string{`{"items":[],"has_more":true}`}},
		{"missing has_more", []string{`{"items":[]}`}},
		{"nil items", []string{`{"has_more":false}`}},
		{"malformed", []string{`{"items":[]`}},
		{"token loop", []string{
			`{"items":[{"id":"a","activity":"idle"}],"has_more":true,"next_page_token":"same"}`,
			`{"items":[{"id":"b","activity":"idle"}],"has_more":true,"next_page_token":"same"}`,
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scan := newReopenSessionsScan()
			var err error
			for _, raw := range tt.pages {
				if _, _, err = scan.checkPage(json.RawMessage(raw)); err != nil {
					break
				}
			}
			if err == nil || reopenBusy(err) {
				t.Fatalf("broken pagination accepted: %v", err)
			}
		})
	}
}

func TestReopenSessionsScanWalksFullPagedHistory(t *testing.T) {
	item := `{"id":"s%d","activity":"idle"}`
	page := func(start, count int, more bool, token string) string {
		entries := make([]string, 0, count)
		for i := 0; i < count; i++ {
			entries = append(entries, fmt.Sprintf(item, start+i))
		}
		raw := fmt.Sprintf(`{"items":[%s],"has_more":%t`, strings.Join(entries, ","), more)
		if token != "" {
			raw += fmt.Sprintf(`,"next_page_token":%q`, token)
		}
		return raw + "}"
	}
	scan := newReopenSessionsScan()
	pages := []string{
		page(0, 100, true, "p2"),
		page(100, 100, true, "p3"),
		page(200, 37, false, ""),
	}
	for i, raw := range pages {
		hasMore, _, err := scan.checkPage(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("page %d rejected: %v", i+1, err)
		}
		if want := i < len(pages)-1; hasMore != want {
			t.Fatalf("page %d hasMore = %v", i+1, hasMore)
		}
	}
	if scan.pages != 3 || scan.items != 237 {
		t.Fatalf("scan counters: pages=%d items=%d", scan.pages, scan.items)
	}

	busy := page(100, 100, true, "p3")
	busy = strings.Replace(busy, `"s100","activity":"idle"`, `"s100","activity":"waiting"`, 1)
	scan = newReopenSessionsScan()
	if _, _, err := scan.checkPage(json.RawMessage(pages[0])); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scan.checkPage(json.RawMessage(busy)); !reopenBusy(err) {
		t.Fatalf("busy page in a large history reported %v", err)
	}
}

func TestReopenSessionsScanCapsPagesAndItems(t *testing.T) {
	scan := newReopenSessionsScan()
	for i := 0; i < reopenSessionMaxPages; i++ {
		raw := fmt.Sprintf(`{"items":[],"has_more":true,"next_page_token":"t%d"}`, i)
		if _, _, err := scan.checkPage(json.RawMessage(raw)); err != nil {
			t.Fatalf("page %d rejected before the cap: %v", i+1, err)
		}
	}
	if _, _, err := scan.checkPage(json.RawMessage(`{"items":[],"has_more":false}`)); err == nil || reopenBusy(err) {
		t.Fatalf("page cap was not enforced: %v", err)
	}

	item := `{"id":"s","activity":"idle"}`
	tooMany := fmt.Sprintf(`{"items":[%s],"has_more":false}`, strings.Repeat(item+",", reopenSessionMaxItems)+item)
	if _, _, err := newReopenSessionsScan().checkPage(json.RawMessage(tooMany)); err == nil || reopenBusy(err) {
		t.Fatalf("item cap was not enforced: %v", err)
	}
	withinBudget := fmt.Sprintf(`{"items":[%s],"has_more":false}`, strings.Repeat(item+",", reopenSessionMaxItems-2)+item)
	if _, _, err := newReopenSessionsScan().checkPage(json.RawMessage(withinBudget)); err != nil {
		t.Fatalf("items within the cap were rejected: %v", err)
	}
}
