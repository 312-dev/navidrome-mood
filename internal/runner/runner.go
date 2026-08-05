// Package runner orchestrates one batch of labelling: budget check, provider
// call, vocabulary mapping, tag write, and durable state.
//
// Everything here is behind interfaces so it runs as an ordinary Go test. The
// wasm build wires Store to kvstore and Tagger to the flac package; tests wire
// both to fakes.
package runner

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/312-dev/navidrome-mood/internal/llm"
	"github.com/312-dev/navidrome-mood/internal/mood"
)

// Store is the kvstore seam. State must be durable because a whole-library pass
// spans many invocations and can be interrupted by a restart at any point.
type Store interface {
	Get(key string) ([]byte, bool, error)
	Set(key string, value []byte) error
}

// Tagger is the file-writing seam.
type Tagger interface {
	// ReadMood returns existing MOOD values, so tracks tagged by Picard or beets
	// can be left alone.
	ReadMood(path string) ([]string, error)
	// WriteMood replaces MOOD with values and reports what it did.
	WriteMood(path string, values []string) (string, error)
}

// Item is one track to label, paired with the file to tag.
type Item struct {
	Track llm.Track
	Path  string
}

// Record is what is remembered per track. The free-form descriptors are kept
// verbatim alongside the canonical ones, so the controlled vocabulary can be
// revised later without re-paying for inference.
type Record struct {
	ID          string   `json:"id"`
	Energy      int      `json:"energy"`
	Valence     int      `json:"valence"`
	Intensity   int      `json:"intensity"`
	Organic     int      `json:"organic"`
	Freeform    []string `json:"freeform"`
	Canonical   []string `json:"canonical"`
	Times       []string `json:"times,omitempty"`
	Tagged      bool     `json:"tagged"`
	TagsWritten []string `json:"tagsWritten,omitempty"`
}

// Options configure one run.
type Options struct {
	SkipTagged bool
	DryRun     bool
}

// Outcome reports what a batch did, for progress reporting and for the caller to
// decide whether to keep going.
type Outcome struct {
	Requested int
	Skipped   int
	Labelled  int
	Written   int
	// Unresolved are tracks that were sent but came back without a label. They
	// are deliberately NOT marked done, so a later pass retries them.
	Unresolved []string
	Usage      llm.Usage
	Cost       float64
}

type Runner struct {
	Provider llm.Provider
	Budget   *llm.Budget
	Store    Store
	Tagger   Tagger
	System   string
	Opts     Options
	// Discounted must match how the request was actually submitted; the same
	// model costs different amounts through the batch endpoint.
	Discounted bool
}

func recordKey(id string) string { return "track:" + id }

// Process labels one batch and writes tags.
//
// It returns partial progress alongside an error wherever possible: a batch that
// spent tokens and then failed must still charge the budget, or the spend cap
// silently stops counting money that was actually spent.
func (r *Runner) Process(items []Item) (*Outcome, error) {
	out := &Outcome{Requested: len(items)}
	if len(items) == 0 {
		return out, nil
	}

	pending := make([]Item, 0, len(items))
	for _, it := range items {
		skip, err := r.alreadyDone(it)
		if err != nil {
			return out, err
		}
		if skip {
			out.Skipped++
			continue
		}
		pending = append(pending, it)
	}
	if len(pending) == 0 {
		return out, nil
	}

	// Check the cap BEFORE spending. The projection is deliberately conservative;
	// see Budget for why the cap can overshoot by at most one batch.
	if r.Budget != nil {
		projected, err := r.project(len(pending))
		if err != nil {
			return out, err
		}
		if err := r.Budget.CanAfford(projected); err != nil {
			return out, err
		}
	}

	tracks := make([]llm.Track, len(pending))
	for i, it := range pending {
		tracks[i] = it.Track
	}

	res, labelErr := r.Provider.Label(r.System, tracks)
	// Charge whatever was spent, even on failure. A truncated batch still burned
	// tokens, and a budget that only counts successes is not counting money.
	if res != nil {
		out.Usage = res.Usage
		if r.Budget != nil {
			if err := r.Budget.Record(res.Usage, r.Discounted); err != nil {
				return out, err
			}
		}
		if c, err := llm.Cost(r.Provider.ModelID(), res.Usage, r.Discounted); err == nil {
			out.Cost = c
		}
	}
	if labelErr != nil {
		return out, labelErr
	}

	byID := make(map[string]llm.Label, len(res.Labels))
	for _, l := range res.Labels {
		byID[l.ID] = l
	}

	for _, it := range pending {
		label, ok := byID[it.Track.ID]
		if !ok {
			// The model skipped it, or the response was truncated. Leaving it
			// unrecorded is the whole point: marking it done would mean it is
			// never labelled and the gap is invisible.
			out.Unresolved = append(out.Unresolved, it.Track.ID)
			continue
		}
		out.Labelled++

		rec := Record{
			ID:        label.ID,
			Energy:    label.Energy,
			Valence:   label.Valence,
			Intensity: label.Intensity,
			Organic:   label.Organic,
			Freeform:  label.Moods,
			Canonical: mood.TagValues(label.Moods),
			Times:     label.Times,
		}

		if !r.Opts.DryRun && len(rec.Canonical) > 0 && it.Path != "" {
			if _, err := r.Tagger.WriteMood(it.Path, rec.Canonical); err != nil {
				return out, fmt.Errorf("writing %s: %w", it.Path, err)
			}
			rec.Tagged = true
			rec.TagsWritten = rec.Canonical
			out.Written++
		}

		if err := r.save(rec); err != nil {
			return out, err
		}
	}
	return out, nil
}

// alreadyDone reports whether a track can be skipped.
func (r *Runner) alreadyDone(it Item) (bool, error) {
	if !r.Opts.SkipTagged {
		return false, nil
	}
	// A prior run of this plugin.
	if raw, ok, err := r.Store.Get(recordKey(it.Track.ID)); err != nil {
		return false, err
	} else if ok {
		var rec Record
		if err := json.Unmarshal(raw, &rec); err == nil && rec.Tagged {
			return true, nil
		}
	}
	// A tag written by something else entirely - Picard, beets, a manual edit.
	// The plugin adds MOOD, it does not own the file.
	if it.Path != "" && r.Tagger != nil {
		existing, err := r.Tagger.ReadMood(it.Path)
		if err != nil {
			return false, err
		}
		if len(existing) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (r *Runner) save(rec Record) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return r.Store.Set(recordKey(rec.ID), raw)
}

// project estimates a batch's cost for the pre-flight cap check.
func (r *Runner) project(n int) (float64, error) {
	// Assume no cache hit, which overestimates and therefore fails safe.
	u := llm.Usage{
		Input:  int64(len(r.System)/3) + int64(n)*80,
		Output: int64(n) * llm.OutputTokensPerTrack,
	}
	return llm.Cost(r.Provider.ModelID(), u, r.Discounted)
}

// ErrNoProvider is returned when the runner is misconfigured.
var ErrNoProvider = errors.New("runner: no provider configured")
