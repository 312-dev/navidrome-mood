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

// Tagger is the file-writing seam. It is generic over tag names because nine of
// the ten tags carry the geometry the connector actually reads; a mood-only seam
// could only ever write the tenth.
type Tagger interface {
	// ReadTags returns the values present for the named tags, omitting names the
	// file does not carry. Used to spot tracks tagged by Picard or beets so they
	// can be left alone.
	ReadTags(path string, names ...string) (map[string][]string, error)
	// WriteTags replaces each named tag with the given values and reports what
	// strategy the write used. A name mapped to no values removes that tag; a name
	// absent from the map is left untouched. Record.Tags always names all ten, so
	// a relabel replaces the plugin's whole set rather than merging into it.
	WriteTags(path string, tags map[string][]string) (string, error)
}

// Item is one track to label, paired with the file to tag.
type Item struct {
	Track llm.Track
	Path  string
}

// RecordSchema is the version stamped into every Record written, and the version
// alreadyDone will trust a `tagged: true` from.
//
// It exists because a record is what makes skipTagged skip. Records written
// before the ten-tag write carry the mood words and nothing else, yet they
// deserialise cleanly into the struct below with the four new axes reading 0 and
// tempo and vocal reading "" - a track that is nine tenths unlabelled, reporting
// itself done. Bumping this is what sends those tracks back through labelling
// instead. There is no in-place migration because the missing fields were never
// judged; only another pass can supply them.
const RecordSchema = 2

// Record is what is remembered per track. The free-form descriptors are kept
// verbatim alongside the canonical ones, so the controlled vocabulary can be
// revised later without re-paying for inference.
type Record struct {
	Schema       int      `json:"schema"`
	ID           string   `json:"id"`
	Energy       int      `json:"energy"`
	Valence      int      `json:"valence"`
	Intensity    int      `json:"intensity"`
	Acousticness int      `json:"acousticness"`
	Density      int      `json:"density"`
	Tempo        string   `json:"tempo"`
	Vocal        string   `json:"vocal"`
	Freeform     []string `json:"freeform"`
	Canonical    []string `json:"canonical"`
	Times        []string `json:"times,omitempty"`
	// Vibes are the regions the axes above fall in. Stored alongside the axes so
	// a change to the region geometry can be replayed from records rather than
	// re-bought from the model.
	Vibes       []string `json:"vibes,omitempty"`
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
	// Unresolved are tracks that were sent but came back without a usable label,
	// either because the reply omitted them or because what it said about them
	// failed validation. They are deliberately NOT marked done, so a later pass
	// retries them.
	Unresolved []string
	// Rejected explains, per track ID, why a label that did arrive was thrown
	// away. Those IDs also appear in Unresolved; this is what lets a caller say
	// which of the two happened instead of reporting a silent gap.
	Rejected map[string]string
	Usage    llm.Usage
	Cost     float64
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

// RecordPrefix is the kvstore key prefix for per-track records. Exported so the
// incremental sync can list known tracks in one call instead of probing per file.
const RecordPrefix = "track:"

func recordKey(id string) string { return RecordPrefix + id }

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
		if err := label.Validate(); err != nil {
			// Down the same path as a missing label, and for the same reason. The
			// alternative is clamping, which turns a wrong answer into a
			// well-formed one that no later pass has any way to spot.
			out.Unresolved = append(out.Unresolved, it.Track.ID)
			if out.Rejected == nil {
				out.Rejected = map[string]string{}
			}
			out.Rejected[it.Track.ID] = err.Error()
			continue
		}
		out.Labelled++

		rec := Record{
			Schema:       RecordSchema,
			ID:           label.ID,
			Energy:       label.Energy,
			Valence:      label.Valence,
			Intensity:    label.Intensity,
			Acousticness: label.Acousticness,
			Density:      label.Density,
			Tempo:        label.Tempo,
			Vocal:        label.Vocal,
			Freeform:     label.Moods,
			Canonical:    mood.TagValues(label.Moods, MoodTermCap),
			Times:        llm.KnownTimes(label.Times),
			Vibes:        vibesFor(label),
		}

		if !r.Opts.DryRun && it.Path != "" {
			tags := rec.Tags()
			if _, err := r.Tagger.WriteTags(it.Path, tags); err != nil {
				return out, fmt.Errorf("writing %s: %w", it.Path, err)
			}
			rec.Tagged = true
			rec.TagsWritten = writtenNames(tags)
			out.Written++
		}

		if err := r.save(rec); err != nil {
			return out, err
		}
	}
	return out, nil
}

// vibesFor names the regions a label's axes fall in.
//
// The model is never asked for these. Region membership is a measurement against
// a defined area of mood-space, not a prediction about one, so there is no
// accuracy figure to attach to it and no way for it to be wrong about a track it
// has not heard of. It also works identically on a library with no playlists and
// no listening history, because neither is consulted - which is what makes the
// vibe tag mean the same thing on a stranger's server as on this one.
func vibesFor(label llm.Label) []string {
	matches := mood.VibesFor(label.Point(), MaxVibes)
	if len(matches) == 0 {
		// A normal answer. Plenty of music sits between the regions, and those
		// tracks are still reachable by axis range and by vocabulary term.
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.Vibe)
	}
	return out
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
			if rec.Schema >= RecordSchema {
				return true, nil
			}
			// An older record means this plugin wrote the mood tag itself and
			// nothing else. Relabel, and return here rather than falling through:
			// the check below would otherwise read that same mood tag, take it for
			// another tool's work, and skip the track forever.
			return false, nil
		}
	}
	// A tag written by something else entirely - Picard, beets, a manual edit.
	// The plugin adds mood tags, it does not own the file.
	if it.Path != "" && r.Tagger != nil {
		existing, err := r.Tagger.ReadTags(it.Path, TagMood)
		if err != nil {
			return false, err
		}
		if len(existing[TagMood]) > 0 {
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
	// Assume no cache hit, which overestimates the input side and therefore fails
	// safe there. The output side runs the other way: OutputTokensPerTrack was
	// measured on a smaller record and now reads low. The cap itself still holds,
	// since Budget charges from the usage each reply reports.
	u := llm.Usage{
		Input:  int64(len(r.System)/3) + int64(n)*80,
		Output: int64(n) * llm.OutputTokensPerTrack,
	}
	return llm.Cost(r.Provider.ModelID(), u, r.Discounted)
}

// ErrNoProvider is returned when the runner is misconfigured.
var ErrNoProvider = errors.New("runner: no provider configured")
