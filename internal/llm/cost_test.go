package llm

import (
	"errors"
	"math"
	"testing"
)

func closeTo(t *testing.T, got, want, tol float64, what string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %.6f, want %.6f (tolerance %.6f)", what, got, want, tol)
	}
}

func TestCostPricesEachTokenClassSeparately(t *testing.T) {
	// claude-opus-5 is $5/Mtok in, $25/Mtok out.
	// 1M fresh input                    = $5.00
	// 1M cache writes at 1.25x          = $6.25
	// 1M cache reads   at 0.10x         = $0.50
	// 1M output                         = $25.00
	//                                     ------
	//                                     $36.75
	u := Usage{Input: 1e6, CacheWrite: 1e6, CacheRead: 1e6, Output: 1e6}
	got, err := Cost("claude-opus-5", u, false)
	if err != nil {
		t.Fatal(err)
	}
	closeTo(t, got, 36.75, 1e-9, "cost")
}

// Pricing cache reads as ordinary input is the specific mistake this guards
// against - it is a 10x error on the dominant token class in a full pass.
func TestCacheReadsAreNotPricedAsInput(t *testing.T) {
	asRead, _ := Cost("claude-opus-5", Usage{CacheRead: 1e6}, false)
	asInput, _ := Cost("claude-opus-5", Usage{Input: 1e6}, false)
	if asRead >= asInput {
		t.Fatalf("cache read $%.4f is not cheaper than fresh input $%.4f", asRead, asInput)
	}
	closeTo(t, asInput/asRead, 10, 1e-9, "input/cacheRead ratio")
}

func TestBatchDiscountHalvesCost(t *testing.T) {
	u := Usage{Input: 1e6, Output: 1e6}
	full, _ := Cost("claude-opus-5", u, false)
	half, _ := Cost("claude-opus-5", u, true)
	closeTo(t, half, full/2, 1e-9, "discounted cost")
}

// Failing closed is the whole safety property: an unpriced model must not mean
// unlimited spend behind a limit the user believes is enforced.
func TestUnknownModelFailsClosed(t *testing.T) {
	if _, err := Cost("some-model-shipped-tomorrow", Usage{Input: 1e6}, false); err == nil {
		t.Fatal("unknown model was priced, so the spend cap would not be enforced")
	}
	var unknown ErrUnknownModel
	if _, err := NewBudget("some-model-shipped-tomorrow", 10); !errors.As(err, &unknown) {
		t.Fatalf("NewBudget accepted an unpriceable model: %v", err)
	}
}

// The calibration that made the real run land within 0.3%.
func TestEstimateUsesTheMeasuredOutputConstant(t *testing.T) {
	if OutputTokensPerTrack != 96 {
		t.Fatalf("OutputTokensPerTrack = %d; 96 is the measured value from a 9,311-track "+
			"run. Changing it needs new measurement, not a guess.", OutputTokensPerTrack)
	}
	low, high, err := Estimate("claude-opus-5", 100, 50_000, 2_000, false)
	if err != nil {
		t.Fatal(err)
	}
	if low >= high {
		t.Fatalf("estimate range is inverted: %.4f..%.4f", low, high)
	}
	if low <= 0 {
		t.Fatalf("estimate low bound is %.4f, want positive", low)
	}
}

func TestBudgetStopsAtTheLimit(t *testing.T) {
	b, err := NewBudget("claude-opus-5", 1.00)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.CanAfford(0.50); err != nil {
		t.Fatalf("refused an affordable batch: %v", err)
	}

	// 0.1M output at $25/Mtok = $2.50, blowing past the $1.00 limit.
	if err := b.Record(Usage{Output: 100_000}, false); err != nil {
		t.Fatal(err)
	}
	closeTo(t, b.Spent(), 2.50, 1e-9, "spent")

	if err := b.CanAfford(0.01); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("budget allowed spending past the limit: %v", err)
	}
	if b.Remaining() != 0 {
		t.Fatalf("Remaining() = %.4f, want 0", b.Remaining())
	}
}

func TestBudgetRefusesABatchThatWouldExceed(t *testing.T) {
	b, _ := NewBudget("claude-opus-5", 1.00)
	if err := b.CanAfford(1.50); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("allowed a batch projected to exceed the limit: %v", err)
	}
	// ...but the budget is untouched, so a smaller batch still proceeds.
	if err := b.CanAfford(0.10); err != nil {
		t.Fatalf("a smaller batch was refused after a rejection: %v", err)
	}
}

func TestZeroLimitMeansUnlimited(t *testing.T) {
	b, _ := NewBudget("claude-opus-5", 0)
	if err := b.Record(Usage{Output: 10_000_000}, false); err != nil {
		t.Fatal(err)
	}
	if err := b.CanAfford(1000); err != nil {
		t.Fatalf("zero limit should be unlimited: %v", err)
	}
	if b.Remaining() != -1 {
		t.Fatalf("Remaining() = %.2f, want -1 for unlimited", b.Remaining())
	}
}

// A restart must not reset the spend counter to zero, or the cap is meaningless
// for exactly the long-running pass it exists to bound.
func TestSnapshotRoundTripPreservesSpend(t *testing.T) {
	b, _ := NewBudget("claude-sonnet-5", 5.00)
	if err := b.Record(Usage{Input: 1e6, Output: 200_000}, true); err != nil {
		t.Fatal(err)
	}
	want := b.Spent()

	restored, err := Restore(b.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	closeTo(t, restored.Spent(), want, 1e-12, "restored spend")
	if restored.Remaining() != b.Remaining() {
		t.Fatalf("restored remaining %.6f != %.6f", restored.Remaining(), b.Remaining())
	}
}

func TestRestoreOfAnUnpriceableModelFailsClosed(t *testing.T) {
	_, err := Restore(Snapshot{Model: "retired-model", LimitUSD: 5, SpentUSD: 1})
	if err == nil {
		t.Fatal("restored a budget for an unpriceable model; spend would stop being counted")
	}
}

func TestNegativeLimitRejected(t *testing.T) {
	if _, err := NewBudget("claude-opus-5", -1); err == nil {
		t.Fatal("accepted a negative spend limit")
	}
}
