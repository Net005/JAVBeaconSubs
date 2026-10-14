package server

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Covers come from the configured JAVBeacon cache. Its API credential stays
// server-side, including for older jobs that predate cover metadata.
func (s *Server) releaseCover(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, max-age=60")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	cfg := s.javBeaconSettings()
	if err != nil || id <= 0 || strings.TrimSpace(cfg.BaseURL) == "" {
		http.NotFound(w, r)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(cfg.BaseURL, "/")+"/covers/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer response.Body.Close()
	kind := strings.Split(response.Header.Get("Content-Type"), ";")[0]
	if response.StatusCode != http.StatusOK || (kind != "image/jpeg" && kind != "image/png" && kind != "image/webp" && kind != "image/gif") {
		http.NotFound(w, r)
		return
	}
	const limit = 8 << 20
	content, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(content) > limit {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(content)
}
