package usage

import (
	"encoding/json"
	"math"
	"strings"
)

type Counts struct {
	Fresh  *int64 `json:"fresh"`
	Input  *int64 `json:"input"`
	Output *int64 `json:"output"`
	Read   *int64 `json:"read"`
	Write  *int64 `json:"write"`
}

func countPtr(value int64) *int64 {
	return &value
}

func count(raw map[string]any, key string) *int64 {
	value, ok := raw[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case float64:
		if typed < 0 || typed >= float64(math.MaxInt64) || typed != float64(int64(typed)) {
			return nil
		}
		return countPtr(int64(typed))
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed < 0 {
			return nil
		}
		return countPtr(parsed)
	default:
		return nil
	}
}

func detailCount(raw map[string]any, outer, inner string) *int64 {
	nested, ok := raw[outer].(map[string]any)
	if !ok {
		return nil
	}
	return count(nested, inner)
}

// sumCounts adds counts and returns nil when any addend is unknown. A missing
// field is never treated as zero.
func sumCounts(values ...*int64) *int64 {
	var total int64
	for _, value := range values {
		if value == nil {
			return nil
		}
		if *value < 0 || total > math.MaxInt64-*value {
			return nil
		}
		total += *value
	}
	return &total
}

func subtractCounts(total, part *int64) *int64 {
	if total == nil || part == nil || *total < *part {
		return nil
	}
	result := *total - *part
	return &result
}

func Normalize(protocol string, raw map[string]any) Counts {
	if raw == nil {
		return Counts{}
	}
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai", "chat", "chat_completions", "openai-chat":
		input := count(raw, "prompt_tokens")
		if input == nil {
			input = count(raw, "input_tokens")
		}
		read := detailCount(raw, "prompt_tokens_details", "cached_tokens")
		if read == nil {
			read = detailCount(raw, "input_tokens_details", "cached_tokens")
		}
		output := count(raw, "completion_tokens")
		if output == nil {
			output = count(raw, "output_tokens")
		}
		return Counts{Fresh: subtractCounts(input, read), Input: input, Output: output, Read: read, Write: nil}
	case "responses", "openai-responses":
		input := count(raw, "input_tokens")
		if input == nil {
			input = count(raw, "prompt_tokens")
		}
		read := detailCount(raw, "input_tokens_details", "cached_tokens")
		if read == nil {
			read = detailCount(raw, "prompt_tokens_details", "cached_tokens")
		}
		output := count(raw, "output_tokens")
		if output == nil {
			output = count(raw, "completion_tokens")
		}
		return Counts{Fresh: subtractCounts(input, read), Input: input, Output: output, Read: read, Write: nil}
	case "anthropic", "anthropic-messages":
		fresh := count(raw, "input_tokens")
		read := count(raw, "cache_read_input_tokens")
		write := count(raw, "cache_creation_input_tokens")
		if write == nil {
			if ephemeral, ok := raw["cache_creation"].(map[string]any); ok {
				write = sumCounts(count(ephemeral, "ephemeral_5m_input_tokens"), count(ephemeral, "ephemeral_1h_input_tokens"))
			}
		}
		input := sumCounts(fresh, read, write)
		return Counts{Fresh: fresh, Input: input, Output: count(raw, "output_tokens"), Read: read, Write: write}
	case "gemini", "google", "google-gemini":
		input := count(raw, "promptTokenCount")
		read := count(raw, "cachedContentTokenCount")
		output := sumCounts(count(raw, "candidatesTokenCount"), count(raw, "thoughtsTokenCount"))
		return Counts{Fresh: subtractCounts(input, read), Input: input, Output: output, Read: read, Write: nil}
	default:
		return Counts{}
	}
}
