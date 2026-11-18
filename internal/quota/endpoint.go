package quota

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	blockedSuffixes = []string{".local", ".internal", ".localdomain", ".lan", ".home.arpa", ".intranet", ".corp"}
	loopbackNames   = map[string]bool{"localhost": true, "localhost.localdomain": true}
	inferencePath   = regexp.MustCompile(`(?i)/(?:chat/completions|completions|responses|messages|embeddings|images(?:/|$)|audio(?:/|$)|videos(?:/|$)|generateContent|streamGenerateContent)(?:/|$)`)
)

func ipv4Parts(host string) []int {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return nil
	}
	out := make([]int, 4)
	for index, part := range parts {
		if part == "" || len(part) > 3 {
			return nil
		}
		value := atoiSafe(part)
		if value < 0 || value > 255 {
			return nil
		}
		out[index] = value
	}
	return out
}

func atoiSafe(value string) int {
	result := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return -1
		}
		result = result*10 + int(r-'0')
	}
	return result
}

func isPrivateIPv4(parts []int) bool {
	a, b := parts[0], parts[1]
	switch {
	case a == 0 || a == 10:
		return true
	case a == 100 && b >= 64 && b <= 127:
		return true
	case a == 169 && b == 254:
		return true
	case a == 172 && b >= 16 && b <= 31:
		return true
	case a == 192 && b == 168:
		return true
	case a == 192 && b == 0:
		return true
	case a == 198 && (b == 18 || b == 19):
		return true
	case a >= 224:
		return true
	}
	return false
}

func normalizeHostname(hostname string) string {
	host := strings.ToLower(hostname)
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	host = strings.TrimSuffix(host, ".")
	return host
}

func isLoopbackHost(host string) bool {
	if loopbackNames[host] {
		return true
	}
	if parts := ipv4Parts(host); parts != nil && parts[0] == 127 {
		return true
	}
	return host == "::1" || host == "0:0:0:0:0:0:0:1"
}

func isBlockedHost(host string) bool {
	if host == "" {
		return true
	}
	if isLoopbackHost(host) {
		return false
	}
	if parts := ipv4Parts(host); parts != nil {
		return isPrivateIPv4(parts)
	}
	if strings.Contains(host, ":") {
		if host == "::" || host == "::1" {
			return true
		}
		for _, prefix := range []string{"fc", "fd", "fe8", "fe9", "fea", "feb", "ff"} {
			if strings.HasPrefix(host, prefix) {
				return true
			}
		}
		if strings.HasPrefix(host, "::ffff:") {
			return isBlockedHost(host[len("::ffff:"):])
		}
		return false
	}
	for _, suffix := range blockedSuffixes {
		if host == suffix[1:] || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	// A single label is an intranet name by construction (`metadata`, `nas`).
	if !strings.Contains(host, ".") {
		return true
	}
	return false
}

// CheckResult is the static gate a local endpoint passes.
type CheckResult struct {
	OK       bool
	Code     string
	Href     string
	Host     string
	Protocol string
}

// CheckEndpoint is the static gate a local or configured endpoint passes before
// it is requested.
//
// This is static validation, not a resolved-address check. A public hostname can
// resolve to a private address at request time (DNS rebinding), and nothing here
// resolves DNS to prevent it. Describe it as a narrow, number-only read with a
// host allowlist rule, not as a complete SSRF defence.
func CheckEndpoint(raw string) CheckResult {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > maxURLChars || isControl(value) {
		return CheckResult{Code: "invalid_endpoint"}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return CheckResult{Code: "invalid_endpoint"}
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return CheckResult{Code: "invalid_endpoint"}
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return CheckResult{Code: "invalid_endpoint"}
	}
	host := normalizeHostname(parsed.Hostname())
	if host == "" {
		return CheckResult{Code: "invalid_endpoint"}
	}
	if parsed.Scheme == "http" && !isLoopbackHost(host) {
		// Plain HTTP to a remote host would put a credential on the wire in clear.
		return CheckResult{Code: "insecure_endpoint"}
	}
	if isBlockedHost(host) {
		return CheckResult{Code: "blocked_target"}
	}
	if inferencePath.MatchString(parsed.Path) {
		return CheckResult{Code: "inference_endpoint"}
	}
	return CheckResult{OK: true, Href: parsed.String(), Host: host, Protocol: parsed.Scheme}
}

// HostOf returns the lowercased hostname of a base URL, or "" when it cannot be
// parsed. The provider name a person typed is not an identity: the same vendor
// can be registered as "StepFun Step Plan" or as an id with spaces, so adapters
// match on the host only.
func HostOf(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Host == "" {
		return ""
	}
	return normalizeHostname(parsed.Hostname())
}