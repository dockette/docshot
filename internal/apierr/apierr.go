// Package apierr defines HTTP errors carrying a status and a stable code.
package apierr

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Error carries an HTTP status up from the pipeline.
type Error struct {
	Status int
	Code   string // derived from Status when empty
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func New(status int, format string, a ...any) error {
	return &Error{Status: status, Msg: fmt.Sprintf(format, a...)}
}

// statusCodes are the default codes by status.
var statusCodes = map[int]string{
	http.StatusBadRequest:                   "bad_request",
	http.StatusForbidden:                    "forbidden",
	http.StatusNotFound:                     "not_found",
	http.StatusMethodNotAllowed:             "method_not_allowed",
	http.StatusRequestEntityTooLarge:        "too_large",
	http.StatusUnsupportedMediaType:         "unsupported_format",
	http.StatusRequestedRangeNotSatisfiable: "out_of_range",
	http.StatusUnprocessableEntity:          "unreadable_file",
	http.StatusTooManyRequests:              "busy",
	http.StatusBadGateway:                   "engine_failed",
	http.StatusServiceUnavailable:           "unavailable",
	http.StatusInternalServerError:          "internal",
}

// Write sends err as uncached JSON {"error", "code", "status"}.
func Write(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, ""
	var he *Error
	if errors.As(err, &he) {
		status, code = he.Status, he.Code
	}
	if code == "" {
		code = cmp.Or(statusCodes[status], "error")
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "code": code, "status": status})
}
