package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/storage"
)

func TestIndependentChatAndIntentPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	request := func(method, path, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := request("POST", "/enhance/v1/chat/threads", `{"title":"独立讨论","sourceId":"custom-model","model":"synthetic"}`, "")
	if w.Code != 201 {
		t.Fatal(w.Body)
	}
	var thread Thread
	json.Unmarshal(w.Body.Bytes(), &thread)
	pathAPI := "/enhance/v1/chat/threads/" + thread.ID + "/messages"
	for i := 0; i < 2; i++ {
		w = request("POST", pathAPI, `{"text":"selected message"}`, "intent-one")
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	json.Unmarshal(w.Body.Bytes(), &thread)
	if len(thread.Messages) != 2 || !strings.Contains(thread.Messages[1].Text, "未调用任何模型") {
		t.Fatal("mock reply or dedup incorrect")
	}
	if request("POST", pathAPI, `{"text":"changed"}`, "intent-one").Code != 409 {
		t.Fatal("payload conflict ignored")
	}
	db.Close()
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err = New(db)
	if err != nil {
		t.Fatal(err)
	}
	mux = http.NewServeMux()
	s.Register(mux)
	w = request("GET", "/enhance/v1/chat/threads/"+thread.ID, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	json.Unmarshal(w.Body.Bytes(), &thread)
	if len(thread.Messages) != 2 {
		t.Fatal("history not durable")
	}
	for _, body := range []string{`{"id":"evil","name":"bad","url":"javascript:alert(1)"}`, `{"id":"key","name":"bad","url":"https://user:secret@example.com"}`, `{"id":"query","name":"bad","url":"https://example.com/?token=secret"}`} {
		if request("POST", "/enhance/v1/chat/sources", body, "").Code != 422 {
			t.Fatal("unsafe chat URL accepted")
		}
	}
}
