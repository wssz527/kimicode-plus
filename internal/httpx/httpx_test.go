package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBoundaryRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	for _, body := range []string{`{`, `{"known":"ok","unknown":true}`, `{"known":"ok"} {"known":"second"}`, strings.Repeat(" ", 2<<20) + `{"known":"oversized"}`} {
		var v struct {
			Known string `json:"known"`
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		if Decode(w, r, &v) || w.Code != 400 {
			t.Fatal("malformed boundary input accepted")
		}
	}
	var v struct {
		Known string `json:"known"`
	}
	if !Decode(httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(`{"known":"valid"}`)), &v) || v.Known != "valid" {
		t.Fatal("valid JSON rejected")
	}
}
