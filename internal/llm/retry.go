package llm

import "errors"

// Retryable reports whether re-sending a failed request could plausibly succeed.
//
// This exists to stop the task queue paying for the same failure repeatedly. A
// queue with MaxRetries=3 will re-run a failed task three times, and every one of
// those attempts is a fresh billable API call. For a transient 429 that is
// correct. For a bad API key, an exhausted budget, or a batch too large for the
// model's output limit, it is three times the cost for a guaranteed-identical
// failure - multiplied across every batch in the run.
//
// The default is deliberately NOT to retry. An unrecognised error is treated as
// permanent, because the cost of wrongly retrying is money and the cost of
// wrongly stopping is a message asking the user to try again.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	switch {
	// Never retry: the outcome cannot change without the user acting.
	case errors.Is(err, ErrAuth):
		return false
	case errors.Is(err, ErrBudgetExhausted):
		return false
	case errors.Is(err, ErrUnknownModelSentinel):
		return false

	// Worth retrying: the provider asked us to back off, or the network blipped.
	case errors.Is(err, ErrRateLimited):
		return true
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		// 5xx is the provider's problem and usually transient.
		// 4xx is ours: a malformed request will be malformed again.
		return apiErr.Status >= 500
	}

	// Transport-level failures (connection reset, timeout) surface as plain
	// errors from the host and are worth one more go.
	if errors.Is(err, ErrTruncated) {
		// A 10 MB response will be 10 MB again. Retrying just re-buys it.
		return false
	}
	return false
}

// ErrUnknownModelSentinel allows errors.Is checks against the typed
// ErrUnknownModel without callers having to construct one.
var ErrUnknownModelSentinel = errors.New("llm: unknown model")

func (e ErrUnknownModel) Is(target error) bool { return target == ErrUnknownModelSentinel }
