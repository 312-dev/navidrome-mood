package llm

import (
	"errors"
	"fmt"
	"testing"
)

// Every retry is another billable request. Retrying a permanent failure across a
// whole run multiplies the bill by MaxRetries for an identical outcome.
func TestRetryableOnlyForFailuresThatCouldChange(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"bad api key", &APIError{Status: 401, Provider: "p"}, false},
		{"forbidden", &APIError{Status: 403, Provider: "p"}, false},
		{"budget exhausted", ErrBudgetExhausted, false},
		{"unknown model", ErrUnknownModel{Model: "x"}, false},
		{"bad request", &APIError{Status: 400, Provider: "p"}, false},
		{"host truncated at 10MB", ErrTruncated, false},
		{"unrecognised", errors.New("something odd"), false},

		{"rate limited", ErrRateLimited, true},
		{"429 from provider", &APIError{Status: 429, Provider: "p"}, true},
		{"provider 500", &APIError{Status: 500, Provider: "p"}, true},
		{"provider 503", &APIError{Status: 503, Provider: "p"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Retryable(c.err); got != c.want {
				t.Fatalf("Retryable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// The default must be "do not retry": the cost of wrongly retrying is money, the
// cost of wrongly stopping is a message asking the user to try again.
func TestUnrecognisedErrorsDefaultToNoRetry(t *testing.T) {
	for _, err := range []error{
		errors.New("weird"),
		fmt.Errorf("wrapped: %w", errors.New("weird")),
	} {
		if Retryable(err) {
			t.Fatalf("unrecognised error %v was treated as retryable", err)
		}
	}
}

// The per-run limit legitimately resets when the model or limit changes. Without
// a figure that never resets, someone could spend without bound by nudging the
// model dropdown between runs.
func TestLifetimeCapSurvivesModelAndLimitChanges(t *testing.T) {
	b, err := NewBudget("claude-sonnet-5", 5)
	if err != nil {
		t.Fatal(err)
	}
	b.SetLifetime(0, 10) // $10 ceiling

	if err := b.Record(Usage{Output: 400_000}, false); err != nil { // $6 at $15/Mtok
		t.Fatal(err)
	}
	if b.Lifetime() < 5.9 || b.Lifetime() > 6.1 {
		t.Fatalf("lifetime = %.4f, want ~6", b.Lifetime())
	}

	// Simulate the escape route: a brand new budget for a different model and a
	// fresh per-run limit, carrying the lifetime figure across.
	b2, err := NewBudget("claude-haiku-4-5", 5)
	if err != nil {
		t.Fatal(err)
	}
	b2.SetLifetime(b.Lifetime(), 10)

	// Per-run spend is zero, so the per-run limit would happily allow this...
	if err := b2.CanAfford(0.5); err != nil {
		t.Fatalf("small batch under both caps was refused: %v", err)
	}
	// ...but anything that would cross the lifetime ceiling must be refused even
	// though this budget has spent nothing.
	if err := b2.CanAfford(5); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("lifetime ceiling was escaped by changing model: %v", err)
	}
}

func TestLifetimeSurvivesASnapshotRoundTrip(t *testing.T) {
	b, _ := NewBudget("claude-sonnet-5", 5)
	b.SetLifetime(3.25, 10)
	if err := b.Record(Usage{Output: 100_000}, false); err != nil {
		t.Fatal(err)
	}
	want := b.Lifetime()

	restored, err := Restore(b.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.Lifetime(); got != want {
		t.Fatalf("lifetime lost in round trip: %.6f, want %.6f", got, want)
	}
}

// A zero lifetime cap means "not configured" and must not silently disable the
// per-run limit as well.
func TestZeroLifetimeCapStillEnforcesTheRunLimit(t *testing.T) {
	b, _ := NewBudget("claude-sonnet-5", 1)
	b.SetLifetime(999, 0)
	if err := b.CanAfford(5); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("run limit not enforced when lifetime cap is unset: %v", err)
	}
}

// A reply that arrived intact but did not fit the schema is worth another go.
//
// Observed on a live run: one batch in roughly 25 returned its labels array as a
// JSON string rather than an array. The request was well-formed, so the same
// request has a real chance of succeeding, and treating it as permanent means
// paying for the batch and getting nothing back.
func TestAMalformedReplyIsWorthRetrying(t *testing.T) {
	err := fmt.Errorf("%w: could not parse tool input: %v", ErrMalformedReply,
		"json: cannot unmarshal string into Go struct field .labels")
	if !Retryable(err) {
		t.Error("a schema mismatch is not retryable; a paid batch is thrown away")
	}
	// The things it must not be confused with. Each of these repeats identically
	// however many times it is sent, so a retry is pure cost.
	for name, permanent := range map[string]error{
		"auth":      ErrAuth,
		"budget":    ErrBudgetExhausted,
		"truncated": ErrTruncated,
	} {
		if Retryable(permanent) {
			t.Errorf("%s is retryable; retrying cannot change the outcome", name)
		}
	}
}
