// Package mood defines the controlled mood vocabulary, the geometry it lives
// in, and the named regions of that geometry, all of which the plugin writes to
// tags. It is the sole authority on what a track sounds like; the navidrome-mcp
// connector reads the tags back and never labels.
//
// Every term is DEFINED by an anchor in mood-space rather than discovered by
// sampling a library. That distinction is the whole design:
//
//   - A vocabulary derived from one collection inherits its shape. Terms earn
//     their place by being frequent there, so a rock-heavy library yields
//     `riffy` and `bass-heavy` while a jazz collection would need distinctions
//     neither of those covers. The list stops travelling.
//   - A vocabulary of defined anchors covers the SPACE instead of a sample of
//     it. Any library maps onto the same coordinates; different collections
//     simply occupy different regions. A term nobody's music reaches is unused,
//     not wrong.
//
// Coherence is therefore guaranteed by construction. An earlier free-form pass
// over ~9,000 tracks produced 1,020 descriptors, and scoring each by how tightly
// its tracks clustered separated them cleanly: words describing SOUND clustered,
// words describing ASSOCIATION (`nostalgic`, `cinematic`, `raw`, `lush`) spanned
// everything. That split is linguistic, not local - association words behave the
// same way in any collection - so this vocabulary excludes them on principle
// rather than on measurement.
//
// The organising frame is Russell's circumplex: arousal (energy) against
// valence, the standard model in music-emotion work. Terms are laid across those
// quadrants and then differentiated by timbre - intensity, acousticness and
// density - which is what separates a string quartet from a doom riff when both
// are slow, dark and heavy-hearted.
//
// Descriptors the model returns that fold onto no term are dropped rather than
// written. Inventing a term on the fly is exactly how an uncontrolled tail grows.
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
// playlist matching `mood IS up-tempo` found the track. Any term, synonym key or
// region name containing one would silently fragment across the whole library
// and still look like it worked. See Validate.
const Separators = ",;/"

// Anchor is where a term sits, plus the prose that says the same thing in words
// so the coordinates are not the only guidance the labeller gets.
type Anchor struct {
	Axes
	// Gloss is shown to the model verbatim.
	Gloss string
}

