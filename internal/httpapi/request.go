package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const maxBodyBytes = 1 << 20

// errBadRequest carries a client-facing message for a 400 response. Code is
// "invalid_request" unless set: errors users can cause from a form get their
// own code so the app can show a translated message.
type errBadRequest struct{ code, msg string }

func (e errBadRequest) Error() string { return e.msg }

func badRequest(format string, args ...any) error {
	return errBadRequest{msg: fmt.Sprintf(format, args...)}
}

func badRequestCode(code, format string, args ...any) error {
	return errBadRequest{code: code, msg: fmt.Sprintf(format, args...)}
}

// decodeJSON reads a single JSON object into dst, rejecting unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			return badRequest("request body is too large")
		case errors.Is(err, io.EOF):
			return badRequest("request body is empty")
		default:
			return badRequest("invalid JSON: %v", err)
		}
	}
	if dec.More() {
		return badRequest("request body must contain a single JSON object")
	}
	return nil
}

// writeBadRequest answers 400 for decode/validation errors and reports
// whether it did.
func writeBadRequest(w http.ResponseWriter, err error) bool {
	var br errBadRequest
	if errors.As(err, &br) {
		code := br.code
		if code == "" {
			code = "invalid_request"
		}
		writeError(w, http.StatusBadRequest, code, br.msg)
		return true
	}
	return false
}

func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 254 {
		return "", badRequestCode("invalid_email", "email is not valid")
	}
	return email, nil
}

func cleanName(raw string, field string, required bool, max int) (string, error) {
	name := strings.TrimSpace(raw)
	if required && name == "" {
		return "", badRequest("%s is required", field)
	}
	if len([]rune(name)) > max {
		return "", badRequest("%s must be at most %d characters", field, max)
	}
	return name, nil
}

func validCurrency(code string) bool {
	if len(code) != 3 {
		return false
	}
	for _, c := range code {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// uuidParam parses a UUID path parameter; ok is false when it isn't one.
func uuidParam(r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	return id, err == nil
}
