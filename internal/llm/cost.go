// Package llm talks to LLM providers over Navidrome's http_send host service.
//
// No off-the-shelf SDK can be used here. Extism blocks net/http and routes every
// request through the host, which every provider SDK assumes away. So these are
// hand-rolled adapters - but only two are needed, because a configurable base URL
// turns the OpenAI-compatible one into "most of the ecosystem".
package llm

import "fmt"

// Usage is token accounting for one request. Cached input is billed differently
// from fresh input, which matters here: the taxonomy prefix is identical across
// every batch, so cache reads dominate a full-library pass.
type Usage struct {
	Input      int64
	CacheWrite int64
	CacheRead  int64
	Output     int64
}

func (u Usage) Add(o Usage) Usage {
	return Usage{
		Input:      u.Input + o.Input,
		CacheWrite: u.CacheWrite + o.CacheWrite,
		CacheRead:  u.CacheRead + o.CacheRead,
		Output:     u.Output + o.Output,
	}
}

// Price is USD per million tokens.
type Price struct {
	In  float64
	Out float64
}

// Prices covers the models this plugin defaults to or is likely pointed at.
// Anything absent falls back to Unknown, which makes the spend cap fail closed
// rather than silently stop counting - see Cost.
//
// These are list prices and they change. They are only used for the pre-flight
// estimate and the spend cap; actual spend is always computed from the usage the
// provider reports, never guessed.
var Prices = map[string]Price{
	"claude-opus-5":      {In: 5, Out: 25},
	"claude-sonnet-5":    {In: 3, Out: 15},
	"claude-haiku-4-5":   {In: 1, Out: 5},
	"gpt-4o":             {In: 2.5, Out: 10},
	"gpt-4o-mini":        {In: 0.15, Out: 0.6},
	"gpt-4.1":            {In: 2, Out: 8},
	"gpt-4.1-mini":       {In: 0.4, Out: 1.6},
	"deepseek-chat":      {In: 0.27, Out: 1.1},
	"llama-3.3-70b":      {In: 0.59, Out: 0.79},
	"qwen2.5-72b":        {In: 0.35, Out: 0.4},
	"mixtral-8x7b-32768": {In: 0.24, Out: 0.24},
}

// Anthropic's cache multipliers: a write costs 1.25x the input rate, a read 0.1x.
// The taxonomy prefix is identical across batches, so most of a full pass reads
// the cache - which is where roughly a fifth of the total cost goes missing if
// you price cache reads as ordinary input.
const (
	cacheWriteMultiplier = 1.25
	cacheReadMultiplier  = 0.10
)

// ErrUnknownModel is returned when no price is known. Callers must treat this as
// fatal for spend-capped work: continuing would mean spending real money against
// a counter stuck at zero.
type ErrUnknownModel struct{ Model string }

func (e ErrUnknownModel) Error() string {
	return fmt.Sprintf("llm: no price known for model %q, so the spend cap cannot be enforced", e.Model)
}

// Cost returns USD for the given usage.
//
// discounted applies the provider's batch discount (50% on both Anthropic and
// OpenAI). It is a parameter rather than inferred from the model because the same
// model costs different amounts depending on how it was submitted, and getting
// that backwards would silently double or halve every projection.
func Cost(model string, u Usage, discounted bool) (float64, error) {
	p, ok := Prices[model]
	if !ok {
		return 0, ErrUnknownModel{Model: model}
	}
	const perMillion = 1_000_000.0

	in := float64(u.Input)*p.In +
		float64(u.CacheWrite)*p.In*cacheWriteMultiplier +
		float64(u.CacheRead)*p.In*cacheReadMultiplier
	out := float64(u.Output) * p.Out

	total := (in + out) / perMillion
	if discounted {
		total /= 2
	}
	return total, nil
}

// OutputTokensPerTrack is the calibration constant for pre-flight estimates.
//
// Measured, not guessed: 96 tokens per record over a real 9,311-track run. An
// earlier estimate of 65 produced a projection roughly 30% low. With 96 the
// projection landed at $27.30 against $27.22 actual, inside 0.3%.
//
// Re-derive this from recorded usage as runs complete rather than trusting it
// forever - it will drift with the prompt and with the model.
const OutputTokensPerTrack = 96

// Estimate projects the cost of labelling n tracks.
//
// The input side is exactly computable because the prompt is deterministic, so
// the caller passes real token counts. Only the output side is projected, which
// is why the return is a range rather than a single figure: presenting one number
// to two decimal places would imply a precision that does not exist.
func Estimate(model string, n int, promptTokens, cachedPrefixTokens int64, discounted bool) (low, high float64, err error) {
	u := Usage{
		Input:      promptTokens,
		CacheWrite: cachedPrefixTokens,
		CacheRead:  cachedPrefixTokens * int64(max(0, n-1)),
		Output:     int64(n) * OutputTokensPerTrack,
	}
	mid, err := Cost(model, u, discounted)
	if err != nil {
		return 0, 0, err
	}
	// +/-20% covers the observed spread in output length between a track the
	// model finds easy to describe and one it does not.
	return mid * 0.8, mid * 1.2, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
