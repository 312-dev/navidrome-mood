package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/312-dev/navidrome-mood/internal/mood"
)

// schemaProps digs out the per-label property block both providers send.
func schemaProps(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	var s struct {
		Properties struct {
			Labels struct {
				Items struct {
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				} `json:"items"`
			} `json:"labels"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(labelSchema(), &s); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	return s.Properties.Labels.Items.Properties
}

// The five axes plus tempo and vocal are read all-or-nothing, so any one of them
// left optional is a track the connector will discard whole.
func TestSchemaRequiresEveryAllOrNothingField(t *testing.T) {
	var s struct {
		Properties struct {
			Labels struct {
				Items struct {
					Required []string `json:"required"`
				} `json:"items"`
			} `json:"labels"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(labelSchema(), &s); err != nil {
		t.Fatal(err)
	}
	req := map[string]bool{}
	for _, r := range s.Properties.Labels.Items.Required {
		req[r] = true
	}
	for _, want := range []string{
		"id", "energy", "valence", "intensity", "acousticness", "density",
		"tempo", "vocal", "moods",
	} {
		if !req[want] {
			t.Errorf("%q is not required, so the model may omit it", want)
		}
	}
	props := schemaProps(t)
	if _, ok := props["organic"]; ok {
		t.Error("schema still asks for `organic`; acousticness replaced it")
	}
}

// The whole point of generating the enums: a request that asks for a value
// Validate rejects would keep working and reject every label it received.
func TestSchemaEnumsComeFromTheMoodPackage(t *testing.T) {
	props := schemaProps(t)
	for _, c := range []struct {
		field string
		want  []string
	}{
		{"tempo", asStrings(mood.TempoFeels)},
		{"vocal", asStrings(mood.VocalKinds)},
		{"times", TimeSlots},
	} {
		var probe struct {
			Enum  []string `json:"enum"`
			Items struct {
				Enum []string `json:"enum"`
			} `json:"items"`
		}
		if err := json.Unmarshal(props[c.field], &probe); err != nil {
			t.Fatalf("%s: %v", c.field, err)
		}
		got := probe.Enum
		if got == nil {
			got = probe.Items.Enum
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s enum = %v, want %v", c.field, got, c.want)
		}
	}
}

func asStrings[T ~string](vals []T) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	return out
}

func validLabel() Label {
	return Label{ID: "t1", Energy: 50, Valence: 50, Intensity: 50,
		Acousticness: 50, Density: 50, Tempo: "mid", Vocal: "sung",
		Moods: []string{"warm", "wistful"}}
}

func TestValidateAcceptsAWellFormedLabel(t *testing.T) {
	if err := validLabel().Validate(); err != nil {
		t.Fatalf("a valid label was rejected: %v", err)
	}
}

// Rejected, never clamped. A clamped 140 becomes a confident 100 that reads as a
// real judgement and cannot be told apart from one afterwards.
func TestValidateRejectsRatherThanClamps(t *testing.T) {
	cases := map[string]func(*Label){
		"energy above 100":  func(l *Label) { l.Energy = 140 },
		"valence below 0":   func(l *Label) { l.Valence = -1 },
		"intensity above":   func(l *Label) { l.Intensity = 101 },
		"acousticness":      func(l *Label) { l.Acousticness = 1000 },
		"density below 0":   func(l *Label) { l.Density = -20 },
		"tempo not in set":  func(l *Label) { l.Tempo = "moderato" },
		"tempo empty":       func(l *Label) { l.Tempo = "" },
		"vocal not in set":  func(l *Label) { l.Vocal = "screamed" },
		"vocal wrong case":  func(l *Label) { l.Vocal = "Sung" },
		"tempo wrong case":  func(l *Label) { l.Tempo = "MID" },
		"vocal empty":       func(l *Label) { l.Vocal = "" },
		"several at once":   func(l *Label) { l.Energy = -5; l.Vocal = "hummed" },
		"axis at exactly 0": nil, // valid, checked below
	}
	for name, mutate := range cases {
		if mutate == nil {
			continue
		}
		l := validLabel()
		mutate(&l)
		err := l.Validate()
		if err == nil {
			t.Errorf("%s: accepted %+v", name, l)
			continue
		}
		if !errors.Is(err, ErrInvalidLabel) {
			t.Errorf("%s: error does not wrap ErrInvalidLabel: %v", name, err)
		}
		if !strings.Contains(err.Error(), l.ID) {
			t.Errorf("%s: error does not name the track: %v", name, err)
		}
	}

	// The bounds themselves are inclusive; 0 and 100 are ordinary answers.
	for _, v := range []int{0, 100} {
		l := validLabel()
		l.Energy, l.Valence, l.Intensity, l.Acousticness, l.Density = v, v, v, v, v
		if err := l.Validate(); err != nil {
			t.Errorf("axis value %d was rejected: %v", v, err)
		}
	}
}

// The five axes plus tempo and vocal are the coordinates region membership is
// measured against, so Point must carry all seven.
func TestPointCarriesEveryCoordinate(t *testing.T) {
	l := Label{Energy: 10, Valence: 20, Intensity: 30, Acousticness: 40,
		Density: 50, Tempo: "slow", Vocal: "instrumental"}
	p := l.Point()
	want := mood.Point{
		Axes:  mood.Axes{Energy: 10, Valence: 20, Intensity: 30, Acousticness: 40, Density: 50},
		Tempo: "slow", Vocal: "instrumental",
	}
	if p != want {
		t.Fatalf("Point() = %+v, want %+v", p, want)
	}
}

// moodtime sits outside the all-or-nothing set, so an unrecognised slot is
// dropped rather than costing the track its other nine tags.
func TestKnownTimesFiltersAndDedupes(t *testing.T) {
	got := KnownTimes([]string{"Morning", "brunch", "morning", " late night "})
	if strings.Join(got, "|") != "morning|late night" {
		t.Fatalf("KnownTimes = %v, want [morning late night]", got)
	}
	if KnownTimes(nil) != nil {
		t.Fatal("KnownTimes(nil) should stay nil so the tag is omitted, not blanked")
	}
}
