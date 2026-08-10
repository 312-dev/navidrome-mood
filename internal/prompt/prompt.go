// Package prompt builds the system prompt that asks a model to place a track in
// the mood-space defined by package mood.
//
// The vocabulary is shown to the model, not gathered from the collection being
// labelled. Every term has fixed coordinates and a gloss, so the prompt can hand
// over the space itself and ask only where each track sits in it. That is what
// makes a label mean the same thing on a stranger's server as on the machine the
// anchors were tuned on: nothing in the prompt depends on whose music it is.
package prompt

import (
	"fmt"
	"strings"

	"github.com/312-dev/navidrome-mood/internal/mood"
)

// TimeSlots are the time-of-day buckets a track can be marked as fitting. Fixed
// rather than free-form because the connector filters on the values exactly, so
// a slot it has no name for is a slot no playlist can ever ask for.
var TimeSlots = []string{
	"early morning", "morning", "midday", "afternoon",
	"golden hour", "evening", "late night",
}

// Library is the per-install context Build is given. It is empty: the anchors
// define both the vocabulary and the axis scales, and they are identical on
// every server, so there is nothing about a particular collection the prompt
// needs to be told.
//
// A genre or decade summary is the obvious thing to add here, and it is left out
// on purpose. Telling the model "this collection is mostly metal" invites it to
// score relative to the collection, so a mild metal track comes back at
// intensity 40 because it is mild FOR METAL. The axes would then mean something
// different in every library, which is the exact failure the defined anchors
// exist to prevent. Anything added here has to survive that test.
//
// The struct stays as Build's single parameter so a future input that does
// survive it can be added without touching callers.
type Library struct{}

// anchorGroups lay the vocabulary out by circumplex quadrant, the frame the
// anchors themselves are organised around: arousal against valence, split at the
// midpoint of each.
//
// A flat alphabetical list would carry the same 52 terms and teach none of the
// shape. Grouped, the model reads the geometry in the same pass as the words: it
// sees that `bleak` and `mournful` are neighbours and that both sit far from
// `sunny`, which is the judgement it is being asked to make. The membership test
// is computed from each anchor's own coordinates rather than written out term by
// term, so adding a 53rd anchor to mood.Anchors places it here automatically
// instead of dropping it silently.
var anchorGroups = []struct {
	heading string
	holds   func(mood.Axes) bool
}{
	{"calm and positive (energy under 50, valence 50 or over)",
		func(a mood.Axes) bool { return a.Energy < 50 && a.Valence >= 50 }},
	{"calm and dark (energy under 50, valence under 50)",
		func(a mood.Axes) bool { return a.Energy < 50 && a.Valence < 50 }},
	{"energised and positive (energy 50 or over, valence 50 or over)",
		func(a mood.Axes) bool { return a.Energy >= 50 && a.Valence >= 50 }},
	{"energised and dark (energy 50 or over, valence under 50)",
		func(a mood.Axes) bool { return a.Energy >= 50 && a.Valence < 50 }},
}

