package prompt

import (
	"fmt"
	"strings"
	"testing"

	"github.com/312-dev/navidrome-mood/internal/mood"
)

// lineFor returns the prompt line whose first word is term, which is how the
// anchor block is laid out. Matching on the whole line rather than on Contains
// keeps `driving` the vocabulary term from being satisfied by `driving` the
// tempo feel.
func lineFor(t *testing.T, prompt, term string) string {
	t.Helper()
	for _, line := range strings.Split(prompt, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == term {
			return line
		}
	}
	t.Fatalf("prompt has no line for term %q", term)
	return ""
}

// Provider-side caching only works on a byte-identical prefix. A prompt that
// varied between runs would silently cost about a fifth more, with nothing
// visible except the bill. Twenty iterations because the usual cause is a range
// over a map, and Go's randomised iteration order can repeat by luck.
func TestBuildIsDeterministic(t *testing.T) {
	first := Build(Library{})
	for i := 0; i < 20; i++ {
		if got := Build(Library{}); got != first {
			t.Fatalf("prompt changed between builds on iteration %d", i)
		}
	}
}

// The model can only use a term it has been shown, and a term missing from the
// prompt fails silently: the label just comes back without it.
func TestPromptCarriesEveryAnchor(t *testing.T) {
	got := Build(Library{})

	if len(mood.Vocabulary) != 52 {
		t.Fatalf("vocabulary has %d terms, expected 52; update this test deliberately",
			len(mood.Vocabulary))
	}
	for _, term := range mood.Vocabulary {
		lineFor(t, got, term)
	}
}

// The gloss is why the anchor is not the only guidance the labeller gets. Shown
// as a bare word list, the model would fall back on its own priors for what
// `smouldering` means, which is the drift the fixed coordinates exist to stop.
func TestEveryAnchorKeepsItsGlossAndCoordinates(t *testing.T) {
	got := Build(Library{})

	for _, term := range mood.Vocabulary {
		a := mood.Anchors[term]
		line := lineFor(t, got, term)

		if !strings.Contains(line, a.Gloss) {
			t.Errorf("term %q lost its gloss; line reads %q", term, line)
		}
		coords := fmt.Sprintf("e%d v%d i%d a%d d%d",
			a.Energy, a.Valence, a.Intensity, a.Acousticness, a.Density)
		if !strings.Contains(line, coords) {
			t.Errorf("term %q is missing its coordinates %q; line reads %q", term, coords, line)
		}
	}
}

// Two runs of the model only agree on a scale that was stated. An axis named
// without its ends is an axis each run calibrates for itself.
func TestEveryAxisIsNamedWithItsScale(t *testing.T) {
	got := Build(Library{})

	for _, axis := range []string{"energy", "valence", "intensity", "acousticness", "density"} {
		line := lineFor(t, got, axis)
		for _, want := range []string{"0-100", "0 is", "100 is"} {
			if !strings.Contains(line, want) {
				t.Errorf("axis %q line does not state %q: %q", axis, want, line)
			}
		}
	}
}

// The connector reads tempo and vocal all-or-nothing with the five axes, so a
// value outside these sets makes the whole track count as unlabelled.
func TestPromptListsTheClosedSetsExactly(t *testing.T) {
	got := Build(Library{})

	tempo := lineFor(t, got, "tempo")
	for _, feel := range mood.TempoFeels {
		if !strings.Contains(tempo, string(feel)) {
			t.Errorf("tempo line omits %q: %q", feel, tempo)
		}
	}
	vocal := lineFor(t, got, "vocal")
	for _, kind := range mood.VocalKinds {
		if !strings.Contains(vocal, string(kind)) {
			t.Errorf("vocal line omits %q: %q", kind, vocal)
		}
	}
}

func TestPromptListsTimeSlotsExactly(t *testing.T) {
	got := Build(Library{})
	for _, slot := range TimeSlots {
		if !strings.Contains(got, slot) {
			t.Fatalf("prompt omits time slot %q, so the model cannot use it", slot)
		}
	}
}

// Every output field must appear together under "For each track return:". An
// earlier version emitted `times` after the vocabulary block, several paragraphs
// away from the list it belongs to, and the model started dropping it.
func TestOutputFieldsStayInOneBlock(t *testing.T) {
	got := Build(Library{})

	header := strings.Index(got, "For each track return:")
	if header < 0 {
		t.Fatal("prompt has no output field header")
	}

	fields := []string{
		"energy", "valence", "intensity", "acousticness", "density",
		"tempo", "vocal", "moods", "times",
	}
	prev := header
	last := header
	for _, f := range fields {
		at := strings.Index(got, "\n  "+f+" ")
		if at < 0 {
			t.Fatalf("prompt does not ask for %q", f)
		}
		if at < prev {
			t.Fatalf("field %q appears before the field ahead of it, at %d after %d", f, at, prev)
		}
		prev = at
		last = at
	}

	// A blank line anywhere between the header and the last field means something
	// has been wedged into the middle of the list.
	if block := got[header:last]; strings.Contains(block, "\n\n") {
		t.Fatalf("blank line inside the output field list:\n%s", block)
	}
}

// Vibes are computed by mood.VibesFor from the axes, not judged. Asking the
// model for one would be asking it to guess the output of a function whose
// inputs it has already supplied, and the two answers would disagree with no
// correct side. This is the field most likely to be helpfully added back.
func TestPromptNeverAsksForVibesOrRegions(t *testing.T) {
	got := strings.ToLower(Build(Library{}))

	for _, forbidden := range []string{"vibe", "region"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("prompt mentions %q; membership is geometry, not a judgement", forbidden)
		}
	}
	// Region names that are not also vocabulary terms or time slots. Their presence
	// would mean the region taxonomy leaked into what the model is asked for.
	for _, name := range []string{"wind down", "slow morning", "workout", "hype", "dinner"} {
		if strings.Contains(got, name) {
			t.Errorf("prompt names the region %q", name)
		}
	}
}

// The instruction exists because of a measured behaviour: MOOD="up-tempo, driving"
// became two moods in Navidrome and matched a rule for "up-tempo".
func TestPromptForbidsSeparatorsInsideTerms(t *testing.T) {
	got := Build(Library{})
	for _, want := range []string{"comma", "semicolon", "slash"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt does not forbid %q inside a term", want)
		}
	}
}

// The vocabulary is defined rather than sampled, so the prompt must not describe
// the collection it is labelling or invite the model to score relative to it.
// It also has no listener to be personal about: this plugin runs on servers
// whose owner nobody here has met.
func TestPromptIsNotPersonalisedToOneListener(t *testing.T) {
	got := strings.ToLower(Build(Library{}))
	for _, forbidden := range []string{"the listener", " his ", " her ", "curated", "playlists"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("prompt contains %q, which has no referent on a stranger's server", forbidden)
		}
	}
}

// The prompt is the cached prefix on every request, so its size is a direct cost
// input. This is a canary, not a hard limit: the anchor block is meant to be
// large, and a jump past this means something other than an anchor grew.
func TestPromptStaysReasonablySized(t *testing.T) {
	got := Build(Library{})
	if n := len(got); n > 12_000 {
		t.Fatalf("prompt is %d bytes; it is the cached prefix of every request, so "+
			"growth here multiplies across the whole pass", n)
	}
}
