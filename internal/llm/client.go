package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/312-dev/navidrome-mood/internal/mood"
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
	// ErrMalformedReply is a reply that arrived intact and did not fit the schema:
	// the labels array delivered as a JSON string, a field of the wrong type, a
	// tool call the model declined to make. The request was fine, so the same
	// request has a real chance of working, which is what separates this from a
	// 4xx.
	ErrMalformedReply = errors.New("llm: reply did not match the schema")
	// ErrTransport is any failure to complete the HTTP exchange at all: a
	// timeout, a reset connection, DNS. An error from the Doer always means this,
	// because a request that reached the provider comes back as a Response with a
	// status code, however unhappy that status is.
	ErrTransport = errors.New("llm: could not reach the provider")
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

// Label is one track's verdict: its position in mood-space plus the words for it.
//
// The five axes are 0-100 and give smart playlists something continuous to filter
// on, which a controlled vocabulary alone cannot. Tempo and vocal are closed sets
// rather than numbers because neither is a magnitude: "rapped" is not more of
// anything than "sung".
type Label struct {
	ID           string `json:"id"`
	Energy       int    `json:"energy"`
	Valence      int    `json:"valence"`
	Intensity    int    `json:"intensity"`
	Acousticness int    `json:"acousticness"`
	Density      int    `json:"density"`
	Tempo        string `json:"tempo"`
	Vocal        string `json:"vocal"`
	// Moods are free-form descriptors, mapped onto the controlled vocabulary on
	// the way into the tag. Anything that folds onto none of the 52 anchored
	// terms is dropped: an unanchored word sits at no coordinates, falls in no
	// vibe region, and nothing downstream can reason about where it is.
	Moods []string `json:"moods"`
	Times []string `json:"times,omitempty"`
}

// ErrInvalidLabel marks a reply the model got wrong in a way that cannot be
// repaired locally.
var ErrInvalidLabel = errors.New("llm: label is unusable")

// Validate reports whether a label can be written to a file at all.
//
// The five axes plus tempo and vocal are read all-or-nothing by the connector, so
// a bad value is rejected rather than clamped. Clamping an out-of-range axis to
// 100 produces a plausible-looking wrong answer that nothing downstream can tell
// apart from a real one; rejecting sends the track down the unresolved path,
// where the gap stays visible and a later pass retries it.
func (l Label) Validate() error {
	var problems []string
	for _, a := range []struct {
		name string
		v    int
	}{
		{"energy", l.Energy},
		{"valence", l.Valence},
		{"intensity", l.Intensity},
		{"acousticness", l.Acousticness},
		{"density", l.Density},
	} {
		if a.v < 0 || a.v > 100 {
			problems = append(problems, fmt.Sprintf("%s is %d, outside 0-100", a.name, a.v))
		}
	}
	if !inSet(mood.TempoFeels, mood.TempoFeel(l.Tempo)) {
		problems = append(problems, fmt.Sprintf("tempo %q is not one of %s",
			l.Tempo, join(mood.TempoFeels)))
	}
	if !inSet(mood.VocalKinds, mood.VocalKind(l.Vocal)) {
		problems = append(problems, fmt.Sprintf("vocal %q is not one of %s",
			l.Vocal, join(mood.VocalKinds)))
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w (%s): %s", ErrInvalidLabel, l.ID, strings.Join(problems, "; "))
}

// Point is where the label sits in mood-space, which is what region membership is
// measured against.
func (l Label) Point() mood.Point {
	return mood.Point{
		Axes: mood.Axes{
			Energy:       l.Energy,
			Valence:      l.Valence,
			Intensity:    l.Intensity,
			Acousticness: l.Acousticness,
			Density:      l.Density,
		},
		Tempo: mood.TempoFeel(l.Tempo),
		Vocal: mood.VocalKind(l.Vocal),
	}
}

// KnownTimes drops time slots outside TimeSlots.
//
// Unlike the axes this is a filter rather than a rejection: moodtime sits outside
// the all-or-nothing set, so a slot the connector has no name for costs nothing
// to discard and does not make the rest of the track unusable.
func KnownTimes(raw []string) []string {
	var out []string
	seen := make(map[string]bool, len(raw))
	for _, r := range raw {
		s := strings.ToLower(strings.TrimSpace(r))
		if seen[s] || !inSet(TimeSlots, s) {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func inSet[T comparable](haystack []T, needle T) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func join[T ~string](vals []T) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	return strings.Join(out, ", ")
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
	// ModelID is the resolved model, with any default already applied.
	//
	// On the interface rather than recovered by a type switch elsewhere: a
	// switch has to be taught about every implementation, and anything it does
	// not recognise silently prices at nothing. Since pricing drives the spend
	// cap, "silently unknown" is the one answer that must not be possible.
	ModelID() string
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
