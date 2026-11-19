package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

func TestWindowsBridgeSessionAndRPCDispatch(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	host := "127.0.0.1:18444"
	guard := security.New(host)
	app, err := NewWithNative(db, guard, false, "", NativeOptions{Home: t.TempDir(), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	// 1. Pair to get session token with adapter:write.
	pairBody, _ := json.Marshal(map[string]any{
		"pairingToken": guard.PairingToken(),
		"permissions":  []string{"adapter:write"},
	})
	pairReq := httptest.NewRequest(http.MethodPost, "http://"+host+"/enhance/v1/pair", bytes.NewReader(pairBody))
	pairReq.Host = host
	pairReq.Header.Set("Content-Type", "application/json")
	pairRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(pairRec, pairReq)
	if pairRec.Code != http.StatusOK {
		t.Fatalf("pair failed: %d %s", pairRec.Code, pairRec.Body.String())
	}
	var pairRes struct {
		SessionToken string `json:"sessionToken"`
	}
	_ = json.Unmarshal(pairRec.Body.Bytes(), &pairRes)

	// 2. Mint Windows bridgeKey.
	mintReq := httptest.NewRequest(http.MethodPost, "http://"+host+"/enhance/v1/desktop/bridge-session", bytes.NewReader([]byte(`{}`)))
	mintReq.Host = host
	mintReq.Header.Set("Content-Type", "application/json")
	mintReq.Header.Set("Authorization", "Bearer "+pairRes.SessionToken)
	mintRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(mintRec, mintReq)
	if mintRec.Code != http.StatusOK {
		t.Fatalf("bridge-session failed: %d %s", mintRec.Code, mintRec.Body.String())
	}
	var mintRes struct {
		BridgeKey string `json:"bridgeKey"`
	}
	_ = json.Unmarshal(mintRec.Body.Bytes(), &mintRes)
	if len(mintRes.BridgeKey) < 32 {
		t.Fatalf("bridge key too short: %q", mintRes.BridgeKey)
	}

	// 3. Untrusted origin must be rejected on both preflight and POST.
	badOpt := httptest.NewRequest(http.MethodOptions, "http://"+host+"/ekd/native/v1", nil)
	badOpt.Host = host
	badOpt.Header.Set("Origin", "https://evil.example")
	badOptRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(badOptRec, badOpt)
	if badOptRec.Code != http.StatusForbidden {
		t.Fatalf("evil origin preflight accepted: %d", badOptRec.Code)
	}

	// 4. Trusted app://renderer preflight succeeds with PNA + CORS headers.
	goodOpt := httptest.NewRequest(http.MethodOptions, "http://"+host+"/ekd/native/v1", nil)
	goodOpt.Host = host
	goodOpt.Header.Set("Origin", "app://renderer")
	goodOptRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(goodOptRec, goodOpt)
	if goodOptRec.Code != http.StatusNoContent || goodOptRec.Header().Get("Access-Control-Allow-Origin") != "app://renderer" {
		t.Fatalf("renderer preflight failed: %d %+v", goodOptRec.Code, goodOptRec.Header())
	}

	// 5. Missing/invalid bridge key must be rejected.
	badKeyReq := httptest.NewRequest(http.MethodPost, "http://"+host+"/ekd/native/v1", bytes.NewReader([]byte(`{"protocol":1,"id":"1","action":"attach.status"}`)))
	badKeyReq.Host = host
	badKeyReq.Header.Set("Origin", "app://renderer")
	badKeyReq.Header.Set("Content-Type", "application/json")
	badKeyReq.Header.Set("X-EKD-Bridge", "invalid-key")
	badKeyRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(badKeyRec, badKeyReq)
	if badKeyRec.Code != http.StatusForbidden {
		t.Fatalf("invalid bridge key accepted: %d", badKeyRec.Code)
	}

	// 6. Valid bridge POST for core.fetch reaches /enhance/v1/settings using server-held token.
	rpcReq := httptest.NewRequest(http.MethodPost, "http://"+host+"/ekd/native/v1", bytes.NewReader([]byte(`{"protocol":1,"id":"req-1","action":"core.fetch","payload":{"path":"/enhance/v1/settings","method":"GET"}}`)))
	rpcReq.Host = host
	rpcReq.Header.Set("Origin", "app://renderer")
	rpcReq.Header.Set("Content-Type", "application/json")
	rpcReq.Header.Set("X-EKD-Bridge", mintRes.BridgeKey)
	rpcRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rpcRec, rpcReq)
	if rpcRec.Code != http.StatusOK {
		t.Fatalf("bridge core.fetch failed: %d %s", rpcRec.Code, rpcRec.Body.String())
	}
	var rpcRes struct {
		Protocol int    `json:"protocol"`
		ID       string `json:"id"`
		Result   struct {
			Status     int    `json:"status"`
			BodyBase64 string `json:"bodyBase64"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rpcRec.Body.Bytes(), &rpcRes); err != nil {
		t.Fatal(err)
	}
	if rpcRes.Error != nil || rpcRes.ID != "req-1" || rpcRes.Result.Status != 200 {
		t.Fatalf("unexpected core.fetch RPC response: %+v", rpcRes)
	}
	decoded, err := base64.StdEncoding.DecodeString(rpcRes.Result.BodyBase64)
	if err != nil || !bytes.Contains(decoded, []byte(`"modules"`)) {
		t.Fatalf("unexpected core.fetch body: %q err=%v", string(decoded), err)
	}

	// 7. Native action dispatch (attach.status) works transparently over the bridge.
	actReq := httptest.NewRequest(http.MethodPost, "http://"+host+"/ekd/native/v1", bytes.NewReader([]byte(`{"protocol":1,"id":"req-2","action":"attach.status","payload":{}}`)))
	actReq.Host = host
	actReq.Header.Set("Origin", "app://renderer")
	actReq.Header.Set("Content-Type", "application/json")
	actReq.Header.Set("X-EKD-Bridge", mintRes.BridgeKey)
	actRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(actRec, actReq)
	if actRec.Code != http.StatusOK || !bytes.Contains(actRec.Body.Bytes(), []byte(`"available"`)) {
		t.Fatalf("bridge attach.status failed: %d %s", actRec.Code, actRec.Body.String())
	}
}
