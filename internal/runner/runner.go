// Package runner orchestrates one batch of labelling: budget check, provider
// call, vocabulary mapping and tag write.
//
// Nothing per-track is remembered anywhere but the files themselves. Every value
// a verdict produces is written into the FLAC as a tag, so the file answers the
// only question a later pass has to ask - "has this track been judged already" -
// and there is no parallel store to keep in step with it or to run out of room.
//
// The provider and the tagger are behind interfaces so this runs as an ordinary
// Go test. The wasm build wires Tagger to the flac package; tests wire it to a
// fake.
package runner

import (
	"errors"
	"fmt"

	"github.com/312-dev/navidrome-mood/internal/llm"
	"github.com/312-dev/navidrome-mood/internal/mood"
)

// Tagger is the file-writing seam. It is generic over tag names because nine of
// the ten tags carry the geometry the connector actually reads; a mood-only seam
// could only ever write the tenth.
type Tagger interface {
	// WriteTags replaces each named tag with the given values and reports what
	// strategy the write used. A name mapped to no values removes that tag; a name
	// absent from the map is left untouched. A write always names all ten, so a
	// relabel replaces the plugin's whole set rather than merging into it.
	WriteTags(path string, tags map[string][]string) (string, error)
}

// Item is one track to label, paired with the file to tag and whatever that file
// already carries under the plugin's ten names.
//
// Tags comes from the same comment block the metadata in Track was parsed out
// of, so deciding what to skip costs nothing beyond the read the caller had to
// do anyway. A nil map means a file carrying none of the ten, which is a track
// nothing has ever labelled.
type Item struct {
	Track llm.Track
	Path  string
	Tags  map[string][]string
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
	// failed validation. Nothing is written for them, which is what leaves them
	// looking unlabelled to a later pass so it retries them.
	Unresolved []string
	// Rejected explains, per track ID, why a label that did arrive was thrown
	// away. Those IDs also appear in Unresolved; this is what lets a caller say
	// which of the two happened instead of reporting a silent gap.
	Rejected map[string]string
	// MoodOnly counts tracks skipped because they carry a mood tag and none of
	// the seven values a smart playlist filters on. Counted separately from the
	// rest of Skipped because it is the one kind of skip the user can change
	// their mind about, by turning skipTagged off.
	MoodOnly int
	Usage    llm.Usage
	Cost     float64
}

type Runner struct {
	Provider llm.Provider
	Budget   *llm.Budget
	Tagger   Tagger
	System   string
	Opts     Options
	// Discounted must match how the request was actually submitted; the same
	// model costs different amounts through the batch endpoint.
	Discounted bool
}

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
		switch {
		case FullyLabelled(it.Tags):
			// Already done, and re-paying for finished work is the expensive
			// mistake this whole check exists to prevent. Skipped whatever
			// skipTagged says, because that setting is about tags this plugin did
			// not write and these are unmistakably its own.
			out.Skipped++
		case len(nonEmpty(it.Tags[TagMood])) > 0:
			// A mood tag and nothing else. It is either an older version of this
			// plugin or another tool entirely, and no amount of reading can tell
			// which, so the user's answer to "leave existing mood tags alone" is
			// the only thing that can decide it.
			if r.Opts.SkipTagged {
				out.Skipped++
				out.MoodOnly++
				continue
			}
			pending = append(pending, it)
		default:
			pending = append(pending, it)
		}
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
			// The model skipped it, or the response was truncated. Writing nothing
			// is the whole point: a partial write would leave the file looking
			// labelled, and the gap would be invisible from then on.
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

		if r.Opts.DryRun || it.Path == "" {
			// A preview keeps nothing. The tags in the files are the only record
			// this plugin has, so a run that writes none of them buys a count and a
			// cost figure and leaves the library exactly as it found it.
			continue
		}
		if _, err := r.Tagger.WriteTags(it.Path, tagsFor(label)); err != nil {
			return out, fmt.Errorf("writing %s: %w", it.Path, err)
		}
		out.Written++
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

// project estimates a batch's cost for the pre-flight cap check.
func (r *Runner) project(n int) (float64, error) {
	// Assume no cache hit, which overestimates the input side and therefore fails
	// safe there. The output side runs the other way: OutputTokensPerTrack was
	// measured against a reply carrying fewer fields than one now carries, so it
	// reads low. The cap itself still holds, since Budget charges from the usage
	// each reply reports.
	u := llm.Usage{
		Input:  int64(len(r.System)/3) + int64(n)*80,
		Output: int64(n) * llm.OutputTokensPerTrack,
	}
	return llm.Cost(r.Provider.ModelID(), u, r.Discounted)
}

// ErrNoProvider is returned when the runner is misconfigured.
var ErrNoProvider = errors.New("runner: no provider configured")
