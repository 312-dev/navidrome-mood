package mood

import (
	"math"
	"sort"
)

// Region is a named area of mood-space. The set is deliberately functional
// ("what is this for") rather than personal, so a collection with no curated
// playlists still has a working taxonomy on first run.
type Region struct {
	Centre Axes
	// Radius is the membership boundary, in the same units Distance returns.
	//
	// Calibrated so each region holds about 8% of a real library. That is a
	// different basis from the one these radii were first fitted against, and the
	// difference is most of their value: an earlier set was tuned to cover ~7% of
	// UNIFORM five-axis space, on the assumption that a library spreads through
	// the cube. It does not. Measured over 9,195 labelled tracks, two tracks
	// picked at random sit 17.7 apart at the median, where two uniform points sit
	// 39.1 apart. Radii of 18 to 24 were therefore wider than the typical gap
	// between any two tracks in the collection, and `driving` tagged 45% of the
	// library while `focus` reached 111 tracks. A tag that half the library
	// carries is not a shortcut to anything.
	//
	// Fitting to a share of the library rather than of the space is what makes
	// the number mean something to the person querying it: every vibe returns a
	// pool big enough to build from and small enough to have chosen. Where a hard
	// constraint makes the eligible pool too small to supply that share, the
	// radius is capped at 55% of what is eligible instead, so the geometry keeps
	// doing work rather than rubber-stamping the constraint. `focus` is the only
	// region that hits it: this library holds 387 instrumental tracks, and asking
	// for 8% of 9,195 would admit every one of them.
	//
	// Regions are shortcuts, not a partition. About a third of a library lands in
	// no region at all, which is the expected outcome and not a gap: those tracks
	// are still reachable by axis range and by vocabulary term.
	Radius float64

	// Valence, Tempo and Vocal are hard constraints, nil when unconstrained. A
	// track outside one is excluded however close it sits, because a mean
	// distance over five axes cannot enforce a requirement on one of them: four
	// agreeing axes wash out a 40-point gap on the fifth. That is fine for "is
	// this nearby" and wrong for "is this sad", so the regions whose NAME makes
	// an affect claim carry a valence bound, exactly as `focus` carries an
	// instrumental one.
	Valence *[2]int
	Tempo   []TempoFeel
	Vocal   []VocalKind

	Gloss string
}

// Regions are the vibe names written to the `vibe` tag. The names are the seam
// with the connector: they must match its VIBE_SCHEDULE keys exactly, since that
// is where each region's hours live. Which hours suit a region is a playlist
// question and the labeller has no opinion about clocks, so no hours here.
var Regions = map[string]Region{
	"wind down":    {Centre: Axes{Energy: 20, Valence: 52, Intensity: 14, Acousticness: 78, Density: 24}, Radius: 13, Tempo: []TempoFeel{"still", "slow"}, Gloss: "settling toward sleep"},
	"slow morning": {Centre: Axes{Energy: 32, Valence: 62, Intensity: 22, Acousticness: 70, Density: 34}, Radius: 9, Tempo: []TempoFeel{"slow", "mid"}, Gloss: "easing into the day"},
	"focus":        {Centre: Axes{Energy: 42, Valence: 50, Intensity: 28, Acousticness: 40, Density: 40}, Radius: 20.5, Vocal: []VocalKind{"instrumental"}, Gloss: "steady, undemanding, stays out of the way"},
	"background":   {Centre: Axes{Energy: 38, Valence: 58, Intensity: 25, Acousticness: 55, Density: 38}, Radius: 7.5, Gloss: "pleasant and unobtrusive"},
	"uplift":       {Centre: Axes{Energy: 68, Valence: 80, Intensity: 42, Acousticness: 45, Density: 58}, Radius: 9, Valence: valenceBound(55, 100), Gloss: "a deliberate lift in mood"},
	"workout":      {Centre: Axes{Energy: 84, Valence: 62, Intensity: 70, Acousticness: 25, Density: 72}, Radius: 10.5, Tempo: []TempoFeel{"driving", "frantic"}, Gloss: "sustained physical push"},
	"hype":         {Centre: Axes{Energy: 86, Valence: 70, Intensity: 66, Acousticness: 18, Density: 74}, Radius: 13, Valence: valenceBound(50, 100), Gloss: "getting up for something"},
	"driving":      {Centre: Axes{Energy: 70, Valence: 60, Intensity: 58, Acousticness: 40, Density: 62}, Radius: 9.5, Tempo: []TempoFeel{"mid", "driving"}, Gloss: "motion; miles passing"},
	"golden hour":  {Centre: Axes{Energy: 48, Valence: 68, Intensity: 32, Acousticness: 55, Density: 46}, Radius: 7, Valence: valenceBound(45, 100), Gloss: "warm light, day easing off"},
	"late night":   {Centre: Axes{Energy: 40, Valence: 40, Intensity: 38, Acousticness: 35, Density: 45}, Radius: 7.5, Gloss: "after hours; low light"},
	"melancholy":   {Centre: Axes{Energy: 28, Valence: 24, Intensity: 24, Acousticness: 65, Density: 32}, Radius: 14.5, Valence: valenceBound(0, 45), Gloss: "sitting with something sad"},
	"heavy":        {Centre: Axes{Energy: 80, Valence: 32, Intensity: 84, Acousticness: 28, Density: 78}, Radius: 13.5, Valence: valenceBound(0, 55), Gloss: "loud, dark and physical"},
	"dinner":       {Centre: Axes{Energy: 40, Valence: 66, Intensity: 26, Acousticness: 68, Density: 40}, Radius: 8, Gloss: "convivial but not competing with conversation"},
	"party":        {Centre: Axes{Energy: 80, Valence: 82, Intensity: 50, Acousticness: 20, Density: 72}, Radius: 10, Valence: valenceBound(55, 100), Gloss: "a room full of people"},
}

