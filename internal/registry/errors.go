package registry

import (
	"errors"
	"fmt"
	"net/http"
)

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return e.code + ": " + e.message }

func errf(status int, code, format string, args ...any) error {
	return &apiError{status: status, code: code, message: fmt.Sprintf(format, args...)}
}

func badRequest(code, format string, args ...any) error {
	return errf(http.StatusBadRequest, code, format, args...)
}

func unprocessable(code, format string, args ...any) error {
	return errf(http.StatusUnprocessableEntity, code, format, args...)
}

func conflict(code, format string, args ...any) error {
	return errf(http.StatusConflict, code, format, args...)
}

func notFound(code, format string, args ...any) error {
	return errf(http.StatusNotFound, code, format, args...)
}

func errorResponse(err error) (int, string, string) {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status, ae.code, ae.message
	}
	return http.StatusInternalServerError, "INTERNAL", "内部错误"
}
