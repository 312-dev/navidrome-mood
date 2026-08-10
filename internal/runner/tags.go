package runner

import (
	"strconv"
	"strings"

	"github.com/312-dev/navidrome-mood/internal/llm"
	"github.com/312-dev/navidrome-mood/internal/mood"
)

// The ten tag names written into every labelled file.
//
// This list is the entire seam between this plugin and the navidrome-mcp
// connector, which holds the same ten names in src/moodtags.ts and reads them
// back. Renaming one here alone leaves files that look labelled on disk and are
// invisible to every playlist the connector builds, with no error anywhere.
//
// Navidrome splits every tag value on `,;/`, so no value written under these may
// contain one. mood.Validate is the gate for the vocabulary and the region names.
//
// Nine of the ten do not exist until the user declares them in Navidrome's config
// under a Tags map; only `mood` is built in. An undeclared tag is dropped by the
// scanner with no error, so writing one is necessary but not sufficient. Verified
// 2026-08-09 against Navidrome 0.63.2.
const (
	TagMood         = "mood"
	TagEnergy       = "moodenergy"
	TagValence      = "moodvalence"
	TagIntensity    = "moodintensity"
	TagAcousticness = "moodacousticness"
	TagDensity      = "mooddensity"
	TagTempo        = "moodtempo"
	TagVocal        = "moodvocal"
	TagTime         = "moodtime"
	TagVibe         = "vibe"
)

// TagNames lists all ten, for callers that need to name the whole set rather
// than one tag: the write, and the read that decides what is already labelled.
var TagNames = []string{
	TagMood, TagEnergy, TagValence, TagIntensity, TagAcousticness,
	TagDensity, TagTempo, TagVocal, TagTime, TagVibe,
}

// MoodTermCap is how many vocabulary terms reach the `mood` tag. Two to four is
// what the contract asks the model for and what the request schema enforces; this
// is the cap on what survives canonicalisation, where several descriptors can
// fold onto the same term.
const MoodTermCap = 4

// MaxVibes caps the `vibe` tag. Regions overlap by construction - golden hour and
// dinner share most of their volume - so a central track can sit in several, and
// listing every one of them turns the tag into noise rather than a shortcut. The
// nearest three are the ones a playlist would actually reach for.
const MaxVibes = 3

// tagsFor renders a verdict as the write to perform, naming all ten every time.
//
// Naming all ten is what makes a relabel a REPLACE rather than a merge. A track
// that used to land in `melancholy` and now lands nowhere near it must lose that
// vibe value, not merely fail to have it rewritten: a stale tag is confidently
// wrong, invisible on disk, and there is nothing the connector could check it
// against. The three multi-valued tags therefore appear with no values when the
// track has none, which is the instruction to remove them.
//
// The seven all-or-nothing tags always carry a value. The connector treats a
// track missing any one of them as unlabelled, so writing six of seven is worse
// than writing none.
//
// Ten names is also the blast radius. Nothing else in the file is named here, so
// nothing else can be touched.
func tagsFor(l llm.Label) map[string][]string {
	return map[string][]string{
		TagEnergy:       {strconv.Itoa(l.Energy)},
		TagValence:      {strconv.Itoa(l.Valence)},
		TagIntensity:    {strconv.Itoa(l.Intensity)},
		TagAcousticness: {strconv.Itoa(l.Acousticness)},
		TagDensity:      {strconv.Itoa(l.Density)},
		TagTempo:        {l.Tempo},
		TagVocal:        {l.Vocal},
		TagMood:         mood.TagValues(l.Moods, MoodTermCap),
		TagTime:         llm.KnownTimes(l.Times),
		TagVibe:         vibesFor(l),
	}
}

// FullyLabelled reports whether a file's own tags already carry a complete
// label, which is what makes a second pass over that track free.
//
// The rule is the connector's rule. navidrome-mcp/src/moodtags.ts reads the five
// axes plus moodtempo and moodvocal all-or-nothing and treats moodtime and vibe
// as optional, and the two sides have to agree or a track one of them calls
// labelled is unlabelled to the other. `vibe` in particular must stay optional:
// belonging to no region is a normal outcome that no value can express, so
// requiring it would send a large slice of any library back through paid
// labelling on every pass.
//
// A mood word is required on top of the connector's seven, because `mood` is the
// tag the "leave existing mood tags alone" setting is defined against and the
// two rules have to meet. The cost is that a track whose every descriptor fell
// outside the 52-term vocabulary is judged again on the next pass.
func FullyLabelled(tags map[string][]string) bool {
	for _, name := range []string{
		TagEnergy, TagValence, TagIntensity, TagAcousticness, TagDensity,
	} {
		if !validAxis(tags[name]) {
			return false
		}
	}
	return inClosedSet(tags[TagTempo], mood.TempoFeels) &&
		inClosedSet(tags[TagVocal], mood.VocalKinds) &&
		len(nonEmpty(tags[TagMood])) > 0
}

// validAxis accepts the integer 0-100 this plugin writes and nothing else. A
// value outside that range means the writer and the reader disagree about
// something, and the connector rejects it too rather than clamping.
func validAxis(values []string) bool {
	if len(values) == 0 {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(values[0]))
	return err == nil && n >= 0 && n <= 100
}

// inClosedSet matches a single-valued tag against one of the closed sets,
// case-insensitively: Navidrome lower-cases field names but not values, and
// another tool may well have written "Sung".
func inClosedSet[T ~string](values []string, allowed []T) bool {
	if len(values) == 0 {
		return false
	}
	got := strings.ToLower(strings.TrimSpace(values[0]))
	for _, a := range allowed {
		if string(a) == got {
			return true
		}
	}
	return false
}

// nonEmpty drops blank values. A tag present with an empty value carries no
// information, and the FLAC layer is happy to represent one.
func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}