// Anchors define each term rather than summarising one library's usage. Grouped
// by circumplex quadrant, then by timbre within it.
var Anchors = map[string]Anchor{
	// low arousal, positive valence: calm, warm, at ease
	"serene":   {Axes{Energy: 12, Valence: 62, Intensity: 10, Acousticness: 85, Density: 20}, "still and untroubled; space around every sound"},
	"tender":   {Axes{Energy: 20, Valence: 58, Intensity: 14, Acousticness: 78, Density: 25}, "soft and close, handled carefully"},
	"gentle":   {Axes{Energy: 22, Valence: 56, Intensity: 15, Acousticness: 76, Density: 26}, "unhurried and mild, nothing forced"},
	"warm":     {Axes{Energy: 32, Valence: 66, Intensity: 25, Acousticness: 68, Density: 40}, "rounded and comfortable; low-end without weight"},
	"mellow":   {Axes{Energy: 30, Valence: 60, Intensity: 20, Acousticness: 62, Density: 35}, "relaxed and smooth-edged"},
	"pastoral": {Axes{Energy: 20, Valence: 58, Intensity: 15, Acousticness: 90, Density: 22}, "open, rural, acoustic; air and daylight"},
	"intimate": {Axes{Energy: 22, Valence: 50, Intensity: 15, Acousticness: 80, Density: 20}, "small-room close; you hear the performer breathe"},
	"hushed":   {Axes{Energy: 16, Valence: 48, Intensity: 12, Acousticness: 78, Density: 18}, "deliberately quiet, held back"},
	"dreamy":   {Axes{Energy: 28, Valence: 58, Intensity: 20, Acousticness: 45, Density: 50}, "blurred and reverberant, edges softened"},

	// low arousal, negative valence: sad, heavy-hearted, still
	"melancholy": {Axes{Energy: 26, Valence: 26, Intensity: 22, Acousticness: 62, Density: 35}, "settled sadness, not acute"},
	"mournful":   {Axes{Energy: 20, Valence: 18, Intensity: 20, Acousticness: 70, Density: 30}, "grieving; weight without volume"},
	"lonesome":   {Axes{Energy: 24, Valence: 28, Intensity: 18, Acousticness: 82, Density: 20}, "solitary and spare, often a single voice"},
	"bleak":      {Axes{Energy: 20, Valence: 12, Intensity: 28, Acousticness: 45, Density: 32}, "cold and without consolation"},
	"weary":      {Axes{Energy: 22, Valence: 32, Intensity: 20, Acousticness: 58, Density: 32}, "worn down, dragging slightly"},
	"wistful":    {Axes{Energy: 30, Valence: 40, Intensity: 22, Acousticness: 65, Density: 35}, "longing, gently unresolved"},

	// mid arousal, positive valence: bright, moving, good-natured
	"sunny":    {Axes{Energy: 48, Valence: 80, Intensity: 30, Acousticness: 58, Density: 45}, "bright and uncomplicated"},
	"sweet":    {Axes{Energy: 36, Valence: 76, Intensity: 20, Acousticness: 70, Density: 35}, "affectionate and light"},
	"playful":  {Axes{Energy: 52, Valence: 78, Intensity: 30, Acousticness: 55, Density: 45}, "bouncing, unserious"},
	"breezy":   {Axes{Energy: 44, Valence: 72, Intensity: 28, Acousticness: 58, Density: 40}, "easy forward motion, no effort showing"},
	"groovy":   {Axes{Energy: 55, Valence: 68, Intensity: 35, Acousticness: 45, Density: 52}, "pocket-led; the rhythm section is the point"},
	"funky":    {Axes{Energy: 60, Valence: 70, Intensity: 42, Acousticness: 45, Density: 58}, "syncopated and physical"},
	"swinging": {Axes{Energy: 50, Valence: 74, Intensity: 32, Acousticness: 78, Density: 48}, "lilting triplet feel, live-band warmth"},
	"jaunty":   {Axes{Energy: 48, Valence: 74, Intensity: 30, Acousticness: 72, Density: 42}, "chipper and stepping along"},

	// mid arousal, negative valence: unsettled, dark but not violent
	"moody":       {Axes{Energy: 40, Valence: 34, Intensity: 40, Acousticness: 40, Density: 45}, "overcast; withholding"},
	"brooding":    {Axes{Energy: 38, Valence: 26, Intensity: 45, Acousticness: 40, Density: 45}, "gathering, something held down"},
	"tense":       {Axes{Energy: 52, Valence: 30, Intensity: 55, Acousticness: 35, Density: 50}, "wound tight, unresolved"},
	"restless":    {Axes{Energy: 58, Valence: 40, Intensity: 50, Acousticness: 42, Density: 50}, "agitated, unable to settle"},
	"smouldering": {Axes{Energy: 45, Valence: 40, Intensity: 45, Acousticness: 55, Density: 45}, "slow-burning heat, held in reserve"},
	"defiant":     {Axes{Energy: 62, Valence: 45, Intensity: 60, Acousticness: 40, Density: 60}, "planted and pushing back"},

	// high arousal, positive valence: lift, celebration, drive
	"euphoric":   {Axes{Energy: 84, Valence: 86, Intensity: 50, Acousticness: 18, Density: 72}, "peak lift; hands in the air"},
	"exuberant":  {Axes{Energy: 80, Valence: 82, Intensity: 45, Acousticness: 50, Density: 65}, "overflowing, unrestrained delight"},
	"triumphant": {Axes{Energy: 78, Valence: 78, Intensity: 62, Acousticness: 45, Density: 78}, "victorious, full-width"},
	"anthemic":   {Axes{Energy: 72, Valence: 66, Intensity: 58, Acousticness: 42, Density: 76}, "built to be sung back by a crowd"},
	"driving":    {Axes{Energy: 76, Valence: 56, Intensity: 60, Acousticness: 35, Density: 66}, "relentless forward propulsion"},
	"danceable":  {Axes{Energy: 68, Valence: 72, Intensity: 42, Acousticness: 25, Density: 60}, "made to move to; steady and physical"},

	// high arousal, negative valence: force, threat, fury
	"aggressive": {Axes{Energy: 85, Valence: 30, Intensity: 85, Acousticness: 25, Density: 76}, "attacking; force is the message"},
	"furious":    {Axes{Energy: 92, Valence: 24, Intensity: 92, Acousticness: 28, Density: 82}, "flat-out rage at full tilt"},
	"menacing":   {Axes{Energy: 68, Valence: 24, Intensity: 74, Acousticness: 20, Density: 64}, "threat held just below the surface"},
	"frantic":    {Axes{Energy: 90, Valence: 42, Intensity: 70, Acousticness: 38, Density: 70}, "too fast to hold on to"},
	"savage":     {Axes{Energy: 88, Valence: 20, Intensity: 90, Acousticness: 30, Density: 80}, "brutal and unpolished"},

	// timbre-led: chosen for HOW it sounds, across the quadrants
	"heavy":      {Axes{Energy: 72, Valence: 34, Intensity: 82, Acousticness: 32, Density: 78}, "downtuned mass; the low end dominates"},
	"gritty":     {Axes{Energy: 62, Valence: 42, Intensity: 62, Acousticness: 45, Density: 55}, "dirt on the signal; unsmoothed"},
	"fuzzy":      {Axes{Energy: 58, Valence: 48, Intensity: 58, Acousticness: 42, Density: 60}, "saturated and woolly-edged"},
	"glossy":     {Axes{Energy: 55, Valence: 65, Intensity: 40, Acousticness: 15, Density: 62}, "polished studio sheen"},
	"shimmering": {Axes{Energy: 45, Valence: 66, Intensity: 32, Acousticness: 30, Density: 55}, "bright high-end, glittering"},
	"pulsing":    {Axes{Energy: 60, Valence: 55, Intensity: 45, Acousticness: 12, Density: 58}, "steady synthetic throb"},
	"cold":       {Axes{Energy: 50, Valence: 32, Intensity: 50, Acousticness: 15, Density: 45}, "clinical, unwarmed by the room"},
	"sparse":     {Axes{Energy: 28, Valence: 45, Intensity: 22, Acousticness: 60, Density: 12}, "few elements, lots of silence"},
	"lush":       {Axes{Energy: 45, Valence: 60, Intensity: 35, Acousticness: 55, Density: 85}, "many layers, richly filled in"},
	"raucous":    {Axes{Energy: 78, Valence: 62, Intensity: 68, Acousticness: 45, Density: 70}, "loud, rowdy, spilling over"},
	"hypnotic":   {Axes{Energy: 45, Valence: 48, Intensity: 35, Acousticness: 30, Density: 50}, "repetition that pulls you under"},
	"stark":      {Axes{Energy: 30, Valence: 30, Intensity: 35, Acousticness: 45, Density: 15}, "bare and unsoftened"},
}

