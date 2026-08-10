package mood

import (
	"math"
	"math/rand"
	"testing"
)

// The radii are calibrated against a library, so they have to be tested against
// one. TestNoRegionSwallowsMoodSpace sweeps a uniform grid, and a uniform grid
// is exactly the assumption that produced the bug this file exists to catch:
// every region passed that test at 15% while `driving` was tagging 45% of a real
// collection and `focus` was reaching 111 tracks out of 9,195.
//
// Real music does not fill the cube. Measured over 9,195 labelled tracks, two
// tracks picked at random sit 17.7 apart at the median where two uniform points
// sit 39.1 apart, so a radius of 20 encloses more than half of everything.
//
// synthLibrary draws from that measured shape rather than from the cube. The
// moments below are the real ones; no track from the library that produced them
// is reproduced here, and none needs to be, because it is the SHAPE the radii
// are fitted to and the shape is what these five numbers and one matrix carry.
const synthSize = 4000

// Per-axis mean and standard deviation, in the order energy, valence,
// intensity, acousticness, density.
var (
	synthMean = [5]float64{54.8, 52.7, 43.3, 35.8, 53.1}
	synthSD   = [5]float64{15.3, 14.7, 14.4, 20.7, 12.5}

	// Lower Cholesky factor of the measured correlation matrix. The correlations
	// matter more than the spread: energy, intensity and density correlate at
	// 0.85 to 0.93 and all three run against acousticness at about -0.75, so four
	// of the five axes are largely one underlying loud-to-quiet factor and the
	// library occupies a diagonal ribbon rather than a cube. Drawing the axes
	// independently would scatter points into corners no recording occupies and
	// make every region look far more selective than it is.
	synthChol = [5][5]float64{
		{1.0000, 0.0000, 0.0000, 0.0000, 0.0000},
		{0.1413, 0.9900, 0.0000, 0.0000, 0.0000},
		{0.8504, -0.4583, 0.2584, 0.0000, 0.0000},
		{-0.7743, 0.1022, 0.1472, 0.6069, 0.0000},
		{0.9319, -0.0764, 0.1413, -0.1387, 0.2941},
	}
)

// synthLibrary returns a library-shaped sample of points.
func synthLibrary(n int) []Point {
	rng := rand.New(rand.NewSource(1))
	out := make([]Point, 0, n)
	for len(out) < n {
		var z, x [5]float64
		for i := range z {
			z[i] = rng.NormFloat64()
		}
		for i := range x {
			var s float64
			for j := 0; j <= i; j++ {
				s += synthChol[i][j] * z[j]
			}
			x[i] = synthMean[i] + synthSD[i]*s
		}
		axes := Axes{
			Energy:       clamp100(x[0]),
			Valence:      clamp100(x[1]),
			Intensity:    clamp100(x[2]),
			Acousticness: clamp100(x[3]),
			Density:      clamp100(x[4]),
		}
		out = append(out, Point{
			Axes:  axes,
			Tempo: synthTempo(axes.Energy, rng),
			Vocal: synthVocal(axes.Acousticness, rng),
		})
	}
	return out
}

// synthTempo derives the tempo feel from energy, because in the real data it
// very nearly is one: 92% of tracks between energy 40 and 59 are `mid`, 83%
// between 60 and 79 are `driving`, and 83% between 20 and 39 are `slow`.
func synthTempo(energy int, rng *rand.Rand) TempoFeel {
	switch {
	case energy < 20:
		if rng.Float64() < 0.24 {
			return "still"
		}
		return "slow"
	case energy < 40:
		if rng.Float64() < 0.17 {
			return "mid"
		}
		return "slow"
	case energy < 60:
		if rng.Float64() < 0.08 {
			return "driving"
		}
		return "mid"
	case energy < 80:
		if rng.Float64() < 0.17 {
			return "mid"
		}
		return "driving"
	default:
		if rng.Float64() < 0.24 {
			return "frantic"
		}
		return "driving"
	}
}

// synthVocal draws the vocal kind conditioned on acousticness. The measured
// marginal is 77% sung, 10% mixed, 9% rapped and 4% instrumental, and the
// conditioning matters for one region: `focus` admits instrumental only, so how
// scarce instrumental music is decides whether that region can exist at all.
func synthVocal(acousticness int, rng *rand.Rand) VocalKind {
	r := rng.Float64()
	switch {
	case acousticness < 25:
		switch {
		case r < 0.54:
			return "sung"
		case r < 0.75:
			return "rapped"
		case r < 0.94:
			return "mixed"
		default:
			return "instrumental"
		}
	case acousticness < 50:
		switch {
		case r < 0.84:
			return "sung"
		case r < 0.92:
			return "mixed"
		case r < 0.98:
			return "rapped"
		default:
			return "instrumental"
		}
	default:
		switch {
		case r < 0.92:
			return "sung"
		case r < 0.97:
			return "instrumental"
		default:
			return "mixed"
		}
	}
}

func clamp100(v float64) int {
	n := int(math.Round(v))
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

// TestEveryRegionIsUsableOnALibrary is the test the uniform sweep could not be.
//
// Both bounds are failures a user would meet:
//
//   - Over the ceiling, the tag has stopped selecting. `driving` reached 45% of
//     a real library, which makes "give me something for driving" and "give me
//     anything" the same query.
//   - Under the floor, there is nothing to build a playlist from. `focus` reached
//     111 tracks out of 9,195, and once repeats from recent runs are excluded a
//     pool that small returns the same handful every time.
//
// The band is wide on purpose. This is a synthetic library, no real collection
// matches it exactly, and the point is to catch a radius that has moved by a
// factor rather than to pin one to a decimal.
func TestEveryRegionIsUsableOnALibrary(t *testing.T) {
	const (
		floor   = 0.015
		ceiling = 0.20
	)
	lib := synthLibrary(synthSize)

	tagged := make(map[string]int, len(RegionNames))
	var homeless int
	for _, p := range lib {
		// The cap is what a track actually carries, so it is what a query
		// actually matches. Measuring raw membership would report a region as
		// healthy while every one of its members lists three nearer ones.
		got := VibesFor(p, 3)
		if len(got) == 0 {
			homeless++
		}
		for _, m := range got {
			tagged[m.Vibe]++
		}
	}

	for _, name := range RegionNames {
		share := float64(tagged[name]) / float64(len(lib))
		switch {
		case share > ceiling:
			t.Errorf("region %q tags %.1f%% of a library, over the %.0f%% ceiling: "+
				"a vibe that common has stopped narrowing anything",
				name, share*100, ceiling*100)
		case share < floor:
			t.Errorf("region %q tags %.1f%% of a library, under the %.1f%% floor: "+
				"too few tracks to build a playlist from",
				name, share*100, floor*100)
		}
		t.Logf("%-13s %5.1f%%  (%d tracks of %d)", name, share*100, tagged[name], len(lib))
	}

	// Belonging to no region is normal, and the radii are deliberately tight
	// enough that a good share of any library does. All of it would mean the
	// regions have collapsed to points.
	if got := float64(homeless) / float64(len(lib)); got > 0.65 {
		t.Errorf("%.0f%% of the library is in no region at all, which leaves the "+
			"vibe tag describing a minority of the collection", got*100)
	}
}
