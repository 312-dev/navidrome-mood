package mood

import (
	"strings"
	"testing"
)

// The gate. If this fails, nothing may write a tag: a separator in any term
// fragments that mood across the entire library and still looks like it worked.
func TestVocabularyIsValid(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	if len(Vocabulary) < 20 {
		t.Fatalf("vocabulary has only %d terms, which is too coarse to be useful", len(Vocabulary))
	}
}

// Validate must actually detect each class of problem. Without this, the gate
// above could pass while checking nothing.
func TestValidateDetectsEachProblem(t *testing.T) {
	origVocab, origSyn := Vocabulary, Synonyms
	origIndex := index
	t.Cleanup(func() { Vocabulary, Synonyms, index = origVocab, origSyn, origIndex })

	cases := map[string]struct {
		vocab []string
		syn   map[string]string
		want  string
	}{
		"comma separator":     {vocab: []string{"up-tempo, driving"}, want: "separator"},
		"semicolon separator": {vocab: []string{"warm;bright"}, want: "separator"},
		"slash separator":     {vocab: []string{"funk/soul"}, want: "separator"},
		"uppercase":           {vocab: []string{"Wistful"}, want: "lowercase"},
		"whitespace":          {vocab: []string{" warm"}, want: "whitespace"},
		"empty":               {vocab: []string{""}, want: "empty"},
		"duplicate":           {vocab: []string{"warm", "warm"}, want: "duplicated"},
		"dangling synonym":    {vocab: []string{"warm"}, syn: map[string]string{"toasty": "nope"}, want: "not in the vocabulary"},
		"synonym shadows term": {
			vocab: []string{"warm"}, syn: map[string]string{"warm": "warm"}, want: "both a vocabulary term and a synonym",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			Vocabulary = tc.vocab
			Synonyms = tc.syn
			if Synonyms == nil {
				Synonyms = map[string]string{}
			}
			index = map[string]string{}
			for _, v := range Vocabulary {
				index[v] = v
			}

			err := Validate()
			if err == nil {
				t.Fatalf("accepted %v, want rejection mentioning %q", tc.vocab, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestCanonical(t *testing.T) {
	cases := map[string]struct {
		in   string
		want string
		ok   bool
	}{
		"exact":            {"wistful", "wistful", true},
		"mixed case":       {"Wistful", "wistful", true},
		"padded":           {"  wistful  ", "wistful", true},
		"synonym":          {"melancholic", "melancholy", true},
		"synonym cased":    {"Bleak", "dark", true},
		"hyphenated term":  {"laid-back", "laid-back", true},
		"unspaced synonym": {"laidback", "laid-back", true},
		"unknown":          {"zorbular", "", false},
		"empty":            {"", "", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, ok := Canonical(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("Canonical(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestTagValuesDedupesAndPreservesOrder(t *testing.T) {
	// bleak and nihilistic both canonicalise to dark; the tag must not repeat it.
	got := TagValues([]string{"bleak", "menacing", "nihilistic", "zorbular", "menacing"})
	want := []string{"dark", "menacing"}

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestTagValuesDropsUnknownRatherThanFailing(t *testing.T) {
	// Unknown descriptors are kept in kvstore by the caller, not written to the
	// tag. Returning nothing at all would be a silent data loss bug.
	if got := TagValues([]string{"zorbular", "wistful"}); len(got) != 1 || got[0] != "wistful" {
		t.Fatalf("got %v, want [wistful]", got)
	}
	if got := TagValues([]string{"zorbular"}); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

// Every canonical output must itself be writable, i.e. separator-free. This
// catches a synonym that maps to a malformed term.
func TestEveryCanonicalOutputIsSeparatorFree(t *testing.T) {
	all := append([]string{}, Vocabulary...)
	for from := range Synonyms {
		if c, ok := Canonical(from); ok {
			all = append(all, c)
		}
	}
	for _, term := range all {
		if strings.ContainsAny(term, Separators) {
			t.Fatalf("%q would be split by Navidrome into multiple moods", term)
		}
	}
}
