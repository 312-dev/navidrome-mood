package mood

import (
	"sort"
	"strings"
	"testing"
)

// The gate. If this fails, nothing may write a tag: a separator in any term
// fragments that mood across the entire library and still looks like it worked.
func TestVocabularyIsValid(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	if got := len(Anchors); got != 52 {
		t.Errorf("got %d terms, want 52", got)
	}
	if got := len(Synonyms); got != 146 {
		t.Errorf("got %d synonyms, want 146", got)
	}
	if got := len(Regions); got != 14 {
		t.Errorf("got %d regions, want 14", got)
	}
}

// Validate must actually detect each class of problem. Without this the gate
// above could pass while checking nothing.
func TestValidateDetectsEachProblem(t *testing.T) {
	origAnchors, origSyn, origRegions := Anchors, Synonyms, Regions
	t.Cleanup(func() { Anchors, Synonyms, Regions = origAnchors, origSyn, origRegions })

	ok := Anchor{Axes{Energy: 50, Valence: 50, Intensity: 50, Acousticness: 50, Density: 50}, "fine"}
	okRegion := Region{Centre: Axes{Energy: 50, Valence: 50, Intensity: 50, Acousticness: 50, Density: 50}, Radius: 20, Gloss: "fine"}

	cases := map[string]struct {
		anchors map[string]Anchor
		syn     map[string]string
		regions map[string]Region
		want    string
	}{
		"comma in term":       {anchors: map[string]Anchor{"warm, bright": ok}, want: "separator"},
		"semicolon in term":   {anchors: map[string]Anchor{"warm;bright": ok}, want: "separator"},
		"slash in term":       {anchors: map[string]Anchor{"funk/soul": ok}, want: "separator"},
		"uppercase term":      {anchors: map[string]Anchor{"Wistful": ok}, want: "never match"},
		"padded term":         {anchors: map[string]Anchor{" warm": ok}, want: "never match"},
		"empty term":          {anchors: map[string]Anchor{"": ok}, want: "empty term"},
		"missing gloss":       {anchors: map[string]Anchor{"warm": {ok.Axes, "  "}}, want: "no gloss"},
		"axis above 100":      {anchors: map[string]Anchor{"warm": {Axes{Energy: 101}, "g"}}, want: "outside 0-100"},
		"axis below 0":        {anchors: map[string]Anchor{"warm": {Axes{Valence: -1}, "g"}}, want: "outside 0-100"},
		"dangling synonym":    {syn: map[string]string{"toasty": "nope"}, want: "not a term"},
		"synonym shadows":     {anchors: map[string]Anchor{"warm": ok}, syn: map[string]string{"warm": "warm"}, want: "both a term and a synonym"},
		"separator in region": {regions: map[string]Region{"wind, down": okRegion}, want: "separator"},
		"zero radius":         {regions: map[string]Region{"wind down": {Centre: okRegion.Centre, Gloss: "g"}}, want: "radius"},
		"inverted valence":    {regions: map[string]Region{"wind down": {Centre: okRegion.Centre, Radius: 20, Valence: valenceBound(80, 20), Gloss: "g"}}, want: "valence bound"},
		"unknown tempo":       {regions: map[string]Region{"wind down": {Centre: okRegion.Centre, Radius: 20, Tempo: []TempoFeel{"brisk"}, Gloss: "g"}}, want: "not a tempo feel"},
		"unknown vocal":       {regions: map[string]Region{"wind down": {Centre: okRegion.Centre, Radius: 20, Vocal: []VocalKind{"hummed"}, Gloss: "g"}}, want: "not a vocal kind"},

		// Shipped once as "aggressive " and was dead: Canonical trims and
		// lowercases its input before lookup, so a padded or capitalised key can
		// never be reached, and the only symptom is a count one lower than
		// expected.
		"synonym key not trimmed":   {anchors: map[string]Anchor{"warm": ok}, syn: map[string]string{"toasty ": "warm"}, want: "never match"},
		"synonym key not lowercase": {anchors: map[string]Anchor{"warm": ok}, syn: map[string]string{"Toasty": "warm"}, want: "never match"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			Anchors, Synonyms, Regions = tc.anchors, tc.syn, tc.regions
			if Anchors == nil {
				Anchors = map[string]Anchor{}
			}
			if Synonyms == nil {
				Synonyms = map[string]string{}
			}
			if Regions == nil {
				Regions = map[string]Region{}
			}

			err := Validate()
			if err == nil {
				t.Fatalf("accepted the input, want rejection mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// Everything the plugin writes to a tag goes through Navidrome's splitter, and
// a term that fragments still looks like it worked. Checked here as well as in
// Validate because these three sets are what actually reach a file.
func TestNothingWrittenContainsASeparator(t *testing.T) {
	for _, s := range Vocabulary {
		if strings.ContainsAny(s, Separators) {
			t.Errorf("term %q would be split by Navidrome", s)
		}
	}
	for _, s := range RegionNames {
		if strings.ContainsAny(s, Separators) {
			t.Errorf("region %q would be split by Navidrome", s)
		}
	}
	for s := range Synonyms {
		if strings.ContainsAny(s, Separators) {
			t.Errorf("synonym key %q would be split by Navidrome", s)
		}
	}
}

// A synonym pointing at a term that does not exist, or one Canonical cannot
// reach, fails silently: the descriptor is simply dropped and the track ends up
// with fewer moods than the model gave it.
func TestEverySynonymResolvesAndIsReachable(t *testing.T) {
	for from, to := range Synonyms {
		if _, ok := Anchors[to]; !ok {
			t.Errorf("synonym %q maps to %q, which is not a term", from, to)
		}
		if _, ok := Anchors[from]; ok {
			t.Errorf("%q is both a term and a synonym key, so the synonym is dead", from)
		}
		if norm := strings.ToLower(strings.TrimSpace(from)); norm != from {
			t.Errorf("synonym key %q is not lowercase-trimmed, so Canonical can never reach it", from)
		}
		got, ok := Canonical(from)
		if !ok || got != to {
			t.Errorf("Canonical(%q) = (%q, %v), want (%q, true)", from, got, ok, to)
		}
	}
}

// The axes are declared 0-100 in the tag contract, and the connector treats a
// track with any axis out of range as unlabelled. An anchor or centre outside
// the range would define a term or region no real track can sit at.
func TestAnchorsAndCentresAreInRange(t *testing.T) {
	check := func(what string, a Axes) {
		t.Helper()
		for name, v := range map[string]int{
			"energy": a.Energy, "valence": a.Valence, "intensity": a.Intensity,
			"acousticness": a.Acousticness, "density": a.Density,
		} {
			if v < 0 || v > 100 {
				t.Errorf("%s has %s %d, outside 0-100", what, name, v)
			}
		}
	}
	for term, a := range Anchors {
		check("term "+term, a.Axes)
	}
	for name, r := range Regions {
		check("region "+name, r.Centre)
	}
}

// Vocabulary is what the prompt shows the model, so it has to be exactly the
// anchor set in an order that does not change between runs.
func TestVocabularyMirrorsAnchorsInStableOrder(t *testing.T) {
	if len(Vocabulary) != len(Anchors) {
		t.Fatalf("Vocabulary has %d entries, Anchors has %d", len(Vocabulary), len(Anchors))
	}
	if !sort.StringsAreSorted(Vocabulary) {
		t.Error("Vocabulary is not sorted, so the prompt changes between runs")
	}
	for _, term := range Vocabulary {
		if _, ok := Anchors[term]; !ok {
			t.Errorf("Vocabulary lists %q, which has no anchor", term)
		}
	}
}

// RegionNames is the iteration order VibesFor relies on. A name in one and not
// the other means a region is either never matched or looked up as a zero value
// centred on the origin.
func TestRegionNamesCoverRegionsExactly(t *testing.T) {
	if len(RegionNames) != len(Regions) {
		t.Fatalf("RegionNames has %d entries, Regions has %d", len(RegionNames), len(Regions))
	}
	seen := map[string]bool{}
	for _, name := range RegionNames {
		if _, ok := Regions[name]; !ok {
			t.Errorf("RegionNames lists %q, which is not a region", name)
		}
		if seen[name] {
			t.Errorf("RegionNames lists %q twice", name)
		}
		seen[name] = true
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
		"synonym":          {"sorrowful", "melancholy", true},
		"synonym cased":    {"Angry", "furious", true},
		"hyphenated key":   {"bass-heavy", "heavy", true},
		"spaced to hyphen": {"laid back", "mellow", true},
		"underscored":      {"in_the_pocket", "groovy", true},
		"unknown":          {"zorbular", "", false},
		"empty":            {"", "", false},
		"whitespace only":  {"   ", "", false},
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

func TestTagValuesDedupesCapsAndPreservesOrder(t *testing.T) {
	// sorrowful and downcast both fold onto melancholy; the tag must not repeat
	// it, and an unknown descriptor must not consume a slot.
	got := TagValues([]string{"sorrowful", "menacing", "downcast", "zorbular", "sparse", "lush"}, 4)
	want := []string{"melancholy", "menacing", "sparse", "lush"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v, want %v", got, want)
	}

	if got := TagValues([]string{"sorrowful", "menacing", "sparse"}, 2); len(got) != 2 {
		t.Fatalf("cap ignored: got %v", got)
	}
	if got := TagValues([]string{"sorrowful"}, 0); got != nil {
		t.Fatalf("max 0 returned %v, want nil", got)
	}
}

func TestTagValuesDropsUnknownRatherThanFailing(t *testing.T) {
	// A descriptor that folds onto nothing is dropped, not written verbatim.
	// Writing it is how the uncontrolled tail this vocabulary replaced grew.
	if got := TagValues([]string{"zorbular", "wistful"}, 4); len(got) != 1 || got[0] != "wistful" {
		t.Fatalf("got %v, want [wistful]", got)
	}
	if got := TagValues([]string{"zorbular"}, 4); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

// The measurement the whole geometry exists for.
//
// Debussy's Suite bergamasque and Metallica's Nothing Else Matters are both
// legitimately "tender", and a labeller working from words alone will put them
// in the same playlist. Their coordinates are not close: intensity 8 against 55
// and acousticness 95 against 60 is the pair the distance weights are tuned on.
// If they ever share a region, either the weights or a radius has moved and
// cohesion by vibe has quietly stopped working.
func TestTheTenderPairLandsInDifferentRegions(t *testing.T) {
	debussy := Point{
		Axes:  Axes{Energy: 12, Valence: 60, Intensity: 8, Acousticness: 95, Density: 18},
		Tempo: "slow", Vocal: "instrumental",
	}
	metallica := Point{
		Axes:  Axes{Energy: 45, Valence: 35, Intensity: 55, Acousticness: 60, Density: 50},
		Tempo: "slow", Vocal: "sung",
	}

	// The weights are what has to hold this pair apart, so assert on them
	// directly. Region membership depends on the radii too, and a test that only
	// checked membership would pass for the wrong reason the moment one of the
	// two stopped landing anywhere. Two tracks picked at random from a real
	// library sit about 18 apart; this pair has to be conspicuously further.
	const apart = 30
	if d := Distance(debussy.Axes, metallica.Axes); d < apart {
		t.Errorf("the tender pair is %.1f apart, under %d: the distance weights "+
			"have moved and words-only cohesion is back", d, apart)
	}

	left := vibeSet(VibesFor(debussy, len(RegionNames)))
	right := vibeSet(VibesFor(metallica, len(RegionNames)))
	// Landing in no region is a legitimate answer for either of them and says
	// nothing is wrong: Nothing Else Matters sits 16.0 from its nearest centre,
	// which is roughly the median gap between any two tracks in a library, and a
	// track that middling belongs to nothing in particular.
	for name := range left {
		if right[name] {
			t.Errorf("both tracks are in %q despite %.1f apart in mood-space",
				name, Distance(debussy.Axes, metallica.Axes))
		}
	}
}

// A hard constraint has to beat distance outright. A point at the exact centre
// of `focus` is as close as it is possible to be, and a sung track still does
// not belong there: the name makes a claim about vocals that a mean over five
// numeric axes cannot enforce.
func TestVibesForEnforcesConstraintsBeforeDistance(t *testing.T) {
	centre := Regions["focus"].Centre

	instrumental := Point{Axes: centre, Tempo: "mid", Vocal: "instrumental"}
	got := VibesFor(instrumental, len(RegionNames))
	if !vibeSet(got)["focus"] {
		t.Fatalf("a point at the centre of focus is not in focus: %v", got)
	}
	if got[0].Vibe != "focus" || got[0].Distance != 0 {
		t.Errorf("nearest match is %v, want focus at distance 0", got[0])
	}

	sung := Point{Axes: centre, Tempo: "mid", Vocal: "sung"}
	if vibeSet(VibesFor(sung, len(RegionNames)))["focus"] {
		t.Error("a sung track at the centre of focus was admitted; distance 0 beat the constraint")
	}

	// The same logic on the other two constraint kinds.
	loud := Point{Axes: Regions["melancholy"].Centre, Tempo: "slow", Vocal: "sung"}
	loud.Valence = 90
	if vibeSet(VibesFor(loud, len(RegionNames)))["melancholy"] {
		t.Error("a valence-90 point was admitted to melancholy")
	}
	still := Point{Axes: Regions["workout"].Centre, Tempo: "still", Vocal: "sung"}
	if vibeSet(VibesFor(still, len(RegionNames)))["workout"] {
		t.Error("a still track was admitted to workout")
	}
}

func TestVibesForCapsAndOrders(t *testing.T) {
	p := Point{
		Axes:  Axes{Energy: 44, Valence: 64, Intensity: 28, Acousticness: 60, Density: 42},
		Tempo: "mid", Vocal: "sung",
	}
	all := VibesFor(p, len(RegionNames))
	if len(all) < 2 {
		t.Fatalf("expected an overlapping point to match several regions, got %v", all)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Distance > all[i].Distance {
			t.Fatalf("results are not nearest-first: %v", all)
		}
	}
	if got := VibesFor(p, 1); len(got) != 1 || got[0] != all[0] {
		t.Fatalf("cap of 1 gave %v, want the nearest of %v", got, all)
	}
	if got := VibesFor(p, 0); got != nil {
		t.Fatalf("cap of 0 gave %v, want nil", got)
	}
}

// A backstop against a region growing to swallow the coordinate space, on a
// fixed grid so a hand-edited radius cannot quietly reinflate one.
//
// Read what this does and does not prove. It sweeps a UNIFORM grid, and a real
// library is nothing like uniform, so passing here says only that a region is
// not absurd in the abstract. It is not the calibration test and it cannot be:
// every radius in the set passed this at 15% while `driving` was tagging 45% of
// a real collection. TestEveryRegionIsUsableOnALibrary in library_test.go is
// the one that measures against a library-shaped distribution, and it is the one
// that fails when a radius drifts.
//
// The shares logged here therefore run well under a region's real share. They
// are also cut further by the hard constraints, which the sweep covers: a region
// admitting two of five tempo feels reaches two fifths of the points its radius
// encloses.
func TestNoRegionSwallowsMoodSpace(t *testing.T) {
	const (
		step  = 10
		limit = 0.15
	)
	inside := make(map[string]int, len(RegionNames))
	total := 0

	for energy := 0; energy <= 100; energy += step {
		for valence := 0; valence <= 100; valence += step {
			for intensity := 0; intensity <= 100; intensity += step {
				for acousticness := 0; acousticness <= 100; acousticness += step {
					for density := 0; density <= 100; density += step {
						axes := Axes{energy, valence, intensity, acousticness, density}
						for _, tempo := range TempoFeels {
							for _, vocal := range VocalKinds {
								total++
								for _, m := range VibesFor(Point{axes, tempo, vocal}, len(RegionNames)) {
									inside[m.Vibe]++
								}
							}
						}
					}
				}
			}
		}
	}

	for _, name := range RegionNames {
		share := float64(inside[name]) / float64(total)
		if share > limit {
			t.Errorf("region %q covers %.1f%% of mood-space, over the %.0f%% limit",
				name, share*100, limit*100)
		}
		if inside[name] == 0 {
			t.Errorf("region %q covers nothing, so no track can ever be tagged with it", name)
		}
		t.Logf("%-13s %5.2f%%", name, share*100)
	}
}

// Valence is the cheap axis on purpose: a sad folk tune and a happy folk tune
// sequence together fine, while the Debussy/Metallica gap in intensity and
// acousticness is the clash worth paying for.
func TestDistanceWeightsValenceCheapest(t *testing.T) {
	base := Axes{Energy: 50, Valence: 50, Intensity: 50, Acousticness: 50, Density: 50}
	if d := Distance(base, base); d != 0 {
		t.Fatalf("Distance to itself is %v, want 0", d)
	}

	shift := func(f func(*Axes)) float64 {
		other := base
		f(&other)
		return Distance(base, other)
	}
	byValence := shift(func(a *Axes) { a.Valence += 40 })
	byIntensity := shift(func(a *Axes) { a.Intensity += 40 })
	byAcousticness := shift(func(a *Axes) { a.Acousticness += 40 })

	if byValence >= byIntensity || byValence >= byAcousticness {
		t.Errorf("a 40-point valence gap costs %.1f, intensity %.1f, acousticness %.1f; valence must be cheapest",
			byValence, byIntensity, byAcousticness)
	}
}

func vibeSet(ms []Match) map[string]bool {
	out := make(map[string]bool, len(ms))
	for _, m := range ms {
		out[m.Vibe] = true
	}
	return out
}
