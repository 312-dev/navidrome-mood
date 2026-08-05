package prompt

import (
	"strings"
	"testing"

	"github.com/312-dev/navidrome-mood/internal/mood"
)

func sampleLibrary() Library {
	return Library{
		Vibes: []Vibe{
			{Name: "slow shreds", Total: 81, Samples: []string{
				"Audioslave - Shadow On The Sun", "Pearl Jam - Black",
			}},
			{Name: "strum stories", Total: 229, Samples: []string{
				"Jason Isbell - Cover Me Up",
			}},
		},
		Vocabulary: mood.Vocabulary,
		TopGenres:  []string{"Rock", "Electronic", "Hip Hop"},
		Decades:    []string{"2010s", "2000s"},
	}
}

// Provider-side caching only works on a byte-identical prefix. A prompt that
// varied between runs would silently cost about a fifth more, with nothing
// visible except the bill.
func TestBuildIsDeterministic(t *testing.T) {
	lib := sampleLibrary()
	first := Build(lib)
	for i := 0; i < 20; i++ {
		if got := Build(lib); got != first {
			t.Fatalf("prompt changed between builds on iteration %d", i)
		}
	}
}

func TestPromptCarriesTheUsersOwnVocabulary(t *testing.T) {
	got := Build(sampleLibrary())
	// The vibe names are the point: they mean something specific in this library
	// that no general taxonomy contains.
	for _, want := range []string{"slow shreds", "strum stories", "Shadow On The Sun"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt does not mention %q", want)
		}
	}
	if !strings.Contains(got, "81 tracks") {
		t.Fatal("prompt omits playlist sizes, so the model cannot weigh them")
	}
}

// The instruction exists because of a measured behaviour: MOOD="up-tempo, driving"
// became two moods in Navidrome and matched a rule for "up-tempo".
func TestPromptForbidsSeparatorsInsideDescriptors(t *testing.T) {
	got := Build(sampleLibrary())
	for _, want := range []string{"comma", "semicolon", "slash"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt does not forbid %q inside a descriptor", want)
		}
	}
}

// Every output field must appear together under "For each track return:". An
// earlier version emitted `times` after the vocabulary block, several paragraphs
// away from the list it belongs to, which reads as a dangling instruction.
func TestOutputFieldsStayInOneBlock(t *testing.T) {
	got := Build(sampleLibrary())

	header := strings.Index(got, "For each track return:")
	times := strings.Index(got, "  times ")
	vocab := strings.Index(got, "Prefer these descriptors")

	if header < 0 || times < 0 || vocab < 0 {
		t.Fatalf("missing section: header=%d times=%d vocab=%d", header, times, vocab)
	}
	if !(header < times && times < vocab) {
		t.Fatalf("field block is split: header at %d, times at %d, vocabulary at %d; "+
			"times must sit with the other fields", header, times, vocab)
	}
	// Nothing should separate the fields from each other.
	block := got[header:times]
	if strings.Contains(block, "\n\n") {
		t.Fatalf("blank line inside the output field list:\n%s", block)
	}
}

func TestPromptListsTimeSlotsExactly(t *testing.T) {
	got := Build(sampleLibrary())
	for _, slot := range TimeSlots {
		if !strings.Contains(got, slot) {
			t.Fatalf("prompt omits time slot %q, so the model cannot use it", slot)
		}
	}
}

func TestBuildToleratesAnEmptyLibrary(t *testing.T) {
	// A fresh install has no curated playlists yet. The prompt must still be
	// usable rather than emitting a dangling header.
	got := Build(Library{})
	if got == "" {
		t.Fatal("empty library produced no prompt")
	}
	if strings.Contains(got, "curated vibes") {
		t.Fatal("prompt promises examples it does not have")
	}
	if !strings.Contains(got, "energy") {
		t.Fatal("prompt lost its output contract")
	}
}

// Playlist files cluster by artist, so taking the head of a playlist teaches the
// model that a vibe means one band.
func TestSampleAcrossSpreadsRatherThanTakingTheHead(t *testing.T) {
	items := make([]string, 100)
	for i := range items {
		items[i] = string(rune('a' + i/10)) // 10 of each letter, in runs
	}

	got := SampleAcross(items, 5)
	if len(got) != 5 {
		t.Fatalf("got %d samples, want 5", len(got))
	}
	distinct := map[string]bool{}
	for _, g := range got {
		distinct[g] = true
	}
	if len(distinct) < 4 {
		t.Fatalf("sample %v covers only %d distinct runs; it is taking the head",
			got, len(distinct))
	}
	// Compare with the naive approach this exists to avoid.
	head := items[:5]
	headDistinct := map[string]bool{}
	for _, h := range head {
		headDistinct[h] = true
	}
	if len(headDistinct) != 1 {
		t.Fatal("test fixture is wrong: the head should be a single run")
	}
}

func TestSampleAcrossIsDeterministic(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	first := SampleAcross(items, 4)
	for i := 0; i < 10; i++ {
		got := SampleAcross(items, 4)
		for j := range first {
			if got[j] != first[j] {
				t.Fatalf("sampling varied between calls: %v then %v", first, got)
			}
		}
	}
}

func TestSampleAcrossEdgeCases(t *testing.T) {
	if got := SampleAcross([]string{}, 5); got != nil {
		t.Fatalf("empty input gave %v", got)
	}
	if got := SampleAcross([]string{"a"}, 0); got != nil {
		t.Fatalf("zero n gave %v", got)
	}
	// Fewer items than requested returns all of them, not a padded slice.
	if got := SampleAcross([]string{"a", "b"}, 5); len(got) != 2 {
		t.Fatalf("got %v, want both items", got)
	}
	// Must never index out of range.
	for n := 1; n <= 20; n++ {
		for size := 1; size <= 20; size++ {
			items := make([]int, size)
			_ = SampleAcross(items, n)
		}
	}
}

// The prompt is the cached prefix on every request, so its size is a direct cost
// input. This is a canary, not a hard limit.
func TestPromptStaysReasonablySized(t *testing.T) {
	got := Build(sampleLibrary())
	if n := len(got); n > 12_000 {
		t.Fatalf("prompt is %d bytes; it is the cached prefix of every request, so "+
			"growth here multiplies across the whole pass", n)
	}
}
