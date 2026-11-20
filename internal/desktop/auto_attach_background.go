package desktop

import "context"

var runHostAttachWatcher func(*NativeService)

var reopenHost = func(context.Context, *NativeService) (map[string]any, error) {
	return nil, ErrUnsupportedPlatform
}
