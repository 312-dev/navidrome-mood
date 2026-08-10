package runner

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/312-dev/navidrome-mood/internal/llm"
	"github.com/312-dev/navidrome-mood/internal/mood"
)

// fakeTagger stands in for the FLAC writer. `written` is the instruction it was
// last handed; `existing` is the file that instruction landed on, with removals
// applied - which is what a test asking "did a stale value survive" has to look
// at.
type fakeTagger struct {
	existing map[string]map[string][]string
	written  map[string]map[string][]string
	writeErr error
}

func newTagger() *fakeTagger {
	return &fakeTagger{
		existing: map[string]map[string][]string{},
		written:  map[string]map[string][]string{},
	}
}

func (f *fakeTagger) WriteTags(path string, tags map[string][]string) (string, error) {
	if f.writeErr != nil {
		return "", f.writeErr
	}
	f.written[path] = tags
	if f.existing[path] == nil {
		f.existing[path] = map[string][]string{}
	}
	for n, v := range tags {
		if len(v) == 0 {
			delete(f.existing[path], n)
			continue
		}
		f.existing[path][n] = v
	}
	return "in-place", nil
}

// onFile is what the file carries after every write so far.
func (f *fakeTagger) onFile(path string) map[string][]string { return f.existing[path] }

type stubProvider struct {
	labels []llm.Label
	usage  llm.Usage
	err    error
	called int
	gotN   int
	model  string
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) ModelID() string {
	if s.model != "" {
		return s.model
	}
	return "claude-sonnet-5"
}

func (s *stubProvider) SupportsBatch() bool { return false }
func (s *stubProvider) Label(_ string, tracks []llm.Track) (*llm.Result, error) {
	s.called++
	s.gotN = len(tracks)
	return &llm.Result{Labels: s.labels, Usage: s.usage}, s.err
}

func item(id, path string) Item {
	return Item{Track: llm.Track{ID: id, Title: id}, Path: path}
}

// carrying is the same item with tags already on the file, which is the only
// thing that decides whether it is labelled again.
func carrying(id, path string, tags map[string][]string) Item {
	it := item(id, path)
	it.Tags = tags
	return it
}

// fullSet is what this plugin leaves on a file it has finished with: the five
// axes, tempo, vocal and a mood word, plus the two optional multi-valued tags.
func fullSet() map[string][]string {
	return map[string][]string{
		TagEnergy:       {"70"},
		TagValence:      {"40"},
		TagIntensity:    {"60"},
		TagAcousticness: {"30"},
		TagDensity:      {"55"},
		TagTempo:        {"mid"},
		TagVocal:        {"sung"},
		TagMood:         {"melancholy"},
		TagTime:         {"evening"},
		TagVibe:         {"wind down"},
	}
}

// label is a complete, valid verdict. Tests that care about one field vary it
// from here rather than building a partial label, because a zero-valued tempo or
// vocal is a rejection and would fail every test for the wrong reason.
func label(id string, moods ...string) llm.Label {
	return llm.Label{
		ID: id, Energy: 50, Valence: 50, Intensity: 50,
		Acousticness: 50, Density: 50, Tempo: "mid", Vocal: "sung",
		Moods: moods, Times: []string{"evening"},
	}
}

func newRunner(p llm.Provider, tg Tagger, opts Options) *Runner {
	return &Runner{Provider: p, Tagger: tg, System: "sys", Opts: opts}
}

func TestProcessLabelsAndWritesTags(t *testing.T) {
	l := label("a", "sorrowful", "menacing")
	l.Energy = 70
	p := &stubProvider{labels: []llm.Label{l}}
	tg := newTagger()
	r := newRunner(p, tg, Options{})

	out, err := r.Process([]Item{item("a", "/m/a.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Labelled != 1 || out.Written != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	// sorrowful folds onto melancholy; menacing is already an anchor.
	got := tg.written["/m/a.flac"]
	if m := got[TagMood]; len(m) != 2 || m[0] != "melancholy" || m[1] != "menacing" {
		t.Fatalf("wrote %s=%v, want [melancholy menacing]", TagMood, m)
	}
	// The axes travel with the words. The connector reads them all-or-nothing, so
	// a write carrying only the mood words leaves the track unlabelled as far as
	// anything downstream is concerned.
	if v := got[TagEnergy]; len(v) != 1 || v[0] != "70" {
		t.Fatalf("wrote %s=%v, want [70]", TagEnergy, v)
	}
	if v := got[TagTempo]; len(v) != 1 || v[0] != "mid" {
		t.Fatalf("wrote %s=%v, want [mid]", TagTempo, v)
	}
	// What was written has to read back as done, or the next pass pays again.
	if !FullyLabelled(tg.onFile("/m/a.flac")) {
		t.Fatalf("a file this runner just wrote does not read as labelled: %v",
			tg.onFile("/m/a.flac"))
	}
}

// The single most important correctness property in this package. A truncated or
// partial response returns fewer labels than tracks sent; writing tags for those
// tracks anyway would mean they are never labelled and the gap is invisible.
func TestUnlabelledTracksAreNotMarkedDone(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{label("a", "warm")}}
	tg := newTagger()
	r := newRunner(p, tg, Options{})

	out, err := r.Process([]Item{item("a", "/m/a.flac"), item("b", "/m/b.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Labelled != 1 {
		t.Fatalf("Labelled = %d, want 1", out.Labelled)
	}
	if len(out.Unresolved) != 1 || out.Unresolved[0] != "b" {
		t.Fatalf("Unresolved = %v, want [b]", out.Unresolved)
	}
	if _, ok := tg.written["/m/b.flac"]; ok {
		t.Fatal("unlabelled track was tagged")
	}
}

func TestSkipsTracksAlreadyTaggedByOtherTools(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{label("b", "warm")}}
	tg := newTagger()
	r := newRunner(p, tg, Options{SkipTagged: true})

	// Written by Picard, say: a word and nothing to place it in mood-space.
	picard := carrying("a", "/m/a.flac", map[string][]string{TagMood: {"chill"}})

	out, err := r.Process([]Item{picard, item("b", "/m/b.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Skipped != 1 || out.MoodOnly != 1 {
		t.Fatalf("outcome = %+v, want one skip counted as mood-only", out)
	}
	if p.gotN != 1 {
		t.Fatalf("sent %d tracks to the provider, want 1: skipped tracks cost money", p.gotN)
	}
	if _, ok := tg.written["/m/a.flac"]; ok {
		t.Fatal("clobbered a tag written by another tool")
	}
}

// The rule the whole change exists for: a file carrying the full set is finished,
// and re-paying for finished work is the expensive mistake.
func TestAFullyLabelledTrackIsSkipped(t *testing.T) {
	for _, skipTagged := range []bool{true, false} {
		p := &stubProvider{labels: []llm.Label{label("a", "warm")}}
		tg := newTagger()
		r := newRunner(p, tg, Options{SkipTagged: skipTagged})

		out, err := r.Process([]Item{carrying("a", "/m/a.flac", fullSet())})
		if err != nil {
			t.Fatal(err)
		}
		if out.Skipped != 1 || p.called != 0 {
			t.Fatalf("skipTagged=%v: Skipped = %d after %d provider calls, want 1 and 0",
				skipTagged, out.Skipped, p.called)
		}
		if out.MoodOnly != 0 {
			t.Errorf("skipTagged=%v: a finished track was counted as mood-only", skipTagged)
		}
	}
}

// A track in no vibe region is a normal outcome, so an absent vibe must not make
// a finished track look unfinished. Getting this wrong sends a large slice of any
// library back through paid labelling on every pass, silently.
func TestNoVibeStillCountsAsFullyLabelled(t *testing.T) {
	tags := fullSet()
	delete(tags, TagVibe)
	delete(tags, TagTime)

	p := &stubProvider{labels: []llm.Label{label("a", "warm")}}
	tg := newTagger()
	r := newRunner(p, tg, Options{})

	out, err := r.Process([]Item{carrying("a", "/m/a.flac", tags)})
	if err != nil {
		t.Fatal(err)
	}
	if out.Skipped != 1 || p.called != 0 {
		t.Fatalf("a track with no vibe was relabelled: Skipped = %d, calls = %d",
			out.Skipped, p.called)
	}
}

// The other half of the ambiguity rule. A mood word alone could be an older
// version of this plugin or another tool entirely, so the setting decides.
func TestMoodOnlyIsGovernedBySkipTagged(t *testing.T) {
	moodOnly := func() map[string][]string {
		return map[string][]string{TagMood: {"dark"}}
	}

	p := &stubProvider{labels: []llm.Label{label("a", "warm", "bright")}}
	tg := newTagger()
	r := newRunner(p, tg, Options{SkipTagged: false})
	out, err := r.Process([]Item{carrying("a", "/m/a.flac", moodOnly())})
	if err != nil {
		t.Fatal(err)
	}
	if out.Written != 1 {
		t.Fatalf("Written = %d with skipTagged off, want 1", out.Written)
	}
	// The seven all-or-nothing values are what a mood word on its own lacks.
	got := tg.onFile("/m/a.flac")
	for _, name := range []string{
		TagEnergy, TagValence, TagIntensity, TagAcousticness, TagDensity,
		TagTempo, TagVocal,
	} {
		if len(got[name]) == 0 {
			t.Errorf("relabelling a mood-only track did not write %q", name)
		}
	}

	p = &stubProvider{labels: []llm.Label{label("a", "warm", "bright")}}
	tg = newTagger()
	r = newRunner(p, tg, Options{SkipTagged: true})
	out, err = r.Process([]Item{carrying("a", "/m/a.flac", moodOnly())})
	if err != nil {
		t.Fatal(err)
	}
	if out.Skipped != 1 || out.MoodOnly != 1 || p.called != 0 {
		t.Fatalf("outcome = %+v after %d provider calls, want one mood-only skip and no call",
			out, p.called)
	}
}

// A value the connector would refuse leaves the track unlabelled, because the two
// sides have to agree about what "labelled" means or half a library is invisible.
func TestBrokenValuesMeanNotFullyLabelled(t *testing.T) {
	cases := map[string]func(map[string][]string){
		"axis above 100":    func(m map[string][]string) { m[TagEnergy] = []string{"140"} },
		"axis below zero":   func(m map[string][]string) { m[TagValence] = []string{"-1"} },
		"axis not a number": func(m map[string][]string) { m[TagDensity] = []string{"loud"} },
		"axis missing":      func(m map[string][]string) { delete(m, TagIntensity) },
		"tempo not in the set": func(m map[string][]string) {
			m[TagTempo] = []string{"moderato"}
		},
		"vocal not in the set": func(m map[string][]string) { m[TagVocal] = []string{"screamed"} },
		"no mood word":         func(m map[string][]string) { delete(m, TagMood) },
		"empty mood word":      func(m map[string][]string) { m[TagMood] = []string{" "} },
	}
	for name, break_ := range cases {
		tags := fullSet()
		break_(tags)
		if FullyLabelled(tags) {
			t.Errorf("%s: still reported as fully labelled", name)
		}

		p := &stubProvider{labels: []llm.Label{label("a", "warm")}}
		r := newRunner(p, newTagger(), Options{})
		if _, err := r.Process([]Item{carrying("a", "/m/a.flac", tags)}); err != nil {
			t.Fatal(err)
		}
		if p.called != 1 {
			t.Errorf("%s: the track was not sent for labelling", name)
		}
	}
}

// Whatever the plugin writes has to read back as labelled through the same rule,
// including the tempo and vocal values another tool might have capitalised.
func TestFullyLabelledAcceptsWhatOtherToolsWrite(t *testing.T) {
	tags := fullSet()
	tags[TagTempo] = []string{"Driving"}
	tags[TagVocal] = []string{" INSTRUMENTAL "}
	if !FullyLabelled(tags) {
		t.Fatalf("case and whitespace made a labelled track look unlabelled: %v", tags)
	}
}

// Spend must be charged even when the batch fails, or the cap stops counting
// money that was genuinely spent.
func TestFailedBatchStillChargesTheBudget(t *testing.T) {
	b, err := llm.NewBudget("claude-sonnet-5", 10)
	if err != nil {
		t.Fatal(err)
	}
	p := &stubProvider{
		usage: llm.Usage{Input: 100_000, Output: 50_000},
		err:   errors.New("truncated at max_tokens"),
	}
	r := newRunner(p, newTagger(), Options{})
	r.Budget = b

	_, err = r.Process([]Item{item("a", "/m/a.flac")})
	if err == nil {
		t.Fatal("expected the provider error to surface")
	}
	if b.Spent() <= 0 {
		t.Fatal("failed batch was not charged; the cap would undercount real spend")
	}
}

func TestBudgetStopsTheBatchBeforeSpending(t *testing.T) {
	b, err := llm.NewBudget("claude-sonnet-5", 0.0000001) // effectively zero
	if err != nil {
		t.Fatal(err)
	}
	p := &stubProvider{labels: []llm.Label{label("a", "warm")}}
	r := newRunner(p, newTagger(), Options{})
	r.Budget = b

	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); !errors.Is(err, llm.ErrBudgetExhausted) {
		t.Fatalf("got %v, want ErrBudgetExhausted", err)
	}
	if p.called != 0 {
		t.Fatal("provider was called despite the cap being exhausted; money was spent")
	}
}

func TestDescriptorsOutsideTheVocabularyAreNotTagged(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{label("a", "zorbular", "warm")}}
	tg := newTagger()
	r := newRunner(p, tg, Options{})

	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); err != nil {
		t.Fatal(err)
	}
	if got := tg.written["/m/a.flac"][TagMood]; len(got) != 1 || got[0] != "warm" {
		t.Fatalf("%s = %v, want [warm] only", TagMood, got)
	}
}

// A track whose every descriptor fell outside the vocabulary still has a position
// in mood-space, and that position is what the connector reads. Suppressing the
// whole write because no word survived canonicalisation would throw away seven
// good tags to avoid one empty one.
func TestNoCanonicalTermsStillWritesThePosition(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{label("a", "zorbular")}}
	tg := newTagger()
	r := newRunner(p, tg, Options{})

	out, err := r.Process([]Item{item("a", "/m/a.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Written != 1 {
		t.Fatalf("Written = %d, want 1", out.Written)
	}
	// The name is still in the instruction, carrying no values, which is what
	// removes any stale mood a previous pass left. What must not happen is a mood
	// tag with an empty value ending up on the file.
	if v := tg.written["/m/a.flac"][TagMood]; len(v) != 0 {
		t.Fatalf("wrote %s=%v for a track with no vocabulary term", TagMood, v)
	}
	got := tg.onFile("/m/a.flac")
	if _, ok := got[TagMood]; ok {
		t.Fatalf("file carries an empty %s tag: %v", TagMood, got[TagMood])
	}
	if v := got[TagValence]; len(v) != 1 || v[0] != "50" {
		t.Fatalf("%s = %v, want [50]", TagValence, v)
	}
	if out.Labelled != 1 {
		t.Fatal("should still count as labelled")
	}
	// The price of requiring a mood word: no word means the next pass judges this
	// track again. It is the rare case, and the alternative is being unable to
	// tell a finished track from a partial write.
	if FullyLabelled(got) {
		t.Fatal("a track with no mood word reads as labelled; the skip rule requires one")
	}
}

func TestWriteFailureSurfaces(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{label("a", "warm")}}
	tg := newTagger()
	tg.writeErr = errors.New("disk full")
	r := newRunner(p, tg, Options{})

	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); err == nil {
		t.Fatal("a failed tag write was swallowed")
	}
}

func TestEmptyBatchDoesNothing(t *testing.T) {
	p := &stubProvider{}
	r := newRunner(p, newTagger(), Options{})
	out, err := r.Process(nil)
	if err != nil || out.Requested != 0 || p.called != 0 {
		t.Fatalf("empty batch did something: %v %+v calls=%d", err, out, p.called)
	}
}

// inHeavy sits squarely in mood.Regions["heavy"]: loud, dark, physical.
func inHeavy(id string) llm.Label {
	l := label(id, "brooding", "heavy")
	l.Energy, l.Valence, l.Intensity = 80, 30, 84
	l.Acousticness, l.Density = 28, 78
	l.Tempo, l.Vocal = "driving", "sung"
	l.Times = []string{"late night"}
	return l
}

// betweenRegions sits in none of the fourteen. Regions cover about 7% of the
// space each and leave most of it uncovered, so this is an ordinary place for a
// track to be rather than a failure to classify one.
func betweenRegions(id string) llm.Label {
	l := label(id, "warm", "bright")
	l.Energy, l.Valence, l.Intensity = 100, 100, 0
	l.Acousticness, l.Density = 100, 0
	l.Tempo, l.Vocal = "frantic", "rapped"
	return l
}

// All ten for a track that lands in a region, because the connector reads the
// five axes plus tempo and vocal all-or-nothing: six of those seven written is a
// track it discards whole. This is the assertion that would have caught the
// version of this package that wrote only the mood words.
func TestEveryTagIsWrittenForALabelInARegion(t *testing.T) {
	l := inHeavy("a")
	p := &stubProvider{labels: []llm.Label{l}}
	tg := newTagger()
	r := newRunner(p, tg, Options{})

	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); err != nil {
		t.Fatal(err)
	}
	got := tg.onFile("/m/a.flac")
	for _, name := range TagNames {
		if len(got[name]) == 0 {
			t.Errorf("tag %q was not written", name)
		}
	}
	want := map[string]string{
		TagEnergy: "80", TagValence: "30", TagIntensity: "84",
		TagAcousticness: "28", TagDensity: "78",
		TagTempo: "driving", TagVocal: "sung", TagTime: "late night",
	}
	for name, v := range want {
		if len(got[name]) != 1 || got[name][0] != v {
			t.Errorf("%s = %v, want [%s]", name, got[name], v)
		}
	}
}

// Nine for a track in no region, and nine is the right answer rather than a
// partial write: `vibe` names the regions a track belongs to, and belonging to
// none is a real answer that no value can express. An empty Vorbis comment is not
// the same as an absent one - it would hand the connector a `vibe` key with
// nothing in it to reason about - so the tag is left off the file entirely. The
// other nine are unaffected, and the track stays reachable by axis range and by
// vocabulary term.
func TestATrackInNoRegionGetsNineTagsAndNoVibeKey(t *testing.T) {
	l := betweenRegions("a")
	if got := mood.VibesFor(l.Point(), MaxVibes); len(got) != 0 {
		t.Fatalf("fixture is meant to be in no region but landed in %v", got)
	}

	p := &stubProvider{labels: []llm.Label{l}}
	tg := newTagger()
	r := newRunner(p, tg, Options{})
	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); err != nil {
		t.Fatal(err)
	}
	got := tg.onFile("/m/a.flac")
	if _, ok := got[TagVibe]; ok {
		t.Fatalf("file carries a %s tag for a track in no region: %v", TagVibe, got[TagVibe])
	}
	if len(got) != len(TagNames)-1 {
		t.Fatalf("file carries %d tags, want %d: %v", len(got), len(TagNames)-1, got)
	}
	for _, name := range TagNames {
		if name == TagVibe {
			continue
		}
		if len(got[name]) == 0 {
			t.Errorf("tag %q was not written", name)
		}
	}
}

// The most important property of a relabel. A track that used to sit in
// `melancholy` and now sits nowhere near it must LOSE that value, not merely fail
// to have it rewritten. A stale vibe is confidently wrong, invisible on disk, and
// there is nothing the connector could check it against.
func TestRelabellingLeavesNoStaleValues(t *testing.T) {
	tg := newTagger()

	first := &stubProvider{labels: []llm.Label{inHeavy("a")}}
	r := newRunner(first, tg, Options{})
	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); err != nil {
		t.Fatal(err)
	}
	before := map[string][]string{}
	for k, v := range tg.onFile("/m/a.flac") {
		before[k] = append([]string(nil), v...)
	}
	if len(before[TagVibe]) == 0 || len(before[TagMood]) == 0 {
		t.Fatalf("first pass did not produce the values this test removes: %v", before)
	}

	// Second pass, different music entirely, and SkipTagged off so it relabels.
	second := &stubProvider{labels: []llm.Label{betweenRegions("a")}}
	r = newRunner(second, tg, Options{})
	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); err != nil {
		t.Fatal(err)
	}

	after := tg.onFile("/m/a.flac")
	if _, ok := after[TagVibe]; ok {
		t.Errorf("%s survived a relabel that placed the track in no region: %v",
			TagVibe, after[TagVibe])
	}
	for _, name := range TagNames {
		for _, old := range before[name] {
			for _, now := range after[name] {
				if old == now && name != TagTempo && name != TagVocal {
					t.Errorf("%s still carries %q from the first pass", name, old)
				}
			}
		}
	}
	if v := after[TagEnergy]; len(v) != 1 || v[0] != "100" {
		t.Errorf("%s = %v, want [100] from the second pass", TagEnergy, v)
	}
	if v := after[TagTime]; len(v) != 1 || v[0] != "evening" {
		t.Errorf("%s = %v, want [evening]; the first pass wrote late night", TagTime, v)
	}
}

