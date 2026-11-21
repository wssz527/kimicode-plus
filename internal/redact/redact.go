package redact

import (
	"regexp"
	"sort"
	"strings"
)

type rule struct {
	name string
	re   *regexp.Regexp
	repl string
}

var rules = []rule{
	{name: "PRIVATE_KEY", re: regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*?-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`), repl: "[REDACTED:PRIVATE_KEY]"},
	{name: "PRIVATE_KEY", re: regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----.*$`), repl: "[REDACTED:PRIVATE_KEY]"},
	{name: "BEARER_TOKEN", re: regexp.MustCompile(`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/=-]{8,}`), repl: "Bearer [REDACTED:BEARER_TOKEN]"},
	{name: "BASIC_AUTH", re: regexp.MustCompile(`(?i)\bBasic[ \t]+[A-Za-z0-9+/=]{8,}`), repl: "Basic [REDACTED:BASIC_AUTH]"},
	{name: "JWT", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), repl: "[REDACTED:JWT]"},
	{name: "API_KEY", re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`), repl: "[REDACTED:API_KEY]"},
	{name: "GITHUB_TOKEN", re: regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`), repl: "[REDACTED:GITHUB_TOKEN]"},
	{name: "SLACK_TOKEN", re: regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`), repl: "[REDACTED:SLACK_TOKEN]"},
	{name: "AWS_ACCESS_KEY", re: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), repl: "[REDACTED:AWS_ACCESS_KEY]"},
	{name: "GOOGLE_API_KEY", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`), repl: "[REDACTED:GOOGLE_API_KEY]"},
	{name: "HUGGINGFACE_TOKEN", re: regexp.MustCompile(`\bhf_[A-Za-z0-9]{20,}`), repl: "[REDACTED:HUGGINGFACE_TOKEN]"},
	{name: "NPM_TOKEN", re: regexp.MustCompile(`\bnpm_[A-Za-z0-9]{20,}`), repl: "[REDACTED:NPM_TOKEN]"},
	{name: "GOOGLE_OAUTH", re: regexp.MustCompile(`\bya29\.[A-Za-z0-9_-]{20,}`), repl: "[REDACTED:GOOGLE_OAUTH]"},
	{name: "SECRET_ASSIGNMENT", re: regexp.MustCompile(`(?i)\b(api[_-]?key|apikey|secret|token|password|passwd|pwd|access[_-]?key|private[_-]?key|client[_-]?secret|auth[_-]?token|pairing[_-]?token|session[_-]?token|access[_-]?token|refresh[_-]?token|gateway[_-]?token|bridge[_-]?key|x-ekd-bridge)\b(["']?\s*[:=]\s*["']?)([^\s"',\[\]]{6,})`), repl: "$1$2[REDACTED:SECRET_ASSIGNMENT]"},
	{name: "CLI_TOKEN", re: regexp.MustCompile(`(?i)(--pairing-token(?:\s+|=)["']?)([^\s"'\[\]]{6,})`), repl: "$1[REDACTED:CLI_TOKEN]"},
	{name: "URL_CREDENTIALS", re: regexp.MustCompile(`(?i)\b(https?://)([^/\s:@]+):([^/\s@]+)@`), repl: "$1[REDACTED-URL-USERINFO]@"},
	{name: "URL_QUERY", re: regexp.MustCompile(`(?i)([?&](?:access_token|api_key|apikey|token|secret|password|key)=)[^&\s#\[]+`), repl: "$1[REDACTED:URL_QUERY]"},
	{name: "PAIRING_URL", re: regexp.MustCompile(`(?i)(#pair=)[^\s"'\[\]&]+`), repl: "$1[REDACTED:PAIRING_URL]"},
	{name: "EMAIL", re: regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`), repl: "[REDACTED:EMAIL]"},
}

func Text(s string) string {
	for _, r := range rules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}

func Findings(s string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, r := range rules {
		if !seen[r.name] && r.re.MatchString(s) {
			seen[r.name] = true
			out = append(out, r.name)
		}
	}
	sort.Strings(out)
	return out
}

func JoinFindings(groups ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, group := range groups {
		for _, item := range group {
			item = strings.TrimSpace(item)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}
