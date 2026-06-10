package api

import (
	"encoding/json"
	"log"
	"net/http"
)

// StrictErrorOptions returns the error handlers wired into the generated
// strict handler (see lib/server/routes.go). The generated defaults reply via
// http.Error — the raw err.Error() string as text/plain — which both violates
// the API's JSON Error contract and echoes internal error strings
// (ent/pgx/k8s/river) to clients while logging nothing server-side.
//
// Request errors describe the caller's own malformed input (undecodable JSON
// body, unparsable path/query parameter), so their text is safe — and useful —
// to echo back as a 400. Response errors are handler failures no case mapped
// to a typed response; the detail is logged and the client gets an opaque 500.
func StrictErrorOptions() StrictHTTPServerOptions {
	return StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("api: %s %s: unhandled error: %v", r.Method, r.URL.Path, err)
			writeJSONError(w, http.StatusInternalServerError, "internal", "internal server error")
		},
	}
}

// writeJSONError renders an Error-schema JSON body for paths outside the
// generated typed responses (the strict-handler error hooks above and
// pre-stream SSE failures via writeStreamError).
func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// Marshal the proper Error struct so a message carrying a quote or
	// backslash (e.g. a raw k8s error) still produces valid JSON.
	body, err := json.Marshal(Error{Code: code, Message: msg})
	if err != nil {
		return
	}
	_, _ = w.Write(body)
}