// Vocabulary is the sorted term list, held separately from Anchors so callers
// that show the model the vocabulary get a stable order. Go randomises map
// iteration, and a prompt whose term order changes between runs is a prompt the
// LLM cache cannot reuse.
var Vocabulary = func() []string {
	out := make([]string, 0, len(Anchors))
	for term := range Anchors {
		out = append(out, term)
	}
	sort.Strings(out)
	return out
}()

// Synonyms folds common alternatives onto canonical terms.
//
// These are language-level equivalences, not one library's habits: `angry` and
// `furious` name the same region regardless of whose collection it is.
var Synonyms = map[string]string{
	"angry": "furious", "ferocious": "furious", "enraged": "furious",
	"abrasive": "aggressive", "violent": "aggressive", "punishing": "aggressive",
	"sinister": "menacing", "ominous": "menacing", "dark": "menacing",
	"crushing": "heavy", "pounding": "heavy", "thunderous": "heavy", "bass-heavy": "heavy",
	"raw": "gritty", "scrappy": "gritty", "rough": "gritty",
	"distorted": "fuzzy", "saturated": "fuzzy",
	"slick": "glossy", "polished": "glossy", "sleek": "glossy", "clean": "glossy",
	"sparkling": "shimmering", "glistening": "shimmering", "twinkling": "shimmering",
	"throbbing": "pulsing", "pumping": "pulsing", "motorik": "pulsing",
	"icy": "cold", "clinical": "cold", "detached": "cold",
	"minimal": "sparse", "skeletal": "sparse", "spare": "sparse",
	"layered": "lush", "orchestral": "lush", "symphonic": "lush", "widescreen": "lush",
	"rowdy": "raucous", "boisterous": "raucous", "unruly": "raucous",
	"trancey": "hypnotic", "looping": "hypnotic", "droning": "hypnotic",
	"bare": "stark", "austere": "stark",
	"calm": "serene", "peaceful": "serene", "tranquil": "serene", "still": "serene",
	"soft": "gentle", "delicate": "gentle", "fragile": "gentle",
	"cosy": "warm", "cozy": "warm", "comforting": "warm",
	"laidback": "mellow", "laid-back": "mellow", "chill": "mellow", "relaxed": "mellow",
	"rustic": "pastoral", "folksy": "pastoral", "bucolic": "pastoral",
	"quiet": "hushed", "whispered": "hushed",
	"hazy": "dreamy", "ethereal": "dreamy", "woozy": "dreamy", "floaty": "dreamy",
	"sad": "melancholy", "sorrowful": "melancholy", "downcast": "melancholy",
	"grieving": "mournful", "elegiac": "mournful", "funereal": "mournful",
	"desolate": "bleak", "barren": "bleak", "grim": "bleak",
	"tired": "weary", "resigned": "weary", "worn": "weary",
	"yearning": "wistful", "longing": "wistful", "nostalgic": "wistful",
	"bright": "sunny", "cheerful": "sunny", "upbeat": "sunny",
	"charming": "sweet", "endearing": "sweet", "romantic": "sweet",
	"whimsical": "playful", "cheeky": "playful", "giddy": "playful",
	"breezily": "breezy", "carefree": "breezy", "easygoing": "breezy",
	"soulful": "groovy", "in-the-pocket": "groovy",
	"syncopated": "funky", "strutting": "funky",
	"swung": "swinging", "jazzy": "swinging",
	"jolly": "jaunty", "sprightly": "jaunty",
	"overcast": "moody", "sullen": "moody", "introspective": "moody",
	"simmering": "brooding", "ominously": "brooding",
	"anxious": "tense", "uneasy": "tense", "nervy": "tense",
	"agitated": "restless", "jittery": "restless", "antsy": "restless",
	"sultry": "smouldering", "smoldering": "smouldering", "seductive": "smouldering",
	"rebellious": "defiant", "confrontational": "defiant", "swaggering": "defiant",
	"ecstatic": "euphoric", "rapturous": "euphoric", "blissful": "euphoric",
	"joyful": "exuberant", "jubilant": "exuberant", "celebratory": "exuberant",
	"victorious": "triumphant", "heroic": "triumphant", "soaring": "triumphant",
	"singalong": "anthemic", "stadium": "anthemic", "rousing": "anthemic",
	"propulsive": "driving", "insistent": "driving", "motoring": "driving",
	"clubby": "danceable", "club-ready": "danceable", "dancey": "danceable", "party": "danceable",
	"chaotic": "frantic", "breakneck": "frantic", "manic": "frantic",
	"brutal": "savage", "vicious": "savage", "feral": "savage",
}

