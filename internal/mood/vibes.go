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
	// Calibrated so each region covers about 7% of the five-axis space, which
	// puts a typical point in about one region and leaves most of the space in
	// none. The hard constraints below cut a constrained region's real share
	// further. Regions are shortcuts, not a partition: a track in no region is
	// still reachable by axis ranges and vocabulary terms. The first guesses were
	// much looser and had to be measured back down; `driving` covered 46% of the
	// space before calibration.
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
	"wind down":    {Centre: Axes{Energy: 20, Valence: 52, Intensity: 14, Acousticness: 78, Density: 24}, Radius: 21, Tempo: []TempoFeel{"still", "slow"}, Gloss: "settling toward sleep"},
	"slow morning": {Centre: Axes{Energy: 32, Valence: 62, Intensity: 22, Acousticness: 70, Density: 34}, Radius: 19, Tempo: []TempoFeel{"slow", "mid"}, Gloss: "easing into the day"},
	"focus":        {Centre: Axes{Energy: 42, Valence: 50, Intensity: 28, Acousticness: 40, Density: 40}, Radius: 18, Vocal: []VocalKind{"instrumental"}, Gloss: "steady, undemanding, stays out of the way"},
	"background":   {Centre: Axes{Energy: 38, Valence: 58, Intensity: 25, Acousticness: 55, Density: 38}, Radius: 18, Gloss: "pleasant and unobtrusive"},
	"uplift":       {Centre: Axes{Energy: 68, Valence: 80, Intensity: 42, Acousticness: 45, Density: 58}, Radius: 21, Valence: valenceBound(55, 100), Gloss: "a deliberate lift in mood"},
	"workout":      {Centre: Axes{Energy: 84, Valence: 62, Intensity: 70, Acousticness: 25, Density: 72}, Radius: 20, Tempo: []TempoFeel{"driving", "frantic"}, Gloss: "sustained physical push"},
	"hype":         {Centre: Axes{Energy: 86, Valence: 70, Intensity: 66, Acousticness: 18, Density: 74}, Radius: 24, Valence: valenceBound(50, 100), Gloss: "getting up for something"},
	"driving":      {Centre: Axes{Energy: 70, Valence: 60, Intensity: 58, Acousticness: 40, Density: 62}, Radius: 18, Tempo: []TempoFeel{"mid", "driving"}, Gloss: "motion; miles passing"},
	"golden hour":  {Centre: Axes{Energy: 48, Valence: 68, Intensity: 32, Acousticness: 55, Density: 46}, Radius: 20, Valence: valenceBound(45, 100), Gloss: "warm light, day easing off"},
	"late night":   {Centre: Axes{Energy: 40, Valence: 40, Intensity: 38, Acousticness: 35, Density: 45}, Radius: 18, Gloss: "after hours; low light"},
	"melancholy":   {Centre: Axes{Energy: 28, Valence: 24, Intensity: 24, Acousticness: 65, Density: 32}, Radius: 22, Valence: valenceBound(0, 45), Gloss: "sitting with something sad"},
	"heavy":        {Centre: Axes{Energy: 80, Valence: 32, Intensity: 84, Acousticness: 28, Density: 78}, Radius: 24, Valence: valenceBound(0, 55), Gloss: "loud, dark and physical"},
	"dinner":       {Centre: Axes{Energy: 40, Valence: 66, Intensity: 26, Acousticness: 68, Density: 40}, Radius: 19, Gloss: "convivial but not competing with conversation"},
	"party":        {Centre: Axes{Energy: 80, Valence: 82, Intensity: 50, Acousticness: 20, Density: 72}, Radius: 23, Valence: valenceBound(55, 100), Gloss: "a room full of people"},
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
