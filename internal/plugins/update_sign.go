package plugins

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	PluginManifestFormat    = "kimicode-plus.plugins-manifest.v1"
	PluginManifestDomain    = "kimicode-plus-plugins-manifest-v1\n"
	DefaultOfficialKeyID    = "official-ed25519-2026"
	DefaultPluginReleaseTag = "plugins-latest"
	PluginManifestAssetName = "plugins-manifest.json"
	maxPluginManifestBytes  = 2 << 20
)

var (
	sha256HexPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	archiveZipPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}\.zip$`)
	coreSemverPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
)

// SignedPluginManifest wraps the signed official plugin catalog in a single
// atomic file. The Ed25519 signature covers PluginManifestDomain concatenated
// with the exact raw UTF-8 bytes of Manifest.
type SignedPluginManifest struct {
	Format    string          `json:"format"`
	KeyID     string          `json:"keyId"`
	Signature string          `json:"signature"`
	Manifest  json.RawMessage `json:"manifest"`
}

// OfficialPluginCatalog is the signed payload listing every official plugin
// available for independent online updates.
type OfficialPluginCatalog struct {
	GeneratedAt string                `json:"generatedAt"`
	Plugins     []OfficialPluginEntry `json:"plugins"`
}

// OfficialPluginEntry describes one signed official plugin release package.
type OfficialPluginEntry struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	APIVersion     int      `json:"apiVersion"`
	Description    string   `json:"description,omitempty"`
	HostVersions   []string `json:"hostVersions"`
	MinCoreVersion string   `json:"minCoreVersion,omitempty"`
	Capabilities   []string `json:"capabilities"`
	Archive        string   `json:"archive"`
	ArchiveBytes   int64    `json:"archiveBytes"`
	ArchiveSHA256  string   `json:"archiveSha256"`
	ContentDigest  string   `json:"contentDigest"`
	PublishedAt    string   `json:"publishedAt,omitempty"`
	ReleaseNotes   string   `json:"releaseNotes,omitempty"`
}

func (c OfficialPluginCatalog) Find(id string) (OfficialPluginEntry, bool) {
	for _, entry := range c.Plugins {
		if entry.ID == id {
			return entry, true
		}
	}
	return OfficialPluginEntry{}, false
}

// ParseOfficialPublicKey decodes a base64-encoded 32-byte Ed25519 public key.
func ParseOfficialPublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("invalid ed25519 public key encoding: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid ed25519 public key length: %d", len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// ParseOfficialPrivateKey decodes either a 32-byte Ed25519 seed or a 64-byte
// Ed25519 private key encoded in base64.
func ParseOfficialPrivateKey(encoded string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("invalid ed25519 private key encoding: %w", err)
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	default:
		return nil, fmt.Errorf("invalid ed25519 private key length: %d", len(raw))
	}
}

func manifestSigningMessage(rawManifest []byte) []byte {
	msg := make([]byte, len(PluginManifestDomain)+len(rawManifest))
	copy(msg, PluginManifestDomain)
	copy(msg[len(PluginManifestDomain):], rawManifest)
	return msg
}

// SignPluginCatalog validates and signs an OfficialPluginCatalog with Ed25519.
// The embedded manifest JSON is compacted before signing and spliced verbatim
// into the envelope so verification hashes the exact transmitted bytes.
func SignPluginCatalog(catalog OfficialPluginCatalog, keyID string, privateKey ed25519.PrivateKey) ([]byte, error) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return nil, errors.New("keyId is required")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid ed25519 private key")
	}
	if err := validateCatalog(catalog); err != nil {
		return nil, err
	}
	manifestBytes, err := json.Marshal(catalog)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(privateKey, manifestSigningMessage(manifestBytes))
	envelope := SignedPluginManifest{
		Format:    PluginManifestFormat,
		KeyID:     keyID,
		Signature: base64.StdEncoding.EncodeToString(sig),
		Manifest:  json.RawMessage(manifestBytes),
	}
	var buf bytes.Buffer
	buf.WriteString("{\n")
	formatJSON, _ := json.Marshal(envelope.Format)
	keyIDJSON, _ := json.Marshal(envelope.KeyID)
	sigJSON, _ := json.Marshal(envelope.Signature)
	fmt.Fprintf(&buf, "  \"format\": %s,\n", formatJSON)
	fmt.Fprintf(&buf, "  \"keyId\": %s,\n", keyIDJSON)
	fmt.Fprintf(&buf, "  \"signature\": %s,\n", sigJSON)
	buf.WriteString("  \"manifest\": ")
	buf.Write(manifestBytes)
	buf.WriteString("\n}\n")
	out := buf.Bytes()
	pub := privateKey.Public().(ed25519.PublicKey)
	if _, err := VerifySignedPluginManifest(out, map[string]ed25519.PublicKey{keyID: pub}); err != nil {
		return nil, fmt.Errorf("self-verify signed plugin manifest: %w", err)
	}
	return out, nil
}

// VerifySignedPluginManifest validates the envelope, verifies the Ed25519
// signature against the trusted key set, and checks every catalog entry.
func VerifySignedPluginManifest(raw []byte, keys map[string]ed25519.PublicKey) (OfficialPluginCatalog, error) {
	if len(raw) == 0 || len(raw) > maxPluginManifestBytes {
		return OfficialPluginCatalog{}, errors.New("插件更新清单为空或超过大小限制")
	}
	if len(keys) == 0 {
		return OfficialPluginCatalog{}, errors.New("未配置官方插件签名公钥，无法验证插件更新")
	}
	var env SignedPluginManifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&env); err != nil {
		return OfficialPluginCatalog{}, errors.New("插件更新清单格式无效")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return OfficialPluginCatalog{}, errors.New("插件更新清单包含多余内容")
	}
	if env.Format != PluginManifestFormat {
		return OfficialPluginCatalog{}, errors.New("不支持的插件更新清单格式")
	}
	pub, ok := keys[strings.TrimSpace(env.KeyID)]
	if !ok || len(pub) != ed25519.PublicKeySize {
		return OfficialPluginCatalog{}, errors.New("插件更新清单签名密钥不受信任")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(env.Signature))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return OfficialPluginCatalog{}, errors.New("插件更新清单签名格式无效")
	}
	if len(env.Manifest) == 0 {
		return OfficialPluginCatalog{}, errors.New("插件更新清单缺少内容")
	}
	if !ed25519.Verify(pub, manifestSigningMessage(env.Manifest), sig) {
		return OfficialPluginCatalog{}, errors.New("插件更新清单签名校验失败，已拒绝")
	}
	var catalog OfficialPluginCatalog
	if err := json.Unmarshal(env.Manifest, &catalog); err != nil {
		return OfficialPluginCatalog{}, errors.New("插件更新清单内容无法解析")
	}
	if err := validateCatalog(catalog); err != nil {
		return OfficialPluginCatalog{}, err
	}
	return catalog, nil
}

func validateCatalog(catalog OfficialPluginCatalog) error {
	seen := make(map[string]bool, len(catalog.Plugins))
	for _, entry := range catalog.Plugins {
		if !idPattern.MatchString(entry.ID) {
			return fmt.Errorf("插件更新清单包含无效插件 ID：%s", entry.ID)
		}
		if seen[entry.ID] {
			return fmt.Errorf("插件更新清单包含重复插件 ID：%s", entry.ID)
		}
		seen[entry.ID] = true
		if !versionPattern.MatchString(entry.Version) {
			return fmt.Errorf("插件 %s 版本号格式无效：%s", entry.ID, entry.Version)
		}
		if entry.APIVersion < 1 {
			return fmt.Errorf("插件 %s apiVersion 无效", entry.ID)
		}
		if !archiveZipPattern.MatchString(entry.Archive) || strings.Contains(entry.Archive, "..") {
			return fmt.Errorf("插件 %s 压缩包文件名无效：%s", entry.ID, entry.Archive)
		}
		if entry.ArchiveBytes <= 0 || entry.ArchiveBytes > zipLimit {
			return fmt.Errorf("插件 %s 压缩包大小超出限制", entry.ID)
		}
		if !sha256HexPattern.MatchString(entry.ArchiveSHA256) {
			return fmt.Errorf("插件 %s 压缩包校验值无效", entry.ID)
		}
		if _, err := hex.DecodeString(entry.ContentDigest); err != nil || !sha256HexPattern.MatchString(entry.ContentDigest) {
			return fmt.Errorf("插件 %s 内容摘要无效", entry.ID)
		}
		if entry.MinCoreVersion != "" && !coreSemverPattern.MatchString(entry.MinCoreVersion) {
			return fmt.Errorf("插件 %s 最低核心版本格式无效：%s", entry.ID, entry.MinCoreVersion)
		}
		if _, err := ParseCapabilities(entry.Capabilities); err != nil {
			return fmt.Errorf("插件 %s 能力声明无效：%w", entry.ID, err)
		}
		for _, pattern := range entry.HostVersions {
			if !hostPattern.MatchString(strings.TrimSpace(pattern)) {
				return fmt.Errorf("插件 %s 适用宿主版本格式无效：%s", entry.ID, pattern)
			}
		}
	}
	return nil
}

type coreSemver struct {
	numbers [3]string
	pre     []string
}

func parseCoreSemver(value string) (coreSemver, bool) {
	parts := coreSemverPattern.FindStringSubmatch(strings.TrimSpace(value))
	if parts == nil {
		return coreSemver{}, false
	}
	v := coreSemver{numbers: [3]string{parts[1], parts[2], parts[3]}}
	if parts[4] != "" {
		v.pre = strings.Split(parts[4], ".")
		for _, id := range v.pre {
			if len(id) > 1 && id[0] == '0' && strings.Trim(id, "0123456789") == "" {
				return coreSemver{}, false
			}
		}
	}
	return v, true
}

func compareCoreSemver(left, right string) (int, bool) {
	a, okA := parseCoreSemver(left)
	b, okB := parseCoreSemver(right)
	if !okA || !okB {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if len(a.numbers[i]) != len(b.numbers[i]) {
			if len(a.numbers[i]) < len(b.numbers[i]) {
				return -1, true
			}
			return 1, true
		}
		if cmp := strings.Compare(a.numbers[i], b.numbers[i]); cmp != 0 {
			return cmp, true
		}
	}
	if len(a.pre) == 0 || len(b.pre) == 0 {
		switch {
		case len(a.pre) == 0 && len(b.pre) == 0:
			return 0, true
		case len(a.pre) == 0:
			return 1, true
		default:
			return -1, true
		}
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		pa, pb := a.pre[i], b.pre[i]
		numA := strings.Trim(pa, "0123456789") == ""
		numB := strings.Trim(pb, "0123456789") == ""
		switch {
		case numA && numB:
			if len(pa) != len(pb) {
				if len(pa) < len(pb) {
					return -1, true
				}
				return 1, true
			}
			if cmp := strings.Compare(pa, pb); cmp != 0 {
				return cmp, true
			}
		case numA:
			return -1, true
		case numB:
			return 1, true
		default:
			if cmp := strings.Compare(pa, pb); cmp != 0 {
				return cmp, true
			}
		}
	}
	switch {
	case len(a.pre) < len(b.pre):
		return -1, true
	case len(a.pre) > len(b.pre):
		return 1, true
	default:
		return 0, true
	}
}
