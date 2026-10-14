package server

import (
	"javbeaconsubs/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReleaseCoverKeepsCredentialServerSide(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/covers/42" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
			t.Error("incorrect upstream request")
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("synthetic-image"))
	}))
	defer upstream.Close()
	s := &Server{cfg: config.Config{APIToken: "synthetic-token", JAVBeacon: config.JAVBeaconConfig{BaseURL: upstream.URL, APIKey: "synthetic-key"}}}
	for _, authenticated := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/covers/42", nil)
		if authenticated {
			r.Header.Set("Authorization", "Bearer synthetic-token")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if !authenticated {
			if w.Code != http.StatusUnauthorized {
				t.Fatal("cover bypasses authentication")
			}
			continue
		}
		if w.Code != 200 || w.Header().Get("Cache-Control") != "private, max-age=600" || strings.Contains(w.Body.String(), "synthetic-key") {
			t.Fatalf("unsafe cover response: %d", w.Code)
		}
	}
}

func TestReleaseCoverRejectsRedirectsAndNonImages(t *testing.T) {
	for _, kind := range []string{"redirect", "html", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "redirect":
					http.Redirect(w, r, "https://example.com/image", 302)
				case "html":
					w.Header().Set("Content-Type", "text/html")
					w.Write([]byte("<html>"))
				case "oversize":
					w.Header().Set("Content-Type", "image/png")
					w.Write([]byte(strings.Repeat("x", (8<<20)+1)))
				}
			}))
			defer upstream.Close()
			s := &Server{cfg: config.Config{JAVBeacon: config.JAVBeaconConfig{BaseURL: upstream.URL}}}
			r := httptest.NewRequest(http.MethodGet, "/api/v1/covers/42", nil)
			r.SetPathValue("id", "42")
			w := httptest.NewRecorder()
			s.releaseCover(w, r)
			if w.Code != 404 {
				t.Fatalf("unexpected status %d", w.Code)
			}
		})
	}
}
