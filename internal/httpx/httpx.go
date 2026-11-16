package httpx

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func JSON(w http.ResponseWriter, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		data = []byte(`{"error":{"code":"INTERNAL_ERROR","message":"无法序列化响应"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(append(data, '\n'))
}
func Fail(w http.ResponseWriter, status int, code, message string) {
	JSON(w, status, map[string]any{"error": Error{code, message}})
}
func BodyLimit(r *http.Request) int64 {
	if r.URL.Path == "/enhance/v1/models/modelsdev" {
		return 16 << 20
	}
	return 2 << 20
}
func Decode(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, BodyLimit(r)))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		Fail(w, 400, "INVALID_JSON", "请求格式无效或超过大小限制")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		Fail(w, 400, "INVALID_JSON", "请求只能包含一个 JSON 值")
		return false
	}
	return true
}
func Page(r *http.Request) (int, int) {
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	return offset, limit
}
