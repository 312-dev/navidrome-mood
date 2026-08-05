// Package mood defines the controlled mood vocabulary written to FLAC MOOD tags.
//
// Two vocabularies exist, deliberately:
//
//   - The FIXED enum below is what goes into the MOOD tag. A closed set is what
//     makes `mood IS wistful` a reliable smart-playlist rule; an open one would
//     fragment into synonyms (wistful / wistfulness / bittersweet-wistful) and
//     quietly stop matching.
//   - The FREE-FORM descriptors the model actually returns are kept in kvstore
//     alongside, so nuance is not lost and the enum can be revised later without
//     re-paying for inference.
package mood

import (
	"fmt"
	"sort"
	"strings"
)

// Separators are the characters Navidrome splits a tag value on, declared for
// `mood` in resources/mappings.yaml as split: [";", "/", ","] and applied by
// model/metadata/metadata.go SplitTagValue to EVERY value of a regular tag.
//
// This is not a style preference. Verified against a live Navidrome on
// 2026-08-05: writing MOOD="up-tempo, driving" produced two moods, and a smart
// playlist matching `mood IS up-tempo` found the track. Any enum term containing
// one of these would silently fragment across the whole library and still look
// like it worked. See Validate.
const Separators = ",;/"

// Vocabulary is the controlled set of mood terms.
//
// Derived empirically rather than invented: these are the 60 most frequent
// free-form descriptors produced by labelling 9,193 tracks, as reported by the
// navidrome-mcp index on 2026-08-05. Counts at derivation time are noted so the
// long tail can be judged if this is ever revised - "warm" led with 758 tracks,
// "brash" closed the list with 125.
var Vocabulary = []string{
	// high frequency (300+)
	"warm", "wistful", "tender", "anthemic", "yearning", "swaggering", "hazy",
	"nostalgic", "driving", "soulful", "dreamy", "breezy", "smooth", "sunny",
	"brooding", "groovy", "bouncy", "aching", "gritty", "playful",
	// mid frequency (200-300)
	"earnest", "bittersweet", "romantic", "intimate", "euphoric", "defiant",
	"funky", "reflective", "dark", "hopeful", "restless", "catchy", "hypnotic",
	"uplifting", "heavy", "melancholy", "sultry", "glossy", "punchy",
	// lower frequency (125-200)
	"cinematic", "menacing", "retro", "bluesy", "raw", "aggressive", "gentle",
	"laid-back", "rowdy", "urgent", "moody", "bright", "flirty", "upbeat",
	"longing", "dramatic", "cool", "mellow", "woozy", "danceable", "brash",
}

// Synonyms maps descriptors the model commonly returns onto a canonical term.
// Kept small on purpose: the goal is to catch obvious morphological variants,
// not to editorialise. Anything not listed and not in Vocabulary is preserved
// verbatim in kvstore and simply omitted from the tag.
var Synonyms = map[string]string{
	"melancholic": "melancholy",
	"nostalgia":   "nostalgic",
	"wistfulness": "wistful",
	"yearnful":    "yearning",
	"laidback":    "laid-back",
	"laid back":   "laid-back",
	"up-tempo":    "upbeat",
	"uptempo":     "upbeat",
	"energetic":   "driving",
	"chill":       "mellow",
	"chilled":     "mellow",
	"somber":      "brooding",
	"sombre":      "brooding",
	"sad":         "melancholy",
	"happy":       "sunny",
	"angry":       "aggressive",
	"tense":       "restless",
	"lush":        "warm",
	"grungy":      "gritty",
	"propulsive":  "driving",
	"nocturnal":   "moody",
	"hushed":      "gentle",
	"icy":         "cool",
	"murky":       "hazy",
	"bleak":       "dark",
	"nihilistic":  "dark",
	"surging":     "anthemic",
	"soaring":     "anthemic",
}

var index = func() map[string]string {
	m := make(map[string]string, len(Vocabulary))
	for _, v := range Vocabulary {
		m[v] = v
	}
	return m
}()

// Validate reports every problem with the vocabulary. It must return nil before
// a single tag is written; a violation is not cosmetic, it silently fragments the
// vocabulary across the entire library.
func Validate() error {
	var problems []string
	seen := map[string]bool{}

	for _, term := range Vocabulary {
		switch {
		case term == "":
			problems = append(problems, "empty term")
		case term != strings.ToLower(term):
			problems = append(problems, fmt.Sprintf("%q is not lowercase", term))
		case strings.TrimSpace(term) != term:
			problems = append(problems, fmt.Sprintf("%q has surrounding whitespace", term))
		case strings.ContainsAny(term, Separators):
			problems = append(problems, fmt.Sprintf(
				"%q contains a separator (%q) and would be split into multiple moods",
				term, Separators))
		case seen[term]:
			problems = append(problems, fmt.Sprintf("%q is duplicated", term))
		}
		seen[term] = true
	}

	for from, to := range Synonyms {
		if _, ok := index[to]; !ok {
			problems = append(problems, fmt.Sprintf(
				"synonym %q maps to %q which is not in the vocabulary", from, to))
		}
		if _, ok := index[from]; ok {
			problems = append(problems, fmt.Sprintf(
				"%q is both a vocabulary term and a synonym", from))
		}
		if strings.ContainsAny(from, Separators) {
			problems = append(problems, fmt.Sprintf("synonym key %q contains a separator", from))
		}
		// Canonical lowercases and trims before lookup, so a key that is not
		// already in that form can never match and is silently dead. One such
		// entry ("aggressive " with a trailing space) shipped and was only caught
		// because the plugin logged a synonym count one lower than expected.
		if norm := strings.ToLower(strings.TrimSpace(from)); norm != from {
			problems = append(problems, fmt.Sprintf(
				"synonym key %q is not lowercase-trimmed, so Canonical can never match it", from))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("mood vocabulary is invalid:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// Canonical maps a free-form descriptor onto a vocabulary term, reporting whether
// it belongs in the tag at all. Unknown descriptors return ok=false; the caller
// keeps them in kvstore rather than discarding them.
func Canonical(s string) (string, bool) {
	k := strings.ToLower(strings.TrimSpace(s))
	if k == "" {
		return "", false
	}
	if v, ok := index[k]; ok {
		return v, true
	}
	if v, ok := Synonyms[k]; ok {
		return v, true
	}
	return "", false
}

// TagValues converts the model's free-form descriptors into the ordered,
// deduplicated set of vocabulary terms to write as repeated MOOD fields.
//
// Order follows first appearance so the model's own sense of which descriptor
// matters most survives into the file. Duplicates are dropped: two descriptors
// can legitimately canonicalise to the same term (bleak and nihilistic both
// become dark), and a repeated identical value would show up twice in Navidrome.
func TagValues(descriptors []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range descriptors {
		c, ok := Canonical(d)
		if !ok || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}