// RegionNames fixes the order VibesFor considers regions in, so two equidistant
// regions always resolve the same way. Go randomises map iteration, which would
// otherwise make a track's vibe tags differ between two runs over the same file.
var RegionNames = []string{
	"wind down", "slow morning", "focus", "background", "uplift", "workout",
	"hype", "driving", "golden hour", "late night", "melancholy", "heavy",
	"dinner", "party",
}

func valenceBound(lo, hi int) *[2]int { return &[2]int{lo, hi} }

// Match is one region a point belongs to.
type Match struct {
	Vibe string
	// Distance from the region's centre; lower is a more central example.
	Distance float64
}

// VibesFor reports which regions a point falls in, nearest first, capped at max.
//
// Membership is a measurement against a defined region, not a prediction about
// it: there is no training set, no holdout, and no accuracy figure to attach to
// a result. It works identically in a library with no playlists and no listening
// history, because neither is consulted.
//
// Tracks legitimately land in several regions - "golden hour" and "dinner"
// overlap by construction - so the result is a ranked list rather than a winner,
// and an empty list is a normal answer rather than a failure.
func VibesFor(p Point, max int) []Match {
	if max <= 0 {
		return nil
	}
	var out []Match
	for _, name := range RegionNames {
		r := Regions[name]
		if v := r.Valence; v != nil && (p.Valence < v[0] || p.Valence > v[1]) {
			continue
		}
		if r.Tempo != nil && !contains(r.Tempo, p.Tempo) {
			continue
		}
		if r.Vocal != nil && !contains(r.Vocal, p.Vocal) {
			continue
		}
		d := Distance(p.Axes, r.Centre)
		if d > r.Radius {
			continue
		}
		out = append(out, Match{Vibe: name, Distance: math.Round(d*10) / 10})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Distance < out[j].Distance })
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// FringeFactor widens a radius for a second-choice match. Measured on a 9,195
// track library: of the 3,230 tracks in no region at all, the median sat 2.2
// beyond the nearest boundary, against a median distance between any two tracks
// of 17.7. They are not unusual music between the regions, they are ordinary
// music just outside an edge. 1.5 takes coverage from 65% to 94%; 2.0 reaches
// 100%, which is the point at which membership stops carrying information.
const FringeFactor = 1.5

// NearestFor reports the closest region a point would join if that region's
// radius were widened by FringeFactor, and whether one was found.
//
// The hard constraints still apply. A vocal track does not become a fringe
// member of `focus` by being close to its centre, because the constraint is
// what the region means rather than how large it is.
//
// This is deliberately separate from VibesFor and written to its own tag. A
// caller that treats a fringe match as membership has thrown away the only
// thing that distinguishes "this is a late night track" from "this is the
// closest thing to one that the library has".
func NearestFor(p Point) (Match, bool) {
	best, found := Match{}, false
	for _, name := range RegionNames {
		r := Regions[name]
		if v := r.Valence; v != nil && (p.Valence < v[0] || p.Valence > v[1]) {
			continue
		}
		if r.Tempo != nil && !contains(r.Tempo, p.Tempo) {
			continue
		}
		if r.Vocal != nil && !contains(r.Vocal, p.Vocal) {
			continue
		}
		d := Distance(p.Axes, r.Centre)
		if d > r.Radius*FringeFactor {
			continue
		}
		if !found || d < best.Distance {
			best, found = Match{Vibe: name, Distance: math.Round(d*10) / 10}, true
		}
	}
	return best, found
}
