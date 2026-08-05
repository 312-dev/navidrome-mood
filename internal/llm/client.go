package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Doer is the seam over Navidrome's http_send host service.
//
// It exists so provider logic is testable on the host: the wasm build wires it to
// host.HTTPSend, tests wire it to a fake. Nothing below this line knows it is
// running inside Extism.
type Doer interface {
	Do(req Request) (*Response, error)
}

type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
	// TimeoutMs overrides the host's 10s default. Labelling a batch of 20 tracks
	// takes longer than that, but stay under Navidrome's hard 30s per-invocation
	// ceiling or the whole call is killed with the request still in flight.
	TimeoutMs int64
}

type Response struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// Two host_http behaviours that fail quietly and are easy to design around only
// if you know about them in advance:
//
//  1. No streaming. http_send returns complete responses only, so every request
//     must set stream:false and keep max_tokens modest enough to finish inside
//     the invocation ceiling.
//  2. A 10 MB silent truncation. The host reads through io.LimitReader and
//     returns what it got with NO error, so an oversized response arrives as
//     malformed JSON rather than as a size failure. maxResponseBytes below exists
//     to turn that into a legible message.
const maxResponseBytes = 10 << 20

var (
	ErrTruncated   = errors.New("llm: response hit the host's 10 MB limit and was silently truncated")
	ErrRateLimited = errors.New("llm: rate limited")
	ErrAuth        = errors.New("llm: provider rejected the API key")
)

// APIError carries a provider error in a form worth showing a user. Provider
// error bodies are the main place a raw API key can leak into a log, so the body
// is never included verbatim.
type APIError struct {
	Status   int
	Provider string
	Message  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm: %s returned %d: %s", e.Provider, e.Status, e.Message)
}

func (e *APIError) Unwrap() error {
	switch {
	case e.Status == 401 || e.Status == 403:
		return ErrAuth
	case e.Status == 429:
		return ErrRateLimited
	}
	return nil
}

// Track is the input the model is asked to label. Deliberately small: title,
// artist, album, year and whatever tags exist. No audio, no file paths.
type Track struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Artist string   `json:"artist"`
	Album  string   `json:"album,omitempty"`
	Year   int      `json:"year,omitempty"`
	Genres []string `json:"genres,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}

// Label is one track's verdict. The numeric axes are 0-100 and give smart
// playlists something continuous to filter on, which a controlled vocabulary
// alone cannot.
type Label struct {
	ID        string `json:"id"`
	Energy    int    `json:"energy"`
	Valence   int    `json:"valence"`
	Intensity int    `json:"intensity"`
	Organic   int    `json:"organic"`
	// Moods are free-form descriptors. They are stored verbatim in kvstore and
	// only mapped onto the controlled vocabulary on the way into the tag, so the
	// enum can be revised later without re-paying for inference.
	Moods []string `json:"moods"`
	Times []string `json:"times,omitempty"`
}

// Result pairs labels with the usage that produced them, so the caller can charge
// the budget from what actually happened rather than from an estimate.
type Result struct {
	Labels []Label
	Usage  Usage
}

// Provider is one LLM backend.
type Provider interface {
	// Name identifies the provider in errors shown to the user.
	Name() string
	// Label sends one batch and returns verdicts plus real usage.
	Label(system string, tracks []Track) (*Result, error)
	// SupportsBatch reports whether a discounted async batch API is available.
	SupportsBatch() bool
}

// checkResponse turns transport-level oddities into typed errors before any
// provider-specific parsing runs.
func checkResponse(provider string, r *Response) error {
	if len(r.Body) >= maxResponseBytes {
		return fmt.Errorf("%w (provider %s)", ErrTruncated, provider)
	}
	if r.Status >= 200 && r.Status < 300 {
		return nil
	}
	return &APIError{
		Status:   r.Status,
		Provider: provider,
		Message:  extractMessage(r.Body),
	}
}

// extractMessage pulls a human-readable message out of a provider error body.
//
// It never returns the raw body. Provider errors echo request context, and for a
// misconfigured auth header that can include the credential itself - which would
// then land in Navidrome's log at error level.
func extractMessage(body []byte) string {
	var probe struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &probe); err == nil {
		if m := probe.Error.Message; m != "" {
			return truncate(m, 300)
		}
		if m := probe.Message; m != "" {
			return truncate(m, 300)
		}
		if t := probe.Error.Type; t != "" {
			return truncate(t, 300)
		}
	}
	return "unparseable error body (omitted: it can contain the API key)"
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
