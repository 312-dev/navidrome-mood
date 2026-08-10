package main

import (
	"fmt"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"

	"github.com/312-dev/navidrome-mood/internal/mood"
)

// Queue names. The taskqueue host service is SQLite-backed per plugin and
// recovers tasks stuck in `running` after a restart, so these survive a Navidrome
// restart mid-pass.
const (
	queueLabel = "label"

	// queueRevibe recomputes the `vibe` tag from axes already in the files. Its
	// own queue rather than a flag on queueLabel, because the two differ in the
	// thing that matters about a queue: a label task costs money and calls a
	// provider, a revibe task does neither. Separate queues mean a retry, a
	// concurrency setting or a stuck task on one cannot be mistaken for the other.
	queueRevibe = "revibe"

	// queueMigrate rewrites tags under their current names after a rename. Its
	// own queue for the same reason as queueRevibe: it calls no provider and
	// spends nothing, so it must not share a retry or concurrency policy with
	// work that does.
	queueMigrate = "migrate"

	// scheduleSync keeps newly added music labelled. Fixed ID so re-registering
	// on each load replaces the schedule rather than accumulating copies.
	scheduleSync = "sync-new"

	// Every 15 minutes, which is as close to "on ingest" as a plugin can get:
	// Navidrome exposes no scan-completed hook, so polling is the only mechanism.
	// Affordable because syncNew costs one directory walk when nothing has
	// changed - it opens no file whose mtime is older than the last check.
	defaultSyncCron = "*/15 * * * *"
)

// OnInit runs once when the plugin is loaded - notably NOT on hot reload, so it
// must not be the only place invariants are checked.
func (p *plugin) OnInit() error {
	// The vocabulary gate. A term containing , ; or / would be split by Navidrome
	// into multiple moods and fragment that mood across the whole library while
	// still looking like it worked. Refuse to load rather than write bad tags.
	if err := mood.Validate(); err != nil {
		logf(pdk.LogError, "navidrome-mood: %v", err)
		return err
	}
	logf(pdk.LogInfo, "navidrome-mood ready: %d mood terms, %d synonyms",
		len(mood.Vocabulary), len(mood.Synonyms))

	// Before anything writes, because everything below competes for the same 1MB
	// allowance as the entries this gives back.
	reclaimPerTrackKeys()

	// A library full of mood words and nothing else looks labelled and is not.
	warnAboutPartialLabels()

	// The self-test is diagnostic and opt-in, run here rather than on a queue so
	// its output lands next to the load line where anyone debugging will look.
	// Failure is logged but never fatal: a plugin that refuses to load because a
	// diagnostic failed is worse than one that reports the diagnostic failing.
	switch mode, _ := host.ConfigGet("selfTest"); mode {
	case "read":
		if err := runSelfTest(false); err != nil {
			logf(pdk.LogError, "selftest: FAILED: %v", err)
		}
	case "write":
		if err := runSelfTest(true); err != nil {
			logf(pdk.LogError, "selftest: FAILED: %v", err)
		}
	}

	// A configuration change is the closest thing to "I have looked at this"
	// available without a button, so it is what releases a halt.
	clearHalt()

	// The queue must exist on every load, independent of whether a run starts.
	// See ensureQueue: skipping it strands durable tasks with no worker.
	if err := ensureQueue(); err != nil {
		logf(pdk.LogError, "could not register the task queue: %v", err)
	}

	// Keep newly added music labelled without anyone touching a setting. The
	// schedule is idempotent: it enumerates, skips anything already tagged, and
	// the pending guard stops it piling work on top of an in-flight run.
	if err := configureAutoSync(); err != nil {
		logf(pdk.LogWarn, "auto-sync: %v", err)
	}

	// Starting a run from OnInit works because Navidrome reloads the plugin on
	// every config save, so flipping `run` takes effect immediately. It also
	// means this fires on EVERY save, which is what the pending-batch guard in
	// startRun is for.
	switch mode, _ := host.ConfigGet("run"); mode {
	case "sample", "everything", "revibe", "migrate-tags":
		if err := startRun(mode); err != nil {
			logf(pdk.LogError, "run: FAILED to start: %v", err)
		}
	}
	return nil
}

// OnTaskExecute labels one batch of tracks. One invocation is one unit of work
// small enough to finish inside the 30-second ceiling.
func (p *plugin) OnTaskExecute(req taskworker.TaskExecuteRequest) (string, error) {
	switch req.QueueName {
	case queueLabel:
		return executeBatch(req.Payload)
	case queueRevibe:
		return executeRevibe(req.Payload)
	case queueMigrate:
		return executeMigrate(req.Payload)
	default:
		return "", fmt.Errorf("unknown queue %q", req.QueueName)
	}
}

// OnCallback runs the scheduled incremental sync.
func (p *plugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	switch req.ScheduleID {
	case scheduleSync:
		logf(pdk.LogInfo, "auto-sync: checking for newly added tracks")
		return syncNew()
	default:
		return fmt.Errorf("unknown schedule %q", req.ScheduleID)
	}
}

// configureAutoSync installs or removes the recurring sync to match config.
func configureAutoSync() error {
	if !configBool("autoSync", true) {
		// Cancelling an absent schedule is not an error worth surfacing.
		_ = host.SchedulerCancelSchedule(scheduleSync)
		return nil
	}
	cron, ok := host.ConfigGet("autoSyncCron")
	if !ok || cron == "" {
		cron = defaultSyncCron
	}
	_, err := host.SchedulerScheduleRecurring(cron, "", scheduleSync)
	return err
}
