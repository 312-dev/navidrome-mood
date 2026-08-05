// Package prompt builds the system prompt that grounds labelling in the user's
// own vocabulary rather than in generic descriptors.
//
// The central idea, carried over from the proven TypeScript implementation: a
// user's hand-curated playlists ARE their mood vocabulary. Names like "slow
// shreds" and "strum stories" mean something specific in their library that no
// general taxonomy contains. So the prompt shows the model real examples from
// each playlist and asks it to EXTEND that vocabulary to the rest of the library,
// instead of inventing one.
package prompt

import (
	"fmt"
	"sort"
	"strings"
)

// TimeSlots are the time-of-day buckets a track can be marked as fitting. Fixed
// rather than free-form because the daylist generator filters on them exactly.
var TimeSlots = []string{
	"early morning", "morning", "midday", "afternoon",
	"golden hour", "evening", "late night",
}

// Vibe is one of the user's curated playlists, with a sample of what is in it.
type Vibe struct {
	Name    string
	Total   int
	Samples []string // "Artist - Title"
}

// Library is the context the prompt is built from.
type Library struct {
	Vibes      []Vibe
	Vocabulary []string
	// TopGenres and Decades orient the model to what this library actually is,
	// so it does not describe a rock library in the language of jazz.
	TopGenres []string
	Decades   []string
}

// SamplesPerVibe is how many example tracks are shown per playlist.
//
// Sample ACROSS a playlist rather than taking the head. m3u files cluster by
// artist, so the first N entries are frequently all the same band - which
// teaches the model that "cranked" means that one artist rather than the feeling
// the user actually files under it.
const SamplesPerVibe = 8

// Build returns the system prompt. It is deterministic: identical input yields
// byte-identical output, which is what lets provider-side prompt caching work at
// all. The taxonomy is the expensive part of every request and is identical
// across batches, so a prompt that varied run to run would silently cost roughly
// a fifth more.
func Build(lib Library) string {
	var b strings.Builder

	b.WriteString(
		"You label music by mood for a personal library.\n\n" +
			"The listener has hand-curated playlists that ARE his mood vocabulary. Your job is\n" +
			"to extend that vocabulary to every track in the library, including tracks that\n" +
			"never made it onto a playlist. Judge the FEELING of the music, not its genre.\n\n")

	if len(lib.TopGenres) > 0 || len(lib.Decades) > 0 {
		b.WriteString("This library is mostly ")
		if len(lib.TopGenres) > 0 {
			b.WriteString(strings.Join(lib.TopGenres, ", "))
		}
		if len(lib.Decades) > 0 {
			b.WriteString(", concentrated in the " + strings.Join(lib.Decades, ", "))
		}
		b.WriteString(".\n\n")
	}

	if len(lib.Vibes) > 0 {
		b.WriteString("His curated vibes, with real examples of each:\n\n")
		for _, v := range lib.Vibes {
			fmt.Fprintf(&b, "  %s (%d tracks)\n", v.Name, v.Total)
			for _, s := range v.Samples {
				fmt.Fprintf(&b, "    %s\n", s)
			}
			b.WriteString("\n")
		}
	}

	// Keep every output field in one block. An earlier version emitted `times`
	// after the vocabulary section, leaving it dangling several paragraphs away
	// from the list it belongs to.
	b.WriteString("For each track return:\n" +
		"  energy     0-100, how physically activating it is\n" +
		"  valence    0-100, how positive it feels (0 desolate, 100 elated)\n" +
		"  intensity  0-100, how demanding of attention\n" +
		"  organic    0-100, acoustic and human at 100, synthetic and programmed at 0\n" +
		"  moods      2-4 short lowercase descriptors of the feeling\n")
	fmt.Fprintf(&b, "  times      when it fits, any of: %s\n", strings.Join(TimeSlots, ", "))

	if len(lib.Vocabulary) > 0 {
		vocab := append([]string(nil), lib.Vocabulary...)
		sort.Strings(vocab)
		b.WriteString("\nPrefer these descriptors where one genuinely fits, so the vocabulary stays\n" +
			"consistent across the library. Use a different word when none of them is right:\n  " +
			wrap(vocab, 72, "  ") + "\n")
		// Earned by measurement: writing "up-tempo, driving" as a single value
		// produced two moods in Navidrome and matched a rule for "up-tempo".
		b.WriteString("\nNever put a comma, semicolon or slash inside a descriptor. Each descriptor is\n" +
			"a separate item in the list.\n")
	}

	b.WriteString("\nBe decisive and specific. Hedging every track into the middle of every axis\n" +
		"makes the labels useless for building a playlist. If a track is genuinely\n" +
		"unremarkable on an axis, 50 is a real answer - but do not default to it.\n\n" +
		"Return one entry per input track, with the SAME id, in the SAME order.\n")

	return b.String()
}

// wrap joins terms into lines of at most width characters, so the prompt stays
// readable when someone inspects what was actually sent.
func wrap(items []string, width int, indent string) string {
	var lines []string
	cur := ""
	for _, it := range items {
		add := it
		if cur != "" {
			add = ", " + it
		}
		if len(cur)+len(add) > width && cur != "" {
			lines = append(lines, cur)
			cur = it
			continue
		}
		cur += add
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n"+indent)
}

// SampleAcross picks n items spread evenly through the list rather than taking
// the head.
//
// This matters more than it looks. Playlist files cluster by artist, so the first
// n entries of "cranked" are often all one band, and the model learns that the
// vibe means that band. Spreading the sample shows the actual range.
func SampleAcross[T any](items []T, n int) []T {
	if n <= 0 || len(items) == 0 {
		return nil
	}
	if len(items) <= n {
		out := make([]T, len(items))
		copy(out, items)
		return out
	}
	out := make([]T, 0, n)
	// Integer stride keeps this deterministic; a float step would make the prompt
	// vary with rounding and break prompt caching.
	for i := 0; i < n; i++ {
		out = append(out, items[i*len(items)/n])
	}
	return out
}
