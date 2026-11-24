// SPDX-License-Identifier: AGPL-3.0-or-later

package sdk

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

// WriteJSON writes v as a JSON response (never cached). A nil v writes only the status.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// WriteError writes {"error": msg} with the given status.
func WriteError(w http.ResponseWriter, code int, msg string) {
	WriteJSON(w, code, map[string]string{"error": msg})
}

// DecodeJSON strictly decodes a JSON request body into v: the content type must
// be application/json (which also stops cross-site HTML form posts), the body is
// limited to maxBytes, unknown fields and trailing data are rejected. On failure
// it writes the error response itself and returns false.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		WriteError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}
