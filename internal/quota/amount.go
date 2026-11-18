package quota

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxAmount     = 1e12
	maxLabelChars = 40
	maxURLChars   = 2048
	maxJSONDepth  = 6
)

var (
	controlPattern    = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	numericString     = regexp.MustCompile(`^-?\d{1,15}(?:\.\d{1,6})?$`)
	secretShaped      = regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_-]{8,}`)
	isoInstant        = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}`)
	currencyPattern   = regexp.MustCompile(`^[A-Z]{3}$`)
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@+/-]{0,79}$`)
)

// customUnits are the unit strings a formatted balance may carry.
var customUnits = map[string]bool{"CNY": true, "USD": true, "EUR": true, "JPY": true, "HKD": true,
	"GBP": true, "credits": true, "requests": true, "tokens": true}

// SourceError is a validation failure a user can act on.
type SourceError struct {
	Code    string
	Message string
}

// Error implements error.
func (e *SourceError) Error() string { return e.Code }

func sourceError(code, message string) *SourceError {
	return &SourceError{Code: code, Message: message}
}

func isControl(value string) bool { return controlPattern.MatchString(value) }

func round6(value float64) float64 { return math.Round(value*1e6) / 1e6 }

// ToAmount reads an amount from a number or a numeric string. Everything else
// is "unknown", which is not 0. DeepSeek returns its amounts as strings, so this
// conversion is load-bearing rather than defensive.
func ToAmount(value any) *float64 {
	switch item := value.(type) {
	case float64:
		if math.IsNaN(item) || math.IsInf(item, 0) || math.Abs(item) > maxAmount {
			return nil
		}
		result := round6(item)
		return &result
	case int64:
		result := float64(item)
		return &result
	case int:
		result := float64(item)
		return &result
	case json.Number:
		parsed, err := strconv.ParseFloat(item.String(), 64)
		if err != nil || math.Abs(parsed) > maxAmount {
			return nil
		}
		result := round6(parsed)
		return &result
	case string:
		trimmed := strings.TrimSpace(item)
		if !numericString.MatchString(trimmed) {
			return nil
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || math.IsInf(parsed, 0) || math.Abs(parsed) > maxAmount {
			return nil
		}
		result := round6(parsed)
		return &result
	}
	return nil
}

// ToTimestamp reads a reset time from an epoch (seconds or milliseconds) or an
// ISO-8601 instant. A bare date, a locale string or a duration is refused rather
// than guessed at.
func ToTimestamp(value any) *int64 {
	switch item := value.(type) {
	case float64:
		if math.IsNaN(item) || item <= 0 {
			return nil
		}
		ms := item
		if ms <= 1e11 {
			ms *= 1000
		}
		if ms >= 8.64e15 {
			return nil
		}
		result := int64(ms)
		return &result
	case int64:
		if item <= 0 {
			return nil
		}
		ms := item
		if ms <= 1e11 {
			ms *= 1000
		}
		if ms >= 8.64e15 {
			return nil
		}
		return &ms
	case string:
		trimmed := strings.TrimSpace(item)
		if !isoInstant.MatchString(trimmed) {
			return nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, trimmed)
		if err != nil {
			parsed, err = time.Parse("2006-01-02T15:04:05Z07:00", trimmed)
		}
		if err != nil {
			return nil
		}
		ms := parsed.UnixMilli()
		if ms <= 0 || ms >= 8.64e15 {
			return nil
		}
		return &ms
	}
	return nil
}

// AccountTag is a stable, non-secret tag for one account. The credential itself
// never leaves this function: the panel can show which account a reading came
// from without ever holding the key.
func AccountTag(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return "acct-" + hex.EncodeToString(sum[:])[:8]
}

// SanitizeLabel validates a display name. A label a person typed is only shown
// when it does not look like a credential: pasting a key into a display name
// would otherwise put it into State and into the activity log.
func SanitizeLabel(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if len([]rune(trimmed)) > maxLabelChars {
		return "", sourceError("invalid_label", "名称过长，最多 40 个字符。")
	}
	if isControl(trimmed) {
		return "", sourceError("invalid_label", "名称含有控制字符。")
	}
	if secretShaped.MatchString(trimmed) {
		return "", sourceError("invalid_label", "名称看起来像密钥，请改用短名称。")
	}
	return trimmed, nil
}

func optString(value string) *string {
	if value == "" {
		return nil
	}
	copied := value
	return &copied
}

func optISO(ms *int64) *string {
	if ms == nil || *ms <= 0 {
		return nil
	}
	value := time.UnixMilli(*ms).UTC().Format("2006-01-02T15:04:05.000Z")
	return &value
}

func stringOf(value any) string {
	text, _ := value.(string)
	return text
}