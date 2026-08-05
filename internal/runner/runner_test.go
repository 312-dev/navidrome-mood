package runner

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/312-dev/navidrome-mood/internal/llm"
)

type memStore struct {
	m       map[string][]byte
	setFail error
}

func newStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) Get(k string) ([]byte, bool, error) {
	v, ok := s.m[k]
	return v, ok, nil
}

func (s *memStore) Set(k string, v []byte) error {
	if s.setFail != nil {
		return s.setFail
	}
	s.m[k] = v
	return nil
}

type fakeTagger struct {
	existing map[string][]string
	written  map[string][]string
	writeErr error
}

func newTagger() *fakeTagger {
	return &fakeTagger{existing: map[string][]string{}, written: map[string][]string{}}
}

func (f *fakeTagger) ReadMood(path string) ([]string, error) { return f.existing[path], nil }

func (f *fakeTagger) WriteMood(path string, v []string) (string, error) {
	if f.writeErr != nil {
		return "", f.writeErr
	}
	f.written[path] = v
	return "in-place", nil
}

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

func newRunner(p llm.Provider, st Store, tg Tagger, opts Options) *Runner {
	return &Runner{Provider: p, Store: st, Tagger: tg, System: "sys", Opts: opts}
}

func TestProcessLabelsAndWritesTags(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{
		{ID: "a", Energy: 70, Moods: []string{"bleak", "menacing"}},
	}}
	st, tg := newStore(), newTagger()
	r := newRunner(p, st, tg, Options{})

	out, err := r.Process([]Item{item("a", "/m/a.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Labelled != 1 || out.Written != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	// bleak canonicalises to dark; menacing is already vocabulary.
	got := tg.written["/m/a.flac"]
	if len(got) != 2 || got[0] != "dark" || got[1] != "menacing" {
		t.Fatalf("wrote %v, want [dark menacing]", got)
	}

	// The free-form descriptors must survive, so the enum can be revised later
	// without paying for inference again.
	raw, ok, _ := st.Get(recordKey("a"))
	if !ok {
		t.Fatal("no record stored")
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.Freeform) != 2 || rec.Freeform[0] != "bleak" {
		t.Fatalf("free-form descriptors lost: %+v", rec.Freeform)
	}
	if !rec.Tagged {
		t.Fatal("record does not reflect that the file was tagged")
	}
}

// The single most important correctness property in this package. A truncated or
// partial response returns fewer labels than tracks sent; marking those tracks
// done would mean they are never labelled and the gap is invisible.
func TestUnlabelledTracksAreNotMarkedDone(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{{ID: "a", Moods: []string{"warm"}}}}
	st, tg := newStore(), newTagger()
	r := newRunner(p, st, tg, Options{})

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
	if _, ok, _ := st.Get(recordKey("b")); ok {
		t.Fatal("unlabelled track was recorded; a later pass would skip it forever")
	}
	if _, ok := tg.written["/m/b.flac"]; ok {
		t.Fatal("unlabelled track was tagged")
	}
}

func TestSkipsTracksAlreadyTaggedByOtherTools(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{{ID: "b", Moods: []string{"warm"}}}}
	st, tg := newStore(), newTagger()
	tg.existing["/m/a.flac"] = []string{"chill"} // written by Picard, say
	r := newRunner(p, st, tg, Options{SkipTagged: true})

	out, err := r.Process([]Item{item("a", "/m/a.flac"), item("b", "/m/b.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", out.Skipped)
	}
	if p.gotN != 1 {
		t.Fatalf("sent %d tracks to the provider, want 1: skipped tracks cost money", p.gotN)
	}
	if _, ok := tg.written["/m/a.flac"]; ok {
		t.Fatal("clobbered a tag written by another tool")
	}
}

func TestRerunIsFreeWhenEverythingIsAlreadyDone(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{{ID: "a", Moods: []string{"warm"}}}}
	st, tg := newStore(), newTagger()
	r := newRunner(p, st, tg, Options{SkipTagged: true})

	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := p.called

	out, err := r.Process([]Item{item("a", "/m/a.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if p.called != callsAfterFirst {
		t.Fatal("second run called the provider again; a re-run should cost nothing")
	}
	if out.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", out.Skipped)
	}
}

func TestDryRunLabelsButNeverWrites(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{{ID: "a", Moods: []string{"warm"}}}}
	st, tg := newStore(), newTagger()
	r := newRunner(p, st, tg, Options{DryRun: true})

	out, err := r.Process([]Item{item("a", "/m/a.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Labelled != 1 {
		t.Fatalf("dry run did not label: %+v", out)
	}
	if out.Written != 0 || len(tg.written) != 0 {
		t.Fatalf("dry run wrote to %d files", len(tg.written))
	}
	// The label is still saved, so a later real run costs nothing.
	if _, ok, _ := st.Get(recordKey("a")); !ok {
		t.Fatal("dry run discarded the label, so the real run would pay again")
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
	r := newRunner(p, newStore(), newTagger(), Options{})
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
	p := &stubProvider{labels: []llm.Label{{ID: "a", Moods: []string{"warm"}}}}
	r := newRunner(p, newStore(), newTagger(), Options{})
	r.Budget = b

	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); !errors.Is(err, llm.ErrBudgetExhausted) {
		t.Fatalf("got %v, want ErrBudgetExhausted", err)
	}
	if p.called != 0 {
		t.Fatal("provider was called despite the cap being exhausted; money was spent")
	}
}

func TestDescriptorsOutsideTheVocabularyAreKeptButNotTagged(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{
		{ID: "a", Moods: []string{"zorbular", "warm"}},
	}}
	st, tg := newStore(), newTagger()
	r := newRunner(p, st, tg, Options{})

	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); err != nil {
		t.Fatal(err)
	}
	if got := tg.written["/m/a.flac"]; len(got) != 1 || got[0] != "warm" {
		t.Fatalf("tag = %v, want [warm] only", got)
	}
	raw, _, _ := st.Get(recordKey("a"))
	var rec Record
	_ = json.Unmarshal(raw, &rec)
	if len(rec.Freeform) != 2 {
		t.Fatalf("unknown descriptor was discarded: %v", rec.Freeform)
	}
}

func TestNoCanonicalTermsMeansNoWrite(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{{ID: "a", Moods: []string{"zorbular"}}}}
	st, tg := newStore(), newTagger()
	r := newRunner(p, st, tg, Options{})

	out, err := r.Process([]Item{item("a", "/m/a.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Written != 0 || len(tg.written) != 0 {
		t.Fatal("wrote an empty MOOD tag")
	}
	if out.Labelled != 1 {
		t.Fatal("should still count as labelled and be recorded")
	}
}

func TestWriteFailureSurfaces(t *testing.T) {
	p := &stubProvider{labels: []llm.Label{{ID: "a", Moods: []string{"warm"}}}}
	tg := newTagger()
	tg.writeErr = errors.New("disk full")
	r := newRunner(p, newStore(), tg, Options{})

	if _, err := r.Process([]Item{item("a", "/m/a.flac")}); err == nil {
		t.Fatal("a failed tag write was swallowed")
	}
}

func TestEmptyBatchDoesNothing(t *testing.T) {
	p := &stubProvider{}
	r := newRunner(p, newStore(), newTagger(), Options{})
	out, err := r.Process(nil)
	if err != nil || out.Requested != 0 || p.called != 0 {
		t.Fatalf("empty batch did something: %v %+v calls=%d", err, out, p.called)
	}
}