// Validate reports every problem with the vocabulary and the regions. It must
// return nil before a single tag is written; a violation is not cosmetic, it
// silently fragments a term across the entire library or writes a vibe name the
// connector's VIBE_SCHEDULE has no key for.
func Validate() error {
	var problems []string

	for term, a := range Anchors {
		problems = append(problems, checkName("term", term)...)
		problems = append(problems, checkAxes("term "+term, a.Axes)...)
		if strings.TrimSpace(a.Gloss) == "" {
			problems = append(problems, fmt.Sprintf("term %q has no gloss", term))
		}
	}

	for from, to := range Synonyms {
		problems = append(problems, checkName("synonym key", from)...)
		if _, ok := Anchors[to]; !ok {
			problems = append(problems, fmt.Sprintf(
				"synonym %q maps to %q which is not a term", from, to))
		}
		if _, ok := Anchors[from]; ok {
			problems = append(problems, fmt.Sprintf(
				"%q is both a term and a synonym key", from))
		}
	}

	for name, r := range Regions {
		problems = append(problems, checkName("region", name)...)
		problems = append(problems, checkAxes("region "+name, r.Centre)...)
		if r.Radius <= 0 {
			problems = append(problems, fmt.Sprintf("region %q has radius %v", name, r.Radius))
		}
		if v := r.Valence; v != nil && (v[0] < 0 || v[1] > 100 || v[0] > v[1]) {
			problems = append(problems, fmt.Sprintf("region %q has valence bound %v", name, *v))
		}
		for _, t := range r.Tempo {
			if !contains(TempoFeels, t) {
				problems = append(problems, fmt.Sprintf("region %q constrains tempo to %q, which is not a tempo feel", name, t))
			}
		}
		for _, v := range r.Vocal {
			if !contains(VocalKinds, v) {
				problems = append(problems, fmt.Sprintf("region %q constrains vocal to %q, which is not a vocal kind", name, v))
			}
		}
		if strings.TrimSpace(r.Gloss) == "" {
			problems = append(problems, fmt.Sprintf("region %q has no gloss", name))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("mood vocabulary is invalid:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// checkName applies the rules every written string shares: it has to survive
// Navidrome's tag splitting, and it has to be in the form Canonical normalises
// to, or a lookup can never reach it.
func checkName(kind, s string) []string {
	var problems []string
	switch {
	case s == "":
		problems = append(problems, "empty "+kind)
	case strings.ContainsAny(s, Separators):
		problems = append(problems, fmt.Sprintf(
			"%s %q contains a separator (%q) and would be split into several values",
			kind, s, Separators))
	}
	// Canonical lowercases and trims before lookup, so a key that is not already
	// in that form can never match and is silently dead. One such entry
	// ("aggressive " with a trailing space) shipped and was only caught because
	// the plugin logged a synonym count one lower than expected.
	if norm := strings.ToLower(strings.TrimSpace(s)); norm != s {
		problems = append(problems, fmt.Sprintf(
			"%s %q is not lowercase-trimmed, so a lookup can never match it", kind, s))
	}
	return problems
}

func checkAxes(what string, a Axes) []string {
	var problems []string
	for _, axis := range []struct {
		name string
		v    int
	}{
		{"energy", a.Energy}, {"valence", a.Valence}, {"intensity", a.Intensity},
		{"acousticness", a.Acousticness}, {"density", a.Density},
	} {
		if axis.v < 0 || axis.v > 100 {
			problems = append(problems, fmt.Sprintf(
				"%s has %s %d, outside 0-100", what, axis.name, axis.v))
		}
	}
	return problems
}

func contains[T comparable](haystack []T, needle T) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// Canonical folds a raw descriptor onto a vocabulary term, reporting whether it
// belongs in the tag at all. Anything that matches nothing is dropped by the
// caller rather than written verbatim, which is how an uncontrolled tail grows
// in the first place.
//
// Multi-word input is retried with spaces and underscores squashed to hyphens,
// because a model asked for `laid back` and a model asked for `laid-back` mean
// the same thing and only one of them is a key.
func Canonical(raw string) (string, bool) {
	w := strings.ToLower(strings.TrimSpace(raw))
	if w == "" {
		return "", false
	}
	if _, ok := Anchors[w]; ok {
		return w, true
	}
	if v, ok := Synonyms[w]; ok {
		return v, true
	}
	squashed := hyphenate(w)
	if squashed != w {
		if _, ok := Anchors[squashed]; ok {
			return squashed, true
		}
		if v, ok := Synonyms[squashed]; ok {
			return v, true
		}
	}
	return "", false
}

func hyphenate(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevHyphen := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '_' {
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
			continue
		}
		prevHyphen = false
		b.WriteRune(r)
	}
	return b.String()
}

// TagValues turns the model's descriptors into the ordered, deduplicated set of
// terms to write as repeated MOOD fields, capped at max.
//
// Order follows first appearance so the model's own sense of which descriptor
// matters most survives into the file. Duplicates are dropped: two descriptors
// can legitimately fold onto the same term (sorrowful and downcast both become
// melancholy), and a repeated identical value would show up twice in Navidrome.
func TagValues(raw []string, max int) []string {
	if max <= 0 {
		return nil
	}
	var out []string
	seen := make(map[string]bool, max)
	for _, r := range raw {
		c, ok := Canonical(r)
		if !ok || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
		if len(out) >= max {
			break
		}
	}
	return out
}
