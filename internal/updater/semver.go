package updater

import (
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

type version struct {
	numbers [3]string
	pre     []string
}

func parseVersion(value string) (version, bool) {
	parts := versionPattern.FindStringSubmatch(value)
	if parts == nil {
		return version{}, false
	}
	v := version{numbers: [3]string{parts[1], parts[2], parts[3]}}
	if parts[4] != "" {
		v.pre = strings.Split(parts[4], ".")
		for _, id := range v.pre {
			if len(id) > 1 && id[0] == '0' && numeric(id) {
				return version{}, false
			}
		}
	}
	return v, true
}

func numeric(value string) bool {
	return strings.Trim(value, "0123456789") == ""
}

func compareNumeric(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

func (v version) compare(other version) int {
	for i, part := range v.numbers {
		if order := compareNumeric(part, other.numbers[i]); order != 0 {
			return order
		}
	}
	if len(v.pre) == 0 || len(other.pre) == 0 {
		return len(other.pre) - len(v.pre)
	}
	for i := 0; i < min(len(v.pre), len(other.pre)); i++ {
		a, b := v.pre[i], other.pre[i]
		switch {
		case numeric(a) && numeric(b):
			if order := compareNumeric(a, b); order != 0 {
				return order
			}
		case numeric(a):
			return -1
		case numeric(b):
			return 1
		default:
			if order := strings.Compare(a, b); order != 0 {
				return order
			}
		}
	}
	return len(v.pre) - len(other.pre)
}
