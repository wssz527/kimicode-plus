package provider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"enhancedkimi/internal/httpx"
)

// apiError carries the HTTP status and stable code the account API returns. It
// mirrors the registry error contract so clients see one error shape.
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

func conflict(code, format string, args ...any) error {
	return errf(http.StatusConflict, code, format, args...)
}

func notFound(code, format string, args ...any) error {
	return errf(http.StatusNotFound, code, format, args...)
}

// APIError is a deliberate failure with a gateway-safe code. It never carries
// an upstream payload.
type APIError struct {
	status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }
func (e *APIError) Status() int   { return e.status }

func fail(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		httpx.Fail(w, ae.status, ae.code, ae.message)
		return
	}
	httpx.Fail(w, http.StatusInternalServerError, "INTERNAL", "内部错误")
}

// providerRow loads the registry row an account hangs off. Accounts never own
// endpoint configuration; they only add credentials.
type providerRow struct {
	ID        string
	Protocol  string
	BaseURL   string
	SecretRef *string
}

func loadProviderRow(ctx context.Context, db *sql.DB, id string) (providerRow, bool, error) {
	var row providerRow
	var secret sql.NullString
	err := db.QueryRowContext(ctx, `SELECT id,protocol,base_url,secret_ref FROM registry_providers WHERE id=?`, id).Scan(&row.ID, &row.Protocol, &row.BaseURL, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		return providerRow{}, false, nil
	}
	if err != nil {
		return providerRow{}, false, err
	}
	if secret.Valid && secret.String != "" {
		row.SecretRef = &secret.String
	}
	return row, true, nil
}

// looksLikeSecret mirrors the registry guard: a value with a known credential
// prefix is a raw key, not a vault reference.
func looksLikeSecret(value string) bool {
	if len(value) < 16 {
		return false
	}
	for _, prefix := range []string{"sk-", "sk_", "ghp_", "gho_", "xoxb-", "xoxp-", "AKIA", "AIza"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