// The plugin owns ten names and must not reach past them. Anything else in the
// file belongs to whatever wrote it.
func TestRelabellingLeavesForeignTagsAlone(t *testing.T) {
	tg := newTagger()
	tg.existing["/m/a.flac"] = map[string][]string{
		"ARTIST":                {"someone"},
		"REPLAYGAIN_TRACK_GAIN": {"-6.5 dB"},
	}

	p := &stubProvider{labels: []llm.Label{inHeavy("a")}}
	r := newRunner(p, tg, Options{})
	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); err != nil {
		t.Fatal(err)
	}

	instruction := tg.written["/m/a.flac"]
	if len(instruction) != len(TagNames) {
		t.Fatalf("the write named %d tags, want exactly the %d it owns: %v",
			len(instruction), len(TagNames), instruction)
	}
	for name := range instruction {
		if !contains(TagNames, name) {
			t.Errorf("the write named %q, which is not one of the plugin's tags", name)
		}
	}
	got := tg.onFile("/m/a.flac")
	if v := got["ARTIST"]; len(v) != 1 || v[0] != "someone" {
		t.Errorf("ARTIST = %v, want [someone]", v)
	}
	if v := got["REPLAYGAIN_TRACK_GAIN"]; len(v) != 1 || v[0] != "-6.5 dB" {
		t.Errorf("REPLAYGAIN_TRACK_GAIN = %v, want [-6.5 dB]", v)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// The tag names are the seam with navidrome-mcp/src/moodtags.ts. A rename on
// either side leaves files that look labelled and are invisible to every
// playlist the connector builds.
func TestTagNamesMatchTheConnectorContract(t *testing.T) {
	want := []string{
		"mood", "moodenergy", "moodvalence", "moodintensity", "moodacousticness",
		"mooddensity", "moodtempo", "moodvocal", "moodtime", "vibe",
	}
	got := append([]string(nil), TagNames...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tag names = %v, want %v", got, want)
	}
	// Navidrome splits every tag value on these, so a name carrying one would be
	// registered as two tags neither of which the connector asked for.
	for _, n := range TagNames {
		if strings.ContainsAny(n, ",;/") {
			t.Errorf("tag name %q contains a separator Navidrome splits on", n)
		}
	}
}

// Rejected rather than clamped, and routed into the path that keeps the gap
// visible. A clamped 140 becomes a confident 100 that nothing downstream can
// tell apart from a judgement the model actually made.
func TestOutOfRangeAxisIsRejectedNotWritten(t *testing.T) {
	bad := label("a", "warm", "bright")
	bad.Energy = 140
	good := label("b", "warm", "bright")

	p := &stubProvider{labels: []llm.Label{bad, good}}
	tg := newTagger()
	r := newRunner(p, tg, Options{})

	out, err := r.Process([]Item{item("a", "/m/a.flac"), item("b", "/m/b.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Labelled != 1 {
		t.Fatalf("Labelled = %d, want 1", out.Labelled)
	}
	if len(out.Unresolved) != 1 || out.Unresolved[0] != "a" {
		t.Fatalf("Unresolved = %v, want [a]", out.Unresolved)
	}
	if why := out.Rejected["a"]; !strings.Contains(why, "energy") {
		t.Fatalf("rejection does not name the offending field: %q", why)
	}
	if _, ok := tg.written["/m/a.flac"]; ok {
		t.Fatal("a rejected label was written to the file")
	}
	// The good one in the same batch is unaffected.
	if _, ok := tg.written["/m/b.flac"]; !ok {
		t.Fatal("one bad label took the rest of the batch down with it")
	}
}

func TestTempoOutsideTheClosedSetIsRejected(t *testing.T) {
	bad := label("a", "warm", "bright")
	bad.Tempo = "moderato"
	p := &stubProvider{labels: []llm.Label{bad}}
	tg := newTagger()
	r := newRunner(p, tg, Options{})

	out, err := r.Process([]Item{item("a", "/m/a.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Labelled != 0 || len(out.Unresolved) != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	if len(tg.written) != 0 {
		t.Fatal("a track with an unreadable tempo was tagged anyway")
	}
}

// The tokens were genuinely spent whether or not the answer was usable, so a
// rejection must not become a way for the cap to stop counting money.
func TestRejectedLabelsAreStillCharged(t *testing.T) {
	b, err := llm.NewBudget("claude-sonnet-5", 10)
	if err != nil {
		t.Fatal(err)
	}
	bad := label("a", "warm")
	bad.Density = 500
	p := &stubProvider{
		labels: []llm.Label{bad},
		usage:  llm.Usage{Input: 100_000, Output: 50_000},
	}
	r := newRunner(p, newTagger(), Options{})
	r.Budget = b

	out, err := r.Process([]Item{item("a", "/m/a.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Labelled != 0 || len(out.Unresolved) != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	if b.Spent() <= 0 {
		t.Fatal("a rejected label went uncharged; the cap would undercount real spend")
	}
	if out.Cost <= 0 {
		t.Fatalf("Cost = %v, want the spend that actually happened", out.Cost)
	}
}

// The model is never asked for a vibe. It is computed from the axes, so a region
// name the model volunteers has no route into the tag.
func TestVibeComesFromTheGeometryNotTheModel(t *testing.T) {
	l := label("a", "warm", "gentle")
	// mood.Regions["wind down"], centre exactly.
	l.Energy, l.Valence, l.Intensity = 20, 52, 14
	l.Acousticness, l.Density = 78, 24
	l.Tempo, l.Vocal = "still", "sung"
	// Named as if they were descriptors. Neither is a region and neither may
	// reach the vibe tag by this route.
	l.Moods = []string{"party", "workout"}

	p := &stubProvider{labels: []llm.Label{l}}
	tg := newTagger()
	r := newRunner(p, tg, Options{})

	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); err != nil {
		t.Fatal(err)
	}
	vibes := tg.written["/m/a.flac"][TagVibe]
	if len(vibes) == 0 || vibes[0] != "wind down" {
		t.Fatalf("vibe = %v, want wind down first: it is the region the axes land in", vibes)
	}
	for _, v := range vibes {
		if v == "party" || v == "workout" {
			t.Fatalf("a descriptor the model wrote reached the vibe tag: %v", vibes)
		}
	}
	if len(vibes) > MaxVibes {
		t.Fatalf("vibe carries %d regions, capped at %d", len(vibes), MaxVibes)
	}

	// Same geometry, same answer, without consulting a playlist or any history.
	want := mood.VibesFor(l.Point(), MaxVibes)
	if len(want) != len(vibes) {
		t.Fatalf("vibe = %v, but VibesFor says %+v", vibes, want)
	}
	for i, m := range want {
		if vibes[i] != m.Vibe {
			t.Fatalf("vibe[%d] = %q, want %q", i, vibes[i], m.Vibe)
		}
	}
}

// A preview protects the files, and because the files are the only record, it
// leaves nothing behind at all. The next real run pays for the same tracks again,
// which is worth being explicit about: it is the difference between a preview
// being cheap and a preview being free, and it is neither.
func TestDryRunWritesNothingAtAll(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{inHeavy("a")}}
	tg := newTagger()
	r := newRunner(p, tg, Options{DryRun: true})

	out, err := r.Process([]Item{item("a", "/m/a.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Labelled != 1 {
		t.Fatalf("Labelled = %d, want 1: a preview still pays for the judgement", out.Labelled)
	}
	if out.Written != 0 || len(tg.written) != 0 {
		t.Fatalf("dry run wrote to %d files", len(tg.written))
	}
}
