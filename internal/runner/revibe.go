package runner

import (
	"strconv"
	"strings"

	"github.com/312-dev/navidrome-mood/internal/mood"
)

// RevibeOutcome reports what one recomputation batch did.
type RevibeOutcome struct {
	Requested int
	// Unlabelled counts files carrying no complete label. There is nothing to
	// recompute from, and no model is consulted to fill the gap: recomputation
	// derives the vibe from axes that already exist, so a track without them is
	// work for a labelling run and not for this one.
	Unlabelled int
	// Unchanged counts tracks whose recomputed vibes match what the file already
	// carries. Nothing is written for them, which is what makes a second pass
	// over an already-current library cost one read per file and no writes.
	Unchanged int
	Written   int
	Cleared   int
	Errors    map[string]string
}

// Revibe recomputes the `vibe` tag from the axes each file already carries.
//
// This exists because the region radii are a calibration, not a constant, and a
// calibration that can only be applied by re-labelling is a calibration nobody
// will ever change: relabelling 9,195 tracks costs about $19 and several hours,
// to re-derive numbers that are already correct. The regions are geometry over
// the five axes, so once those axes are in the files the vibe follows from them
// with no model, no network and no spend. Moving a radius, adding a region or
// renaming one is then a rebuild and one free pass.
//
// It writes ONE tag. The other nine are the model's verdict and this has no
// opinion about them; naming only `vibe` in the write is what guarantees a
// recomputation cannot disturb a label it did not produce.
//
// A track that now belongs to no region has `vibe` named with no values, which
// removes it. That case is the whole point rather than an edge: tightening a
// radius is precisely the change that should take a tag away, and a vibe left
// behind because the write only ever added values would be a stale claim with
// nothing to check it against.
func Revibe(items []Item, tagger Tagger, dryRun bool) RevibeOutcome {
	out := RevibeOutcome{Requested: len(items), Errors: map[string]string{}}

	for _, it := range items {
		point, ok := PointFromTags(it.Tags)
		if !ok {
			out.Unlabelled++
			continue
		}

		want := make([]string, 0, MaxVibes)
		for _, m := range mood.VibesFor(point, MaxVibes) {
			want = append(want, m.Vibe)
		}
		if sameValues(it.Tags[TagVibe], want) {
			out.Unchanged++
			continue
		}
		if dryRun {
			// Counted as it would have been written, so a preview reports the
			// size of the change rather than a row of zeroes.
			if len(want) == 0 {
				out.Cleared++
			} else {
				out.Written++
			}
			continue
		}
		if _, err := tagger.WriteTags(it.Path, map[string][]string{TagVibe: want}); err != nil {
			out.Errors[it.Path] = err.Error()
			continue
		}
		if len(want) == 0 {
			out.Cleared++
		} else {
			out.Written++
		}
	}
	return out
}

// PointFromTags rebuilds a track's position from the tags in its file.
//
// It is the inverse of the write, and it accepts exactly what FullyLabelled
// accepts, so a track either has a complete label that can be recomputed or is
// unlabelled. There is no partial case in between: five axes plus tempo and
// vocal is the smallest set a region can be evaluated against, since the two
// categoricals are hard constraints that no distance can substitute for.
func PointFromTags(tags map[string][]string) (mood.Point, bool) {
	if !FullyLabelled(tags) {
		return mood.Point{}, false
	}
	axis := func(name string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(tags[name][0]))
		return n
	}
	return mood.Point{
		Axes: mood.Axes{
			Energy:       axis(TagEnergy),
			Valence:      axis(TagValence),
			Intensity:    axis(TagIntensity),
			Acousticness: axis(TagAcousticness),
			Density:      axis(TagDensity),
		},
		// Lower-cased to match FullyLabelled, which compares case-insensitively
		// because Navidrome lower-cases field names but not values.
		Tempo: mood.TempoFeel(strings.ToLower(strings.TrimSpace(tags[TagTempo][0]))),
		Vocal: mood.VocalKind(strings.ToLower(strings.TrimSpace(tags[TagVocal][0]))),
	}, true
}

// sameValues compares a file's current vibe values against the recomputed ones.
//
// Order matters and is not incidental: VibesFor returns nearest-region-first and
// a reader may reasonably treat the first value as the primary vibe, so a track
// whose two regions have swapped ranks has genuinely changed. Blank values are
// dropped first, because a tag present with an empty value carries no
// information and the FLAC layer is happy to represent one.
func sameValues(have, want []string) bool {
	have = nonEmpty(have)
	if len(have) != len(want) {
		return false
	}
	for i := range have {
		if !strings.EqualFold(strings.TrimSpace(have[i]), want[i]) {
			return false
		}
	}
	return true
}
