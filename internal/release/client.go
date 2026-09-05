// Package release resolves optional release metadata (title, story,
// provider) for a job from a JAVBeacon instance, by internal release_id or
// exact external/catalog video_id (e.g. "ADN-803"). javbeaconsubs does not
// import JAVBeacon's own Go module - the two are separate projects/binaries -
// so Release below is a deliberately narrow, independent copy of the wire
// shape JAVBeacon's GET /api/releases endpoints already return.
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Release mirrors the fields of JAVBeacon's domain.Release that translation
// context needs. JAVBeacon's API returns many more fields; the rest are
// simply ignored by json.Decode.
type Release struct {
	ID            int64  `json:"id"`
	VideoID       string `json:"video_id"`
	StashFilePath string `json:"stash_file_path,omitempty"`
	Title         string `json:"title"`
	Story         string `json:"story"`
	Source        string `json:"source"`
	// Local and StashSceneID mirror JAVBeacon's own "open in StashApp" link
	// condition (internal/web/static/app.js's stashSceneURL): only a release
	// JAVBeacon has actually matched to a local StashApp scene carries a
	// non-empty StashSceneID with Local true.
	Local        bool   `json:"local"`
	StashSceneID string `json:"stash_scene_id,omitempty"`
}

// trimVideoIDPrefix removes the redundant catalog code some JAVBeacon
// providers include at the beginning of Title. It only trims an exact,
// case-insensitive ID at a clear separator boundary, so a title that merely
// starts with similar characters is left untouched.
func trimVideoIDPrefix(title, videoID string) string {
	title = strings.TrimSpace(title)
	videoID = strings.TrimSpace(videoID)
	if videoID == "" || len(title) <= len(videoID) || !strings.EqualFold(title[:len(videoID)], videoID) {
		return title
	}
	remainder := title[len(videoID):]
	first, _ := utf8.DecodeRuneInString(remainder)
	if !unicode.IsSpace(first) && !strings.ContainsRune("-–—:|", first) {
		return title
	}
	trimmed := strings.TrimLeftFunc(remainder, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("-–—:|", r)
	})
	if trimmed == "" {
		return title
	}
	return trimmed
}

func normalizeRelease(item *Release) {
	item.Title = trimVideoIDPrefix(item.Title, item.VideoID)
}

// ErrNotFound is returned by ByID when JAVBeacon reports 404 for the given
// internal release id.
var ErrNotFound = errors.New("release not found")

// ErrUnavailable wraps any error that means "could not complete the
// request" - network failure, timeout, non-2xx/404 status, or a response
// that failed to decode. It is distinct from ErrNotFound (a clean "no such
// release" answer from a reachable JAVBeacon) per TODO Part 39.
var ErrUnavailable = errors.New("javbeacon lookup unavailable")

// Client talks to a single JAVBeacon instance's release-lookup endpoints.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client

	// stashBaseURLCache memoizes GET /api/settings' stash_base_url for a
	// short window, since it changes rarely but StashBaseURL may be called
	// once per matched-local-release job creation.
	stashMu           sync.Mutex
	stashBaseURLValue string
	stashBaseURLAt    time.Time
}

const stashBaseURLCacheTTL = 5 * time.Minute

// NewClient returns nil when baseURL is empty, so callers can treat a nil
// *Client as "no JAVBeacon configured" without a separate enabled flag.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{baseURL: baseURL, apiKey: apiKey, httpClient: &http.Client{Timeout: timeout}}
}

func (c *Client) get(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return resp, nil
}

// ByID looks up a release by JAVBeacon's internal database id - exact
// identity (TODO Part 3).
func (c *Client) ByID(ctx context.Context, id int64) (Release, error) {
	resp, err := c.get(ctx, "/api/releases/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Release{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("%w: unexpected status %d", ErrUnavailable, resp.StatusCode)
	}
	var out Release
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Release{}, fmt.Errorf("%w: decode response: %v", ErrUnavailable, err)
	}
	normalizeRelease(&out)
	return out, nil
}

// ByVideoID looks up releases by an exact, case-insensitive catalog/video ID
// match (e.g. "ADN-803") - no fuzzy matching (TODO Part 4). Zero, one, or
// (in principle) more than one release may come back; the caller (Resolve)
// decides what "more than one" means.
func (c *Client) ByVideoID(ctx context.Context, videoID string) ([]Release, error) {
	return c.byExactField(ctx, "video_id", videoID)
}

// ByStashFilePath looks up releases by the exact, case-insensitive full media
// path stored by JAVBeacon. This endpoint was added in JAVBeacon v1.0.71.
func (c *Client) ByStashFilePath(ctx context.Context, filePath string) ([]Release, error) {
	return c.byExactField(ctx, "stash_file_path", filePath)
}

func (c *Client) byExactField(ctx context.Context, field, value string) ([]Release, error) {
	resp, err := c.get(ctx, "/api/releases", url.Values{field: {value}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: unexpected status %d", ErrUnavailable, resp.StatusCode)
	}
	var out []Release
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: decode response: %v", ErrUnavailable, err)
	}
	for i := range out {
		normalizeRelease(&out[i])
	}
	return out, nil
}

// StashBaseURL returns JAVBeacon's configured stash_base_url setting (the
// root URL of the operator's StashApp instance), used to build "open in
// StashApp" links for locally-matched releases. It is best-effort: any
// failure (unreachable, unauthorized, unset) simply returns "", nil rather
// than an error, since a missing Stash link should never block a job or a
// release lookup that otherwise succeeded.
func (c *Client) StashBaseURL(ctx context.Context) string {
	c.stashMu.Lock()
	if time.Since(c.stashBaseURLAt) < stashBaseURLCacheTTL {
		value := c.stashBaseURLValue
		c.stashMu.Unlock()
		return value
	}
	c.stashMu.Unlock()

	resp, err := c.get(ctx, "/api/settings", nil)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var settings map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&settings); err != nil {
		return ""
	}
	value := strings.TrimRight(strings.TrimSpace(settings["stash_base_url"]), "/")

	c.stashMu.Lock()
	c.stashBaseURLValue = value
	c.stashBaseURLAt = time.Now()
	c.stashMu.Unlock()
	return value
}

// StashSceneURL builds the "open in StashApp" link for a matched release,
// mirroring JAVBeacon's own internal/web/static/app.js stashSceneURL(): only
// when the release is actually locally matched (Local) with a non-empty
// StashSceneID, and a stash_base_url is configured, do we return a link.
func (c *Client) StashSceneURL(ctx context.Context, r Release) string {
	if !r.Local || r.StashSceneID == "" {
		return ""
	}
	base := c.StashBaseURL(ctx)
	if base == "" {
		return ""
	}
	return base + "/scenes/" + url.PathEscape(r.StashSceneID)
}
