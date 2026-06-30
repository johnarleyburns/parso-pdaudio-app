// Package provider enumerates recordings from external sources (IA, Commons).
package provider

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/johnarleyburns/parso-pdaudio/internal/core"
)

// Provider enumerates candidate recordings, resumable via an opaque cursor.
type Provider interface {
	Key() string
	Discover(ctx context.Context, cursor string) (cands []core.Candidate, nextCursor string, done bool, err error)
}

// Client is a polite HTTP client with a descriptive UA and retry/backoff.
type Client struct {
	HTTP      *http.Client
	UserAgent string
}

// NewClient builds a Client with the given UA.
func NewClient(ua string) *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 60 * time.Second},
		UserAgent: ua,
	}
}

// GetJSON fetches url and returns the body, retrying on 429/5xx/maxlag.
func (c *Client) GetBytes(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			d := backoff(attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(d):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.UserAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return body, rerr
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("http %d", resp.StatusCode)
			continue
		}
		return nil, fmt.Errorf("http %d for %s", resp.StatusCode, url)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("exhausted retries")
	}
	return nil, lastErr
}

func backoff(attempt int) time.Duration {
	base := time.Duration(1<<uint(attempt-1)) * time.Second
	jitter := time.Duration(rand.Int63n(int64(500 * time.Millisecond)))
	return base + jitter
}

// FormatRank returns a ranking function (lower = preferred) from a prefer list.
func FormatRank(prefer []string) map[string]int {
	m := map[string]int{}
	for i, f := range prefer {
		m[strings.ToLower(f)] = i
	}
	return m
}

// SelectBest chooses the highest-preference file among a candidate's variants.
// If none match and allowFallback is true, any audio file is accepted.
func SelectBest(files []core.CandidateFile, rank map[string]int, allowFallback bool) (core.CandidateFile, bool) {
	best := core.CandidateFile{}
	bestRank := 1 << 30
	for _, f := range files {
		if r, ok := rank[strings.ToLower(f.Format)]; ok && r < bestRank {
			bestRank = r
			best = f
		}
	}
	if bestRank != 1<<30 {
		return best, true
	}
	if allowFallback {
		for _, f := range files {
			if isAudioFormat(f.Format) {
				return f, true
			}
		}
	}
	return core.CandidateFile{}, false
}

func isAudioFormat(f string) bool {
	switch strings.ToLower(f) {
	case "opus", "ogg", "wav", "mp3", "flac", "m4a", "aac", "oga", "other":
		return true
	}
	return false
}

// parseYear extracts a best-effort 4-digit year from a raw date string.
func parseYear(raw string) int {
	run := []rune(raw)
	for i := 0; i+4 <= len(run); i++ {
		s := string(run[i : i+4])
		if y, err := strconv.Atoi(s); err == nil && y >= 1000 && y <= 2100 {
			return y
		}
	}
	return 0
}
