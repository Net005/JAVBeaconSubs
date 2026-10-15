package engine

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"javbeaconsubs/internal/config"
)

type translationTransport func(*http.Request) (*http.Response, error)

func (f translationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTranslationTransientFailureRetriesOnlyOnce(t *testing.T) {
	for _, status := range []int{401, 400, 503, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			r := &Runner{cfg: config.Config{Translation: config.TranslationConfig{BaseURL: "https://translator.example.com/v1"}}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			r.client = &http.Client{Transport: translationTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader("synthetic error")), Header: make(http.Header)}, nil
			})}
			var output translationResult
			if _, err := r.chat(context.Background(), chatRequest{}, &output); err == nil {
				t.Fatal("permanent failure accepted")
			}
			want := 1
			if status == 503 || status == 429 {
				want = 2
			}
			if calls != want {
				t.Fatalf("calls=%d want=%d", calls, want)
			}
		})
	}
}

func TestTranslationTimeoutRecoversAndCancellationStopsRetry(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		calls := 0
		ctx, cancel := context.WithCancel(context.Background())
		r := &Runner{cfg: config.Config{Translation: config.TranslationConfig{BaseURL: "https://translator.example.com/v1"}}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		r.client = &http.Client{Transport: translationTransport(func(*http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				if cancelRequest {
					cancel()
				}
				return nil, context.DeadlineExceeded
			}
			body := `{"choices":[{"message":{"content":"{\"translations\":[{\"id\":0,\"text\":\"Example\"}]}"}}]}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}
		var output translationResult
		started := time.Now()
		_, err := r.chat(ctx, chatRequest{}, &output)
		cancel()
		if cancelRequest {
			if err == nil || calls != 1 || time.Since(started) > time.Second {
				t.Fatal("cancellation retried or delayed")
			}
		} else if err != nil || calls != 2 || len(output.Translations) != 1 {
			t.Fatalf("timeout recovery failed: calls=%d err=%v", calls, err)
		}
	}
}