// Build returns the system prompt. It is deterministic: identical input yields
// byte-identical output, which is what lets provider-side prompt caching work at
// all. The taxonomy is the expensive part of every request and is identical
// across batches, so a prompt that varied run to run would silently cost roughly
// a fifth more.
//
// The anchor block is several kilobytes, far larger than a bare term list, which
// makes the caching worth more rather than less. Everything iterated here is
// ordered explicitly: mood.Anchors is a map and Go randomises map iteration, so
// a single range over it would be enough to defeat the cache.
//
// Note what the prompt does NOT ask for: a vibe or a named area of the space.
// Those are geometry. mood.VibesFor computes them from the five axes plus the
// hard tempo, vocal and valence constraints, so asking the model to name one
// would be asking it to guess the output of a function whose inputs it has
// already supplied, and a disagreement between the two answers would have no
// correct side. Adding a vibe field here is the change to reject in review.
func Build(lib Library) string {
	var b strings.Builder

	b.WriteString(
		"You place a track in a defined mood-space.\n\n" +
			"The space below is the same for every library, so judge the music itself rather\n" +
			"than how it compares to the rest of the collection. Judge the FEELING of the\n" +
			"sound, not the genre and not the subject of the lyrics.\n\n")

	// Every output field stays in this one block, with its scale stated on its own
	// line. An earlier version emitted `times` several paragraphs from the rest and
	// the model started dropping it; TestOutputFieldsStayInOneBlock is what holds
	// that shut. The 0 and 100 ends are spelled out so two runs agree on the scale
	// rather than each inventing one.
	b.WriteString("For each track return:\n" +
		"  energy        0-100. 0 is motionless, 100 is flat out. Physical arousal, not speed.\n" +
		"  valence       0-100. 0 is bleak, 100 is elated. How positive the music feels.\n" +
		"  intensity     0-100. 0 is weightless, 100 is crushing. Force and weight at any speed.\n" +
		"  acousticness  0-100. 0 is wholly electronic and produced, 100 is wholly acoustic.\n" +
		"  density       0-100. 0 is one sound alone, 100 is a wall of sound. How much fills the space.\n")
	fmt.Fprintf(&b, "  tempo         the pace you feel, one of: %s.\n", joinNames(mood.TempoFeels))
	fmt.Fprintf(&b, "  vocal         one of: %s.\n", joinNames(mood.VocalKinds))
	b.WriteString("  moods         2-4 terms from the vocabulary below, best fit first.\n")
	fmt.Fprintf(&b, "  times         when it suits, any of: %s.\n", strings.Join(TimeSlots, ", "))

	b.WriteString("\nThe five numbers are independent. A slow track can be intense, a loud one can\n" +
		"be sparse, and an entirely acoustic one can be furious. Tempo is the pace you\n" +
		"feel rather than the BPM: a half-time riff at 140 BPM feels still.\n\n")

	b.WriteString("The vocabulary. Each term is defined by its own point in the space, written\n" +
		"here as e(nergy) v(alence) i(ntensity) a(cousticness) d(ensity), followed by\n" +
		"what it means. Choose the terms whose point and meaning both fit the track.\n" +
		"Use only these words: a descriptor that is not one of them, and not a close\n" +
		"synonym of one, is discarded, so a near term beats an apt invention.\n")

	width := 0
	for _, term := range mood.Vocabulary {
		if len(term) > width {
			width = len(term)
		}
	}
	for _, g := range anchorGroups {
		var lines []string
		for _, term := range mood.Vocabulary {
			a := mood.Anchors[term]
			if !g.holds(a.Axes) {
				continue
			}
			coords := fmt.Sprintf("e%d v%d i%d a%d d%d",
				a.Energy, a.Valence, a.Intensity, a.Acousticness, a.Density)
			lines = append(lines, fmt.Sprintf("  %-*s  %-19s  %s", width, term, coords, a.Gloss))
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s\n%s\n", g.heading, strings.Join(lines, "\n"))
	}

	// Earned by measurement: writing MOOD="up-tempo, driving" to a file produced two
	// moods in Navidrome, because it splits every tag value on `,;/`.
	b.WriteString("\nNever put a comma, semicolon or slash inside a term. Each term is a separate\n" +
		"item in the list.\n")

	b.WriteString("\nBe decisive and specific. Hedging every track into the middle of every axis\n" +
		"makes the labels useless for building a playlist. If a track is genuinely\n" +
		"unremarkable on an axis, 50 is a real answer - but do not default to it.\n\n" +
		"Return one entry per input track, with the SAME id, in the SAME order.\n")

	return b.String()
}

// joinNames renders a closed set of string-kinded constants for the prompt. The
// sets are declared as slices in package mood precisely so their order is fixed,
// so this preserves it.
func joinNames[T ~string](vals []T) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	return strings.Join(out, ", ")
}
