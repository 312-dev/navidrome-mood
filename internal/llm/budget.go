package llm

import (
	"errors"
	"fmt"
	"sync"
)

// ErrBudgetExhausted is returned when the spend cap has been reached. It is a
// normal outcome, not a failure: the pass stops, state is already durable in
// kvstore, and raising the cap resumes exactly where it left off.
var ErrBudgetExhausted = errors.New("llm: spend limit reached")

// Budget enforces the user's maxSpendUsd as a guarantee rather than a prediction.
//
// # The one-batch overshoot, stated honestly
//
// A request's true cost is unknowable until the provider reports usage, so the
// cap is checked BEFORE each request using a conservative projection and then
// reconciled against actual usage afterwards. Worst case the final batch carries
// spend slightly past the limit. Keeping batches small bounds that overshoot to
// cents; claiming the cap is exact would be a lie.
//
// # Failing closed
//
// If the model's price is unknown, every operation errors rather than proceeding
// with a counter stuck at zero. An unpriced model would otherwise mean unlimited
// spend behind a limit the user believes is enforced - the worst possible
// failure for this particular feature.
type Budget struct {
	mu       sync.Mutex
	model    string
	limitUSD float64
	spent    float64
	usage    Usage
	batches  int
}

// NewBudget fails immediately on an unpriceable model, so the error surfaces at
// configuration time rather than after money has been spent.
func NewBudget(model string, limitUSD float64) (*Budget, error) {
	if limitUSD < 0 {
		return nil, fmt.Errorf("llm: spend limit %.2f is negative", limitUSD)
	}
	if _, ok := Prices[model]; !ok {
		return nil, ErrUnknownModel{Model: model}
	}
	return &Budget{model: model, limitUSD: limitUSD}, nil
}

// CanAfford reports whether a request projected to cost projectedUSD may proceed.
// A zero limit means unlimited, which is why it is not the default in the
// manifest.
func (b *Budget) CanAfford(projectedUSD float64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limitUSD == 0 {
		return nil
	}
	if b.spent >= b.limitUSD {
		return fmt.Errorf("%w: spent $%.4f of $%.2f", ErrBudgetExhausted, b.spent, b.limitUSD)
	}
	if b.spent+projectedUSD > b.limitUSD {
		return fmt.Errorf("%w: $%.4f spent, next batch projected at $%.4f, limit $%.2f",
			ErrBudgetExhausted, b.spent, projectedUSD, b.limitUSD)
	}
	return nil
}

// Record adds real reported usage. Always call it with what the provider actually
// returned, never with an estimate - the whole point is that the cap tracks money
// rather than guesses.
func (b *Budget) Record(u Usage, discounted bool) error {
	cost, err := Cost(b.model, u, discounted)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent += cost
	b.usage = b.usage.Add(u)
	b.batches++
	return nil
}

// Spent returns USD spent so far, from reported usage only.
func (b *Budget) Spent() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

// Remaining returns USD left, or -1 when the limit is unlimited.
func (b *Budget) Remaining() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limitUSD == 0 {
		return -1
	}
	if r := b.limitUSD - b.spent; r > 0 {
		return r
	}
	return 0
}

// Snapshot is the durable form written to kvstore, so a restart resumes with the
// spend counter intact instead of silently starting from zero.
type Snapshot struct {
	Model    string  `json:"model"`
	LimitUSD float64 `json:"limitUsd"`
	SpentUSD float64 `json:"spentUsd"`
	Usage    Usage   `json:"usage"`
	Batches  int     `json:"batches"`
}

func (b *Budget) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Snapshot{
		Model: b.model, LimitUSD: b.limitUSD,
		SpentUSD: b.spent, Usage: b.usage, Batches: b.batches,
	}
}

// Restore rebuilds a Budget from a snapshot. The model is re-priced on the way
// in, so a snapshot naming a model whose price is no longer known fails closed
// exactly as a fresh Budget would.
func Restore(s Snapshot) (*Budget, error) {
	b, err := NewBudget(s.Model, s.LimitUSD)
	if err != nil {
		return nil, err
	}
	b.spent, b.usage, b.batches = s.SpentUSD, s.Usage, s.Batches
	return b, nil
}
