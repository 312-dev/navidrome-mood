package main

import (
	"fmt"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"

	"github.com/312-dev/navidrome-mood/internal/llm"
)

// Spending guards.
//
// This plugin runs on other people's servers with other people's API keys, and
// the failure everyone actually fears is not a bad label - it is a bill. Every
// guard below exists because of a specific way money could be spent without
// producing anything, and each one fails CLOSED: when a guard cannot do its job,
// it stops the run rather than letting work continue unmeasured.
const (
	// keyStrikes counts consecutive batches that cost money and produced no
	// labels. Nonzero cost with zero output means something is systematically
	// wrong - a batch size the model cannot answer within max_tokens, a prompt it
	// rejects - and repeating it just buys the same nothing 460 more times.
	keyStrikes = "run:strikes"
	maxStrikes = 3

	// keyHalted is set when a guard stops a run. It blocks new work until the
	// user clears it by changing configuration, so a runaway cannot resume itself
	// on the next 15-minute sync.
	keyHalted = "run:halted"

	// defaultLifetimeCap bounds total spend across ALL runs and configurations.
	// The per-run limit legitimately resets when the model or limit changes, so
	// without this someone could spend without bound by nudging a dropdown
	// between runs.
	defaultLifetimeCap = 100.0
)

// halt stops everything and records why.
//
// Clearing the queue is the point: leaving hundreds of durable tasks behind
// would mean the run resumes the moment anything reloads the plugin.
func halt(reason string) {
	logf(pdk.LogError, "HALTED: %s", reason)
	if n, err := host.TaskClearQueue(queueLabel); err != nil {
		logf(pdk.LogError, "could not clear the queue - work may resume: %v", err)
	} else if n > 0 {
		logf(pdk.LogWarn, "discarded %d queued batches", n)
	}
	_ = host.KVStoreSet(keyHalted, []byte(reason))
	_ = writeInt(keyPending, 0)
}

// haltedReason returns why work is stopped, or "".
func haltedReason() string {
	raw, ok, err := host.KVStoreGet(keyHalted)
	if err != nil || !ok {
		return ""
	}
	return string(raw)
}

// clearHalt lets work start again. Called when the user changes configuration,
// which is the closest thing to an explicit "I have looked at this" signal
// available without a button.
func clearHalt() {
	if haltedReason() != "" {
		logf(pdk.LogInfo, "clearing previous halt; configuration changed")
		_ = host.KVStoreDelete(keyHalted)
		_ = host.KVStoreSet(keyStrikes, []byte("0"))
	}
}

// recordOutcome updates the circuit breaker after a batch.
func recordOutcome(spent float64, labelled int) {
	if labelled > 0 {
		_ = host.KVStoreSet(keyStrikes, []byte("0"))
		return
	}
	if spent <= 0 {
		return // nothing bought, nothing to worry about
	}
	n, _ := readInt(keyStrikes)
	n++
	_ = writeInt(keyStrikes, n)
	logf(pdk.LogWarn, "batch spent $%.4f and produced no labels (%d/%d)", spent, n, maxStrikes)
	if n >= maxStrikes {
		halt(fmt.Sprintf(
			"%d consecutive batches cost money and produced no labels. "+
				"Lower 'Tracks per request' or check the provider, then change any "+
				"setting to resume.", n))
	}
}

// persistSpend writes the budget, and stops everything if it cannot.
//
// A cap that is not durable is not a cap. If the spend counter cannot be written,
// the next batch would load a stale figure and under-count - so the honest
// response is to stop rather than to keep spending money we have lost track of.
func persistSpend(b *llm.Budget) {
	if b == nil {
		return
	}
	if err := saveBudget(b); err != nil {
		halt(fmt.Sprintf(
			"could not record spend ($%.4f this run, $%.4f lifetime): %v. "+
				"Stopping, because a spend limit that cannot be saved is not a limit.",
			b.Spent(), b.Lifetime(), err))
	}
}

// preflight reports what a run will cost before it starts, and refuses configs
// that cannot be bounded.
func preflight(files int, model string, limit, lifetimeSpent, lifetimeCap float64) error {
	if limit <= 0 {
		return fmt.Errorf(
			"set a spend limit before running. Leaving it at zero would mean no limit at all")
	}
	low, high, err := llm.Estimate(model, files, int64(len(buildPrompt())/3), 2000, false)
	if err != nil {
		return err
	}

	logf(pdk.LogInfo, "estimate: %d tracks with %s is roughly $%.2f-$%.2f",
		files, model, low, high)
	logf(pdk.LogInfo, "limits: $%.2f this run, $%.2f of $%.2f lifetime used",
		limit, lifetimeSpent, lifetimeCap)

	if low > limit {
		return fmt.Errorf(
			"this run needs about $%.2f but the spend limit is $%.2f. "+
				"Raise the limit, or use sample mode first", low, limit)
	}
	if lifetimeCap > 0 && lifetimeSpent+low > lifetimeCap {
		return fmt.Errorf(
			"this run would pass the $%.2f lifetime ceiling (already spent $%.2f)",
			lifetimeCap, lifetimeSpent)
	}
	if high > limit {
		// Not fatal: the cap will stop it partway, which is exactly what a cap is
		// for. Say so plainly rather than letting it look like a failure later.
		logf(pdk.LogWarn,
			"the high estimate ($%.2f) exceeds the limit ($%.2f), so this run will "+
				"probably stop partway. That is the limit working, not a fault",
			high, limit)
	}
	return nil
}

// describeHalt turns a halt reason into something a user can act on.
func describeHalt(reason string) string {
	if strings.Contains(reason, "no labels") {
		return reason + " (try 'Tracks per request' = 10)"
	}
	return reason
}
