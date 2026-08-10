package runner

import (
	"sort"
	"strconv"
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

// TagNames lists all ten in sorted order, for callers that need to name the whole
// set rather than one tag.
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

// Tags renders the record as the write to perform, naming all ten every time.
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
func (r Record) Tags() map[string][]string {
	return map[string][]string{
		TagEnergy:       {strconv.Itoa(r.Energy)},
		TagValence:      {strconv.Itoa(r.Valence)},
		TagIntensity:    {strconv.Itoa(r.Intensity)},
		TagAcousticness: {strconv.Itoa(r.Acousticness)},
		TagDensity:      {strconv.Itoa(r.Density)},
		TagTempo:        {r.Tempo},
		TagVocal:        {r.Vocal},
		TagMood:         r.Canonical,
		TagTime:         r.Times,
		TagVibe:         r.Vibes,
	}
}

// writtenNames lists the tags a write actually left on the file, which is not the
// same as the names it was given: a name with no values is a removal.
//
// Sorted, because Go randomises map iteration and this is recorded per track.
func writtenNames(tags map[string][]string) []string {
	names := make([]string, 0, len(tags))
	for n, v := range tags {
		if len(v) > 0 {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}
