package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

type Blob struct {
	Status string `json:"status"`
	Ref    string `json:"blobRef"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	MIME   string `json:"mime"`
}

type WebTarget struct {
	Site     string `json:"site"`
	Origin   string `json:"origin"`
	TabID    string `json:"tabId"`
	ThreadID string `json:"threadId"`
}

type WebTransport interface {
	Targets(context.Context) ([]WebTarget, error)
	Submit(context.Context, Submission) (Receipt, error)
	Reconcile(context.Context, ReconcileRequest) (Receipt, bool, error)
}

type WebMock struct{ receiver *Mock }

func NewWebMock() *WebMock { return &WebMock{receiver: NewMock()} }
func (m *WebMock) Targets(context.Context) ([]WebTarget, error) {
	return []WebTarget{{Site: "kimi", Origin: "https://www.kimi.com", TabID: "mock-kimi-tab", ThreadID: "mock-kimi-thread"}}, nil
}
func (m *WebMock) Submit(ctx context.Context, s Submission) (Receipt, error) {
	targets, _ := m.Targets(ctx)
	if s.Direction != "CODE_TO_CHAT" || s.WebTarget == nil || *s.WebTarget != targets[0] {
		return Receipt{}, errors.New("web target binding mismatch")
	}
	return m.receiver.Submit(ctx, s)
}
func (m *WebMock) Reconcile(ctx context.Context, r ReconcileRequest) (Receipt, bool, error) {
	return m.receiver.Reconcile(ctx, r)
}

func receiveBlobs(ctx context.Context, s Submission) ([]Blob, error) {
	received := []Blob{}
	for _, blob := range s.Attachments {
		if s.ReadAttachment == nil {
			return nil, errors.New("attachment reader unavailable")
		}
		data, err := s.ReadAttachment(ctx, blob.Ref)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != blob.Size || hex.EncodeToString(sum[:]) != blob.SHA256 {
			return nil, errors.New("receiver attachment digest mismatch")
		}
		received = append(received, blob)
	}
	return received, nil
}
