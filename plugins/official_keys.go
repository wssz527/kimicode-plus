package plugins

import (
	"crypto/ed25519"
	"encoding/base64"
)

const OfficialSigningKeyID = "official-ed25519-2026"

// OfficialSigningPublicKeyBase64 is the Ed25519 public key used by KimiCode+
// clients to verify signed official standalone plugin update catalogs
// (`plugins-manifest.json`). The matching private key is stored only in CI
// release secrets and never committed to the repository.
const OfficialSigningPublicKeyBase64 = "1OMY985RkAdU6D0I83hIVGdUJus0VPDlE/JluqXY0EA="

func OfficialPublicKeys() map[string]ed25519.PublicKey {
	if OfficialSigningPublicKeyBase64 == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(OfficialSigningPublicKeyBase64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil
	}
	return map[string]ed25519.PublicKey{
		OfficialSigningKeyID: ed25519.PublicKey(raw),
	}
}
