package plugins

import (
	"errors"
	"net/http"

	"enhancedkimi/internal/httpx"
)

// Error is a plugin API failure that already knows the HTTP status the core
// should answer with. Anything else is reported as an internal error, so an
// unexpected Go error never leaks its text to the renderer.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func apiError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

func fail(w http.ResponseWriter, err error) {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		httpx.Fail(w, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	httpx.Fail(w, http.StatusInternalServerError, "PLUGIN_INTERNAL", "插件操作失败，核心仍在运行")
}
