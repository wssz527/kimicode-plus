package config

import (
	"encoding/json"
	"testing"
)

func TestNullSettingsDoNotBecomeFalseOrZero(t *testing.T) {
	for _, body := range []string{`{"revision":0,"safeMode":null,"compactionIdleMinutes":25,"defaultChatSource":"kimi","modules":{"registry":true,"usage":true,"bridge":true,"plugins":true}}`, `{"revision":null,"safeMode":false,"compactionIdleMinutes":25,"defaultChatSource":"kimi","modules":{"registry":true}}`, `{"revision":0,"safeMode":true,"compactionIdleMinutes":25,"defaultChatSource":"kimi","modules":{"registry":null}}`} {
		var u Update
		if err := json.Unmarshal([]byte(body), &u); err != nil {
			t.Fatal(err)
		}
		if _, err := u.Value(); err == nil {
			t.Fatal("unknown settings coerced into false or zero")
		}
	}
}
