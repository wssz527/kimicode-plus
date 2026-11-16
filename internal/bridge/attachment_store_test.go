package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/storage"
)

func TestReviewAttachmentPreviewPersistsBytes(t *testing.T) {
	s := testDB(t)
	data := []byte("persist me after preview")
	sum := sha256.Sum256(data)
	task := baseTask(func(p *TaskPackage) {
		p.Attachments = []Attachment{{Name: "x.txt", MIME: "text/plain", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), DataBase64: base64.StdEncoding.EncodeToString(data)}}
	})
	if _, err := s.Preview(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := s.db.QueryRow(`SELECT data FROM attachment_blobs WHERE sha256=?`, hex.EncodeToString(sum[:])).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, data) {
		t.Fatal("preview discarded original bytes")
	}
}

type httpReceiverAdapter struct {
	*desktop.Mock
	url              string
	unauthorizedRead bool
}

func (a *httpReceiverAdapter) Submit(ctx context.Context, s desktop.Submission) (desktop.Receipt, error) {
	if _, err := s.ReadAttachment(ctx, "unapproved-blob"); err != nil {
		a.unauthorizedRead = true
	}
	for _, blob := range s.Attachments {
		data, err := s.ReadAttachment(ctx, blob.Ref)
		if err != nil {
			return desktop.Receipt{}, err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", a.url, bytes.NewReader(data))
		if err != nil {
			return desktop.Receipt{}, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return desktop.Receipt{}, err
		}
		resp.Body.Close()
		if resp.StatusCode != 204 {
			t := desktop.ErrReceiptMismatch
			return desktop.Receipt{}, t
		}
	}
	return a.Mock.Submit(ctx, s)
}

func TestReviewAttachmentRestartScopeAndHTTPReceiver(t *testing.T) {
	data := []byte("actual synthetic file bytes\x00\x01\xff")
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	received := make(chan string, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, data) {
			w.WriteHeader(422)
			return
		}
		h := sha256.Sum256(got)
		received <- hex.EncodeToString(h[:])
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	path := filepath.Join(t.TempDir(), "core.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db, desktop.NewMock(), nil)
	if err != nil {
		t.Fatal(err)
	}
	task := baseTask(func(p *TaskPackage) {
		p.Attachments = []Attachment{{Name: "file.bin", MIME: "application/octet-stream", Size: int64(len(data)), SHA256: hash, DataBase64: base64.StdEncoding.EncodeToString(data)}}
	})
	preview, err := s.Preview(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Task.Attachments[0].BlobRef == "" || preview.Task.Attachments[0].DataBase64 != "" {
		t.Fatal("preview must return stored reference only")
	}
	db.Close()
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adapter := &httpReceiverAdapter{Mock: desktop.NewMock(), url: receiver.URL}
	s, err = New(db, adapter, nil)
	if err != nil {
		t.Fatal(err)
	}
	record, err := s.Submit(context.Background(), SubmitRequest{Intent: "restart-attachment", Task: preview.Task, Confirmed: true, ConfirmationHash: preview.ConfirmationHash})
	if err != nil {
		t.Fatal(err)
	}
	if record.State != StateDelivered || !adapter.unauthorizedRead || len(record.Receipt.Attachments) != 1 || record.Receipt.Attachments[0].SHA256 != hash {
		t.Fatalf("receipt/scope failed %+v", record)
	}
	if got := <-received; got != hash {
		t.Fatal("receiver hash mismatch")
	}
	if n, err := s.collectAttachments(context.Background(), time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("referenced blob collected: %d %v", n, err)
	}
	orphan := Attachment{Name: "orphan", Size: 1, SHA256: func() string { h := sha256.Sum256([]byte("x")); return hex.EncodeToString(h[:]) }()}
	if _, err = s.storeBlob(context.Background(), orphan, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if n, err := s.collectAttachments(context.Background(), time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("orphan not collected %d %v", n, err)
	}
	if _, err = s.storeBlob(context.Background(), Attachment{}, nil); err == nil {
		t.Fatal("empty file silently accepted")
	}
}

func TestReviewWebTargetAndTransportIsolation(t *testing.T) {
	adapter := desktop.NewMock()
	s := newServiceWithSafe(t, adapter, nil)
	ctx := context.Background()
	task := baseTask(func(p *TaskPackage) { p.Direction = "CODE_TO_CHAT" })
	if _, err := s.Submit(ctx, SubmitRequest{Intent: "web-no-target", Task: task, Confirmed: true}); err == nil {
		t.Fatal("missing web target accepted")
	}
	targets, err := s.web.Targets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := targets[0]
	target.Origin = "https://evil.example"
	task.WebTarget = &target
	if _, err := s.Submit(ctx, SubmitRequest{Intent: "web-wrong", Task: task, Confirmed: true}); err == nil {
		t.Fatal("foreign web target accepted")
	}
	task.WebTarget = &targets[0]
	record, err := s.Submit(ctx, SubmitRequest{Intent: "web-bound", Task: task, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := adapter.Counters(); n != 0 {
		t.Fatal("web dispatch used Code transport")
	}
	if record.Receipt.Transport != "MOCK_WEB" {
		t.Fatal("web transport not recorded")
	}
}
