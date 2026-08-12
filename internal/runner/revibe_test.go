package runner

import (
	"errors"
	"testing"

	"github.com/312-dev/navidrome-mood/internal/mood"
)

// labelledTags is a file carrying a complete label, positioned wherever the
// caller asks. Built by hand rather than through tagsFor so a test can place a
// track at a coordinate no vocabulary term anchors.
func labelledTags(p mood.Point, vibes ...string) map[string][]string {
	return map[string][]string{
		TagEnergy:       {itoa(p.Energy)},
		TagValence:      {itoa(p.Valence)},
		TagIntensity:    {itoa(p.Intensity)},
		TagAcousticness: {itoa(p.Acousticness)},
		TagDensity:      {itoa(p.Density)},
		TagTempo:        {string(p.Tempo)},
		TagVocal:        {string(p.Vocal)},
		TagMood:         {"calm"},
		TagVibe:         vibes,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// centreOf returns a point at a region's exact centre, satisfying its
// constraints, so it is certainly a member however the radius moves.
func centreOf(name string) mood.Point {
	r := mood.Regions[name]
	p := mood.Point{Axes: r.Centre, Tempo: "mid", Vocal: "sung"}
	if len(r.Tempo) > 0 {
		p.Tempo = r.Tempo[0]
	}
	if len(r.Vocal) > 0 {
		p.Vocal = r.Vocal[0]
	}
	return p
}

// The reason this whole path exists. A radius is a calibration and calibrations
// move; when one tightens, the tracks it no longer reaches have to LOSE the tag,
// not merely fail to gain it. A vibe left behind is a claim about a track that
// nothing in the file can contradict.
func TestRevibeRemovesAVibeTheTrackNoLongerQualifiesFor(t *testing.T) {
	// Far from every centre, so it is in no region at any sane radius.
	stranded := mood.Point{
		Axes:  mood.Axes{Energy: 0, Valence: 100, Intensity: 100, Acousticness: 0, Density: 0},
		Tempo: "still", Vocal: "rapped",
	}
	if got := mood.VibesFor(stranded, MaxVibes); len(got) != 0 {
		t.Fatalf("fixture is meant to be in no region, landed in %v", got)
	}

	tagger := newTagger()
	items := []Item{{Path: "a.flac", Tags: labelledTags(stranded, "driving", "late night")}}

	out := Revibe(items, tagger, false)

	if out.Cleared != 1 || out.Written != 0 {
		t.Fatalf("got cleared=%d written=%d, want cleared=1 written=0", out.Cleared, out.Written)
	}
	wrote, ok := tagger.written["a.flac"]
	if !ok {
		t.Fatal("nothing was written, so the stale vibes are still on the file")
	}
	if len(wrote) != 2 {
		t.Fatalf("the write named %d tags, want only %s and %s: recomputation must "+
			"not touch a tag it did not produce", len(wrote), TagVibe, TagVibeNear)
	}
	if v, ok := wrote[TagVibe]; !ok || len(v) != 0 {
		t.Fatalf("wrote %v for %s, want the name present with no values, which is "+
			"what removes the tag", wrote, TagVibe)
	}
	// This fixture is stranded beyond every fringe too, so the near tag is named
	// and empty for the same reason the membership one is.
	if v, ok := wrote[TagVibeNear]; !ok || len(v) != 0 {
		t.Fatalf("wrote %v for %s, want the name present with no values", wrote, TagVibeNear)
	}
}

// The nine tags this does not own must come back untouched. Recomputation runs
// over a whole library at once, so a bug that widened its blast radius would
// take the model's verdict with it and there would be nothing to restore from.
// The two region tags are excluded because both are computed here from the axes
// rather than supplied by the model.
func TestRevibeNamesOnlyTheRegionTags(t *testing.T) {
	p := centreOf("melancholy")
	tagger := newTagger()
	before := labelledTags(p)
	items := []Item{{Path: "a.flac", Tags: before}}

	Revibe(items, tagger, false)

	wrote := tagger.written["a.flac"]
	for _, name := range TagNames {
		if name == TagVibe || name == TagVibeNear {
			continue
		}
		if _, ok := wrote[name]; ok {
			t.Errorf("the write named %s, which recomputation has no opinion about", name)
		}
	}
}

// A second pass over an unchanged library has to be free of writes. Every write
// rewrites a FLAC's metadata block and updates its mtime, which would send the
// whole library back through Navidrome's scanner and through auto-sync on every
// recomputation.
func TestRevibeWritesNothingWhenTheVibesAlreadyMatch(t *testing.T) {
	p := centreOf("heavy")
	want := make([]string, 0, MaxVibes)
	for _, m := range mood.VibesFor(p, MaxVibes) {
		want = append(want, m.Vibe)
	}
	if len(want) == 0 {
		t.Fatal("a point at the centre of heavy is in no region, so the fixture proves nothing")
	}

	tagger := newTagger()
	out := Revibe([]Item{{Path: "a.flac", Tags: labelledTags(p, want...)}}, tagger, false)

	if out.Unchanged != 1 || out.Written != 0 || out.Cleared != 0 {
		t.Fatalf("got %+v, want one unchanged and no writes", out)
	}
	if len(tagger.written) != 0 {
		t.Fatalf("wrote %v, want nothing", tagger.written)
	}
}

// Order is part of the value. VibesFor returns nearest-centre first, so a track
// whose regions have swapped ranks has changed even though the same names are
// present, and a comparison that ignored order would leave the old ranking.
func TestRevibeRewritesWhenOnlyTheOrderChanged(t *testing.T) {
	// Sits between the centres of `golden hour` and `focus`, which is one of the
	// few overlaps left once the radii are fitted to a real library. Regions
	// still overlap by construction, but far less than they did when a radius
	// was wider than the typical gap between any two tracks.
	p := mood.Point{
		Axes:  mood.Axes{Energy: 52, Valence: 57, Intensity: 42, Acousticness: 51, Density: 48},
		Tempo: "still", Vocal: "instrumental",
	}
	want := make([]string, 0, MaxVibes)
	for _, m := range mood.VibesFor(p, MaxVibes) {
		want = append(want, m.Vibe)
	}
	if len(want) < 2 {
		t.Fatalf("the fixture is in %v, and this test needs a point in two regions "+
			"to have anything to reorder", want)
	}
	swapped := append([]string{want[1], want[0]}, want[2:]...)

	tagger := newTagger()
	out := Revibe([]Item{{Path: "a.flac", Tags: labelledTags(p, swapped...)}}, tagger, false)

	if out.Written != 1 {
		t.Fatalf("got %+v, want the file rewritten so the nearest region is first again", out)
	}
}

// A track with no complete label is work for a labelling run, not for this one.
// Recomputation derives the vibe from axes that must already exist; there is no
// path here that would consult a model to fill the gap, and a partial write
// would leave a vibe with no axes to justify it.
func TestRevibeSkipsATrackWithNoCompleteLabel(t *testing.T) {
	tagger := newTagger()
	out := Revibe([]Item{
		{Path: "bare.flac", Tags: nil},
		{Path: "moodonly.flac", Tags: map[string][]string{TagMood: {"calm"}}},
		{Path: "noaxes.flac", Tags: map[string][]string{
			TagMood: {"calm"}, TagTempo: {"mid"}, TagVocal: {"sung"},
		}},
	}, tagger, false)

	if out.Unlabelled != 3 {
		t.Fatalf("got %+v, want all three counted unlabelled", out)
	}
	if len(tagger.written) != 0 {
		t.Fatalf("wrote %v for tracks with nothing to recompute from", tagger.written)
	}
}

// Preview reports the size of the change rather than a row of zeroes, so
// somebody can see what a radius edit would do before it touches a file.
func TestRevibeDryRunCountsWithoutWriting(t *testing.T) {
	p := centreOf("party")
	tagger := newTagger()
	out := Revibe([]Item{{Path: "a.flac", Tags: labelledTags(p, "wind down")}}, tagger, true)

	if out.Written+out.Cleared != 1 {
		t.Fatalf("got %+v, want the change counted", out)
	}
	if len(tagger.written) != 0 {
		t.Fatalf("preview wrote %v", tagger.written)
	}
}

// A file that cannot be written is reported per path and does not stop the
// batch. One unwritable file in a library of nine thousand should cost that one
// file, not the pass.
func TestRevibeReportsWriteFailuresAndKeepsGoing(t *testing.T) {
	tagger := newTagger()
	tagger.writeErr = errors.New("read-only file system")

	out := Revibe([]Item{
		{Path: "a.flac", Tags: labelledTags(centreOf("party"), "wind down")},
		{Path: "b.flac", Tags: labelledTags(centreOf("heavy"), "wind down")},
	}, tagger, false)

	if len(out.Errors) != 2 || out.Written != 0 {
		t.Fatalf("got %+v, want both paths in Errors and nothing counted written", out)
	}
	if _, ok := out.Errors["b.flac"]; !ok {
		t.Error("the second file was not attempted, so one failure ended the batch")
	}
}

// PointFromTags is the inverse of the write and has to accept exactly what
// FullyLabelled accepts, including the case another tool may have written.
func TestPointFromTagsRoundTripsAndFoldsCase(t *testing.T) {
	want := mood.Point{
		Axes:  mood.Axes{Energy: 41, Valence: 22, Intensity: 63, Acousticness: 8, Density: 77},
		Tempo: "driving", Vocal: "sung",
	}
	tags := labelledTags(want)
	tags[TagTempo] = []string{"Driving"}
	tags[TagVocal] = []string{" Sung "}

	got, ok := PointFromTags(tags)
	if !ok {
		t.Fatal("a complete label was rejected")
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
