package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// sseWriter is the reusable Server-Sent Events transport for streaming
// endpoints (cluster nodes today; pods/workloads next). It owns the
// event-stream headers and frames each message as `event:`/`data:` lines.
//
// It is intentionally resource-agnostic: callers serialize whatever payload
// they like. Not safe for concurrent use — drive it from a single goroutine.
type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

// newSSEWriter switches the response into event-stream mode and returns a
// writer, or (nil, false) if the ResponseWriter can't flush (no streaming).
// It writes the 200 header, so all request validation must happen first.
func newSSEWriter(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Defeat response buffering in reverse proxies (nginx honours this), which
	// would otherwise hold events until the connection closes.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseWriter{w: w, f: f}, true
}

// event writes a named SSE event whose data is payload marshalled as JSON.
func (s *sseWriter) event(name string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

// comment writes an SSE comment line. Used for heartbeats: it keeps the
// connection (and any intermediary idle timers) alive without delivering a
// client-visible event.
func (s *sseWriter) comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}
