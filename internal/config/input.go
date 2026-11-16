package config

import "fmt"

type Update struct {
	Revision              *int             `json:"revision"`
	SafeMode              *bool            `json:"safeMode"`
	CompactionIdleMinutes *int             `json:"compactionIdleMinutes"`
	DefaultChatSource     *string          `json:"defaultChatSource"`
	Modules               map[string]*bool `json:"modules"`
}

func (u Update) Value() (Settings, error) {
	if u.Revision == nil || u.SafeMode == nil || u.CompactionIdleMinutes == nil || u.DefaultChatSource == nil || u.Modules == nil {
		return Settings{}, fmt.Errorf("all settings fields must be explicitly provided")
	}
	v := Settings{Revision: *u.Revision, SafeMode: *u.SafeMode, CompactionIdleMinutes: *u.CompactionIdleMinutes, DefaultChatSource: *u.DefaultChatSource, Modules: make(map[string]bool, len(u.Modules))}
	for name, enabled := range u.Modules {
		if enabled == nil {
			return Settings{}, fmt.Errorf("module setting must be boolean")
		}
		v.Modules[name] = *enabled
	}
	return v, nil
}
