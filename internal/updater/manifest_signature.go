package updater

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
)

const releaseManifestDomain = "kimicode-plus-release-manifest-v1\n"

type manifestSignature struct {
	KeyID string `json:"keyId"`
	Value string `json:"value"`
}

func unpackManifest(body []byte) ([]map[string]json.RawMessage, json.RawMessage, []byte, error) {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(body, &entries); err != nil || len(entries) == 0 || entries[0] == nil {
		return nil, nil, nil, errors.New("更新服务返回了无效的发布清单")
	}
	signature := entries[0]["signature"]
	delete(entries[0], "signature")
	for _, entry := range entries[1:] {
		if entry == nil || entry["signature"] != nil {
			return nil, nil, nil, errors.New("发布清单签名位置无效")
		}
	}
	payload, err := json.Marshal(entries)
	return entries, signature, payload, err
}

// SignReleaseManifest embeds an Ed25519 signature while retaining the array
// shape that earlier clients and the mirror publisher expect.
func SignReleaseManifest(body []byte, keyID string, key ed25519.PrivateKey) ([]byte, error) {
	if keyID == "" || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid manifest signing key")
	}
	entries, previous, payload, err := unpackManifest(body)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		return nil, errors.New("manifest already contains a signature")
	}
	sig := ed25519.Sign(key, append([]byte(releaseManifestDomain), payload...))
	entries[0]["signature"], err = json.Marshal(manifestSignature{KeyID: keyID, Value: base64.StdEncoding.EncodeToString(sig)})
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(entries, "", "  ")
}

func verifyReleaseManifest(body []byte, keys map[string]ed25519.PublicKey, require bool) ([]byte, bool, error) {
	_, raw, payload, err := unpackManifest(body)
	if err != nil {
		return nil, false, err
	}
	if raw == nil && !require {
		return payload, false, nil
	}
	var signature manifestSignature
	if len(raw) == 0 || json.Unmarshal(raw, &signature) != nil || signature.KeyID == "" || signature.Value == "" {
		return nil, false, errors.New("发布清单缺少有效的 Ed25519 签名")
	}
	key := keys[signature.KeyID]
	value, decodeErr := base64.StdEncoding.DecodeString(signature.Value)
	if decodeErr != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, append([]byte(releaseManifestDomain), payload...), value) {
		return nil, false, errors.New("发布清单 Ed25519 签名校验失败")
	}
	return payload, true, nil
}
