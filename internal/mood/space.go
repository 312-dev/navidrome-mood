package mood

import "math"

// Axes are the five continuous dimensions, each 0-100. They are split out from
// Point because the things a region is defined by - a term's anchor, a vibe's
// centre - have coordinates but no tempo, vocal or descriptors of their own.
type Axes struct {
	// Energy runs still -> frantic.
	Energy int
	// Valence runs bleak -> joyful.
	Valence int
	// Intensity runs gentle -> heavy.
	Intensity int
	// Acousticness runs electronic -> acoustic.
	Acousticness int
	// Density runs sparse/solo -> wall of sound.
	Density int
}

// TempoFeel is the perceived pace, which is not the same as BPM: a half-time
// doom riff at 140 BPM feels still.
type TempoFeel string

// VocalKind is how the voice sits in the track, or that there isn't one.
type VocalKind string

// TempoFeels and VocalKinds are the closed sets written to ndmood_tempo and
// ndmood_vocal. The connector reads both all-or-nothing with the five axes, so a
// value outside these makes the whole track count as unlabelled.
var (
	TempoFeels = []TempoFeel{"still", "slow", "mid", "driving", "frantic"}
	VocalKinds = []VocalKind{"instrumental", "sung", "rapped", "mixed"}
)

// Point is a track's full position: the axes plus the two categoricals.
type Point struct {
	Axes
	Tempo TempoFeel
	Vocal VocalKind
}

// Axis weights for Distance. They are not uniform, and the two interesting ones
// are:
//
//   - Valence is CHEAP. A sad song and a happy song sit together fine if they
//     sound alike; a melancholy folk tune next to a joyful folk tune is a good
//     transition. Weighting valence heavily makes a playlist emotionally
//     monotonous, which is a different failure from the one being fixed.
//   - Intensity and acousticness are EXPENSIVE, because together they are the
//     Debussy/Metallica axis. Both tracks are legitimately "tender": Debussy's
//     Suite bergamasque sits at intensity 8, acousticness 95 and Metallica's
//     Nothing Else Matters at intensity 55, acousticness 60. That is the actual
//     observed failure, and it is what the weights are tuned to catch.
const (
	weightIntensity    = 1.6
	weightAcousticness = 1.4
	weightDensity      = 1.0
	weightEnergy       = 1.0
	weightValence      = 0.45

	weightTotal = weightIntensity + weightAcousticness + weightDensity + weightEnergy + weightValence
)

// Distance is the weighted Euclidean distance over the five axes, on roughly a
// 0-100 scale.
//
// This is the measure a REGION is defined against: a term's anchor and a vibe's
// centre are coordinates, so "is this track in that region" cannot ask about
// tempo or vocal, which the region does not have. Those are handled separately,
// as the hard membership constraints on Region.
func Distance(a, b Axes) float64 {
	sq := weightIntensity*sqDiff(a.Intensity, b.Intensity) +
		weightAcousticness*sqDiff(a.Acousticness, b.Acousticness) +
		weightDensity*sqDiff(a.Density, b.Density) +
		weightEnergy*sqDiff(a.Energy, b.Energy) +
		weightValence*sqDiff(a.Valence, b.Valence)
	return math.Sqrt(sq / weightTotal)
}

func sqDiff(a, b int) float64 {
	d := float64(a - b)
	return d * d
}
