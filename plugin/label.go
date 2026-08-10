package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"

	"github.com/312-dev/navidrome-mood/internal/library"
	"github.com/312-dev/navidrome-mood/internal/llm"
	"github.com/312-dev/navidrome-mood/internal/prompt"
	"github.com/312-dev/navidrome-mood/internal/runner"
)

// Everything this plugin keeps in the kvstore is aggregate: a spend counter, a
// queue depth, a halt flag, a watermark, one tally. It is a few hundred bytes
// whatever the library's size, because what has been labelled is recorded in the
// files themselves rather than here.
const (
	// keyPending guards re-enqueueing. OnInit re-runs on EVERY config save, so
	// without this, changing any unrelated setting mid-run would queue the whole
	// library again on top of itself.
	keyPending = "run:pending"
	keyBudget  = "run:budget"

	// keySyncCursor is the modification time, in unix seconds, of the newest file
	// auto-sync has already looked at. Everything at or below it has been checked
	// and needs no further attention until something touches it again.
	keySyncCursor = "sync:cursor"

	// keyMoodOnly tallies tracks skipped because they carry a mood tag without
	// the seven values a smart playlist filters on. It is what lets the load-time
	// warning name a number; passes add to it and startRun resets it, so it says
	// what labelling has actually seen rather than what is in the library.
	keyMoodOnly = "run:moodonly"

	// sampleSize is what "sample" mode labels: enough to judge whether the labels
	// are any good, cheap enough to throw away.
	sampleSize = 20

	defaultBatchSize = 20

	// oldRecordPrefix is the key prefix of the per-track entries some installs
	// still hold. Nothing reads them - what has been labelled is answered from
	// the files - but they count against the plugin's storage allowance, which
	// is 1MB. A library that filled 3.4MB of entries leaves no room for the spend
	// counter, and a spend counter that cannot be written halts everything.
	oldRecordPrefix = "track:"

	// sweepCap bounds how many of them one invocation deletes. Each delete is a
	// host call into SQLite and a library's worth of them has no reason to fit in
	// Navidrome's 30-second ceiling; the rest goes on the next load or the next
	// auto-sync tick.
	sweepCap = 500

	// syncScanCap bounds how many files one auto-sync tick opens, which is what
	// keeps the tick inside Navidrome's 30-second ceiling. It matters on the very
	// first tick, when there is no watermark and every file in the library counts
	// as changed: reading metadata for ~9,200 files takes well over a minute, so
	// an uncapped tick would time out and never record a watermark to resume from.
	// Whatever is left over is picked up on the next tick.
	syncScanCap = 500

	// maxConcurrency mirrors the taskqueue permission in the manifest. Navidrome
	// clamps a queue to whatever the permission declares, so a larger number here
	// would be accepted and quietly ignored.
	maxConcurrency = 4
)

// ensureQueue registers the label queue and its worker.
//
// Must run on EVERY load, not only when a run starts. Queue creation is what
// subscribes a worker to the queue, and the queue's tasks are durable in SQLite -
// so a plugin that reloads without calling this leaves queued work with nothing
// watching it. That bug parked a real batch: the dispatcher polls every 5s, and
// it sat pending for minutes because startRun returned at the already-queued
// guard before ever reaching the create call.
func ensureQueue() error {
	return host.TaskCreateQueue(queueLabel, host.QueueConfig{
		Concurrency: int32(concurrency()),
		MaxRetries:  3,
		BackoffMs:   5_000,
		DelayMs:     500,
		RetentionMs: 24 * 60 * 60 * 1000,
	})
}

// concurrency is how many batches may be in flight at once.
//
// One by default, and that default is about the spend cap rather than about
// speed. Spend is recorded by reading the counter, adding what a batch cost, and
// writing it back, so two batches finishing together can read the same starting
// value and one update is lost. At one worker that cannot happen and the cap is
// exact; above one it becomes an underestimate, and the ceiling is enforced by
// the lifetime cap instead.
//
// Navidrome polls the queue every 5 seconds, so a 13-second batch spends nearly
// a third of its wall clock waiting. Parallel batches are what recover that.
func concurrency() int {
	n := configInt("concurrency", 1)
	if n < 1 {
		return 1
	}
	if n > maxConcurrency {
		// Above what the taskqueue permission declares, Navidrome would clamp it
		// silently and the setting would read as doing something it does not.
		return maxConcurrency
	}
	return n
}

// syncNew queues files that have changed since the last check and are not
// already labelled.
//
// The check must be cheap enough to run every few minutes, because that is what
// makes it behave like "label on ingest" rather than "label eventually". Opening
// every file to see whether it is labelled would mean reading metadata from
// ~9,200 files on every poll to discover that nothing changed, which does not
// fit in Navidrome's 30-second ceiling even once.
//
// So the walk stats rather than opens, and only files newer than the watermark
// are opened at all. Writing a tag updates a file's mtime, so a track this
// plugin has just labelled comes back on the next tick; that costs one metadata
// read and nothing else, because it now reads as fully labelled and is skipped.
func syncNew() error {
	// First, and before the halt check: running out of storage is one of the
	// things that halts a run, and this is what gives the space back.
	reclaimPerTrackKeys()

	if r := haltedReason(); r != "" {
		return nil // stay stopped until the user changes something
	}
	pending, err := readInt(keyPending)
	if err != nil {
		return err
	}
	if pending > 0 {
		return nil // a run is already in flight; adding to it helps nobody
	}

	t, err := newFileTagger()
	if err != nil {
		return err
	}
	root := t.mounts[0]

	files, _, err := library.ListFiles(os.DirFS(root), root)
	if err != nil {
		return err
	}
	cursor, err := readInt64(keySyncCursor)
	if err != nil {
		return err
	}
	changed := changedSince(files, cursor, syncScanCap)
	if len(changed) == 0 {
		return nil // the whole poll cost one directory walk
	}

	tracks, failed := library.ReadTracks(os.DirFS(root), root, library.Paths(changed))
	for p, e := range failed {
		logf(pdk.LogWarn, "auto-sync: cannot read %s: %s", p, e)
	}
	var fresh []string
	for _, tr := range tracks {
		if !runner.FullyLabelled(tr.Tags) {
			fresh = append(fresh, tr.Path)
		}
	}
	if len(fresh) > 0 {
		logf(pdk.LogInfo, "auto-sync: %d of %d changed file(s) still need labelling",
			len(fresh), len(changed))
		if err := enqueue(fresh); err != nil {
			return err
		}
	}
	// Last, so that a failure anywhere above leaves the watermark where it was
	// and the same files are examined again rather than skipped forever.
	return writeInt64(keySyncCursor, changed[len(changed)-1].ModTime.Unix())
}

// changedSince returns the files modified after cursor, oldest first, at most n
// of them.
//
// The cap is extended to cover every file sharing the last second, because the
// watermark has one-second resolution and cutting a second in half would leave
// the remainder permanently below it. It also keeps a bulk import, where
// thousands of files land on the same mtime, from pinning the watermark and
// making every tick redo the same work.
func changedSince(files []library.File, cursor int64, n int) []library.File {
	var out []library.File
	for _, f := range files {
		if f.ModTime.Unix() > cursor {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.Before(out[j].ModTime) })
	if len(out) <= n {
		return out
	}
	end := n
	for end < len(out) && out[end].ModTime.Unix() == out[end-1].ModTime.Unix() {
		end++
	}
	return out[:end]
}

// startRun enumerates the library and enqueues labelling work.
//
// Enumeration opens nothing: it is one directory walk for paths and modification
// times. Reading metadata for ~9,200 files takes over a minute and this runs
// inside Navidrome's 30-second ceiling, so each task reads metadata for its own
// batch instead.
func startRun(mode string) error {
	if r := haltedReason(); r != "" {
		logf(pdk.LogWarn, "run: not starting, work is halted: %s", describeHalt(r))
		return nil
	}
	pending, err := readInt(keyPending)
	if err != nil {
		return err
	}
	if pending > 0 {
		logf(pdk.LogInfo, "run: %d batches still queued, not starting another", pending)
		return nil
	}

	t, err := newFileTagger()
	if err != nil {
		return err
	}
	root := t.mounts[0]

	files, unsupported, err := library.ListFiles(os.DirFS(root), root)
	if err != nil {
		return fmt.Errorf("enumerating %s: %w", root, err)
	}
	logf(pdk.LogInfo, "run: %d taggable files under %s", len(files), root)
	for _, u := range unsupported {
		// Reported, never silently dropped, so the counts reconcile.
		logf(pdk.LogWarn, "run: skipping %s (%s)", u.Path, u.Reason)
	}
	if len(files) == 0 {
		return fmt.Errorf("no FLAC files found under %s", root)
	}

	if mode == "sample" {
		// Spread across the library rather than taking the head. Taking the head
		// is how the mount self-test ended up generalising from one file that
		// turned out to be one of three outliers in 9,195.
		files = library.SampleAcross(files, sampleSize)
		logf(pdk.LogInfo, "run: sample mode, %d files spread across the library", len(files))
	} else if configBool("dryRun", false) {
		// Preview over the whole library is the one settings combination with no
		// use and a real cost. A preview answers "do I like these labels", which
		// a sample answers for pennies; run over everything it buys the identical
		// answer for the price of the whole pass and leaves nothing behind,
		// because the tags in the files are the only record this plugin keeps.
		//
		// It is not hypothetical. It has happened three times on one install: a
		// full pass to $24.98 of a $25 limit, then twice more after the warning
		// that names the cause was added. The setting is the only one where the
		// cautious-sounding choice is the expensive one, so bounding it is worth
		// more than describing it again.
		files = library.SampleAcross(files, sampleSize)
		logf(pdk.LogWarn, "run: preview mode is on, so this is capped at %d files "+
			"spread across the library. A preview writes nothing and costs full "+
			"price, and running it over everything buys the same answer as a "+
			"sample for the price of the whole library. Turn off 'Preview only, "+
			"do not change my files' to label for real.", len(files))
	}
	paths := library.Paths(files)

	// Cost the run before committing to it. Refusing here is far cheaper than
	// discovering the limit halfway through 460 batches.
	provider, err := buildProvider()
	if err != nil {
		return err
	}
	lifetime, cap := lifetimeState()
	if err := preflight(len(paths), provider.ModelID(),
		configFloat("maxSpendUsd", 0), lifetime, cap); err != nil {
		return fmt.Errorf("refusing to start: %w", err)
	}

	// The tally is what this pass finds, not the sum of every pass before it.
	if err := writeInt(keyMoodOnly, 0); err != nil {
		return err
	}
	return enqueue(paths)
}

// reclaimPerTrackKeys deletes per-track entries the store is still holding.
//
// Run from both the load path and the auto-sync tick so it finishes on its own:
// a library's worth of them takes several passes at sweepCap each, and an install
// where nobody touches a setting again would otherwise stay full forever. Silent
// when there is nothing to do, which is the usual case, so it costs one list.
func reclaimPerTrackKeys() {
	keys, err := host.KVStoreList(oldRecordPrefix)
	if err != nil || len(keys) == 0 {
		return
	}
	n := len(keys)
	if n > sweepCap {
		n = sweepCap
	}
	for _, k := range keys[:n] {
		if err := host.KVStoreDelete(k); err != nil {
			logf(pdk.LogWarn, "could not reclaim %s: %v", k, err)
			return
		}
	}
	logf(pdk.LogInfo, "reclaimed %d of %d per-track storage entries, which nothing "+
		"reads: a track's own tags are what say whether it has been labelled", n, len(keys))
}

// warnAboutPartialLabels reports tracks left alone because they carry a mood tag
// and nothing else.
//
// One kvstore read, no file opens, so it is cheap enough to run on every load.
// It says nothing about who wrote those mood tags, because nothing can tell: an
// older version of this plugin wrote the word alone, and so do Picard, beets and
// a person editing a file by hand. That ambiguity is the whole reason the
// decision is handed back to the user, and it is why the warning has to name
// what turning the setting off would cost as well as what it would fix.
func warnAboutPartialLabels() {
	n, err := readInt(keyMoodOnly)
	if err != nil || n <= 0 {
		return
	}
	logf(pdk.LogWarn, "labelling has found %d tracks carrying a mood tag without the five "+
		"axes, tempo and vocal, which is everything a smart playlist filters on. Nothing "+
		"can tell whether an older version of this plugin wrote those words or Picard, "+
		"beets or you did, so they are left alone while 'Leave existing mood tags alone' "+
		"is on. Turning it off sends them through labelling again, and also replaces any "+
		"mood tag another tool wrote. Tracks already carrying the full set are skipped "+
		"either way and cost nothing.", n)
}

// lifetimeState reads total spend and its ceiling.
func lifetimeState() (spent, cap float64) {
	cap = configFloat("lifetimeCapUsd", defaultLifetimeCap)
	raw, ok, err := host.KVStoreGet(keyBudget)
	if err != nil || !ok {
		return 0, cap
	}
	var snap llm.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return 0, cap
	}
	return snap.LifetimeUSD, cap
}

// enqueue splits paths into batches and queues them.
func enqueue(paths []string) error {
	size := configInt("batchSize", defaultBatchSize)
	var queued int
	for i := 0; i < len(paths); i += size {
		end := i + size
		if end > len(paths) {
			end = len(paths)
		}
		payload, err := json.Marshal(paths[i:end])
		if err != nil {
			return err
		}
		if _, err := host.TaskEnqueue(queueLabel, payload); err != nil {
			return fmt.Errorf("enqueueing batch %d: %w", queued, err)
		}
		queued++
	}
	if err := writeInt(keyPending, queued); err != nil {
		return err
	}
	logf(pdk.LogInfo, "queued %d batches of up to %d (%d files)", queued, size, len(paths))
	return nil
}

// executeBatch labels one batch. Returns a human-readable summary, which the task
// queue stores and Navidrome surfaces.
func executeBatch(payload []byte) (string, error) {
	var paths []string
	if err := json.Unmarshal(payload, &paths); err != nil {
		return "", fmt.Errorf("bad payload: %w", err)
	}

	t, err := newFileTagger()
	if err != nil {
		return "", err
	}
	root := t.mounts[0]

	tracks, failed := library.ReadTracks(os.DirFS(root), root, paths)
	for p, e := range failed {
		logf(pdk.LogWarn, "batch: cannot read %s: %s", p, e)
	}

	provider, err := buildProvider()
	if err != nil {
		return "", err
	}
	budget, err := loadBudget(provider.ModelID())
	if err != nil {
		return "", err
	}
	lifetime, cap := lifetimeState()
	budget.SetLifetime(lifetime, cap)

	// Every track goes to the runner, which decides what to skip from the tags the
	// read above already produced. Deciding here would mean two places holding the
	// same rule, and the expensive way for them to disagree is the one that sends
	// finished tracks back through paid labelling.
	skipTagged := configBool("skipTagged", true)
	dryRun := configBool("dryRun", false)
	items := make([]runner.Item, 0, len(tracks))
	for _, tr := range tracks {
		items = append(items, runner.Item{Track: tr.Meta, Path: tr.Path, Tags: tr.Tags})
	}

	r := &runner.Runner{
		Provider: provider,
		Budget:   budget,
		Tagger:   t,
		System:   buildPrompt(),
		Opts: runner.Options{
			SkipTagged: skipTagged,
			DryRun:     dryRun,
		},
	}

	out, procErr := r.Process(items)

	// Persist even on failure: those tokens were spent either way, and a cap that
	// only counts successful batches is not counting money. If the write fails,
	// persistSpend halts everything - a limit that cannot be saved is not a limit.
	persistSpend(budget)
	recordOutcome(out.Cost, out.Labelled)
	decrement(keyPending)
	addInt(keyMoodOnly, out.MoodOnly)

	summary := fmt.Sprintf("requested=%d skipped=%d labelled=%d written=%d unresolved=%d cost=$%.4f",
		out.Requested, out.Skipped, out.Labelled, out.Written, len(out.Unresolved), out.Cost)
	if len(out.Unresolved) > 0 {
		// Not marked done, so a later run retries them. Say so rather than
		// letting the count quietly disagree.
		logf(pdk.LogWarn, "batch: %d tracks came back unlabelled and will be retried: %v",
			len(out.Unresolved), out.Unresolved)
	}
	for id, why := range out.Rejected {
		// A rejected label is a different problem from a missing one: the model
		// answered and the answer was out of range. Naming it is what makes a
		// provider that consistently ignores the schema visible.
		logf(pdk.LogWarn, "batch: rejected the label for %s: %s", id, why)
	}
	if out.Labelled > 0 && out.Written == 0 {
		// Paying for labels and writing none is the failure that hides inside a
		// successful-looking summary, because every count except one is healthy.
		// It happened on a real install: a library reached $24.98 of a $25 limit
		// with preview mode on and never wrote a tag. Say the two likely causes
		// out loud, since neither raises an error anywhere.
		why := "preview mode is on, or Navidrome has not granted this plugin write access"
		if dryRun {
			why = "preview mode is on, so nothing is written and the run still costs full price"
		}
		logf(pdk.LogWarn, "batch: paid for %d labels and wrote 0 tags: %s", out.Labelled, why)
	}
	if procErr != nil {
		// Only surface an error the queue will retry when retrying could actually
		// help. Every retry is another billable request, so a permanent failure
		// re-run three times is triple the cost for an identical outcome.
		if !llm.Retryable(procErr) {
			logf(pdk.LogError, "batch failed permanently, not retrying: %v", procErr)
			if isFatal(procErr) {
				halt(fmt.Sprintf("%v", procErr))
			}
			return summary + " (permanent failure: " + procErr.Error() + ")", nil
		}
		return summary, procErr
	}
	logf(pdk.LogInfo, "batch: %s", summary)
	return summary, nil
}

func buildProvider() (llm.Provider, error) {
	key, _ := host.ConfigGet("apiKey")
	if key == "" {
		return nil, fmt.Errorf("no API key configured")
	}
	model, _ := host.ConfigGet("model")

	switch p, _ := host.ConfigGet("provider"); p {
	case "openai-compatible":
		if model == "" {
			return nil, fmt.Errorf("openai-compatible needs an explicit model")
		}
		base, _ := host.ConfigGet("baseUrl")
		return &llm.OpenAICompatible{
			Doer: httpDoer{}, APIKey: key, Model: model, BaseURL: base,
		}, nil
	default:
		return &llm.Anthropic{Doer: httpDoer{}, APIKey: key, Model: model}, nil
	}
}

// buildPrompt assembles the system prompt.
//
// There is no per-install context to pass and no setting for the vocabulary. A
// user-supplied word has no anchor in mood-space: it can reach the `mood` tag,
// but it cannot be placed in any vibe region and nothing downstream can reason
// about where it sits relative to anything else. The 52 anchored terms are the
// whole vocabulary, and they mean the same thing on every server.
func buildPrompt() string {
	return prompt.Build(prompt.Library{})
}

func loadBudget(model string) (*llm.Budget, error) {
	limit := configFloat("maxSpendUsd", 25)
	raw, ok, err := host.KVStoreGet(keyBudget)
	if err != nil {
		return nil, err
	}
	if ok {
		var snap llm.Snapshot
		if err := json.Unmarshal(raw, &snap); err == nil && snap.Model == model && snap.LimitUSD == limit {
			return llm.Restore(snap)
		}
		// Model or limit changed, so the old counter no longer describes this
		// configuration. Start fresh rather than carrying spend across.
	}
	return llm.NewBudget(model, limit)
}

func saveBudget(b *llm.Budget) error {
	raw, err := json.Marshal(b.Snapshot())
	if err != nil {
		return err
	}
	return host.KVStoreSet(keyBudget, raw)
}

func readInt(key string) (int, error) {
	raw, ok, err := host.KVStoreGet(key)
	if err != nil || !ok {
		return 0, err
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, nil
	}
	return n, nil
}

func writeInt(key string, n int) error {
	return host.KVStoreSet(key, []byte(strconv.Itoa(n)))
}

// readInt64 and writeInt64 exist for the sync watermark. GOARCH=wasm is 32-bit,
// so a unix time stored through readInt would overflow in 2038.
func readInt64(key string) (int64, error) {
	raw, ok, err := host.KVStoreGet(key)
	if err != nil || !ok {
		return 0, err
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, nil
	}
	return n, nil
}

func writeInt64(key string, n int64) error {
	return host.KVStoreSet(key, []byte(strconv.FormatInt(n, 10)))
}

func addInt(key string, delta int) {
	if delta == 0 {
		return
	}
	n, err := readInt(key)
	if err != nil {
		return
	}
	_ = writeInt(key, n+delta)
}

func decrement(key string) {
	n, err := readInt(key)
	if err != nil || n <= 0 {
		_ = host.KVStoreSet(key, []byte("0"))
		return
	}
	_ = writeInt(key, n-1)
}

func configBool(key string, def bool) bool {
	v, ok := host.ConfigGet(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func configInt(key string, def int) int {
	if v, ok := host.ConfigGetInt(key); ok && v > 0 {
		return int(v)
	}
	return def
}

func configFloat(key string, def float64) float64 {
	v, ok := host.ConfigGet(key)
	if !ok || v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

// isFatal reports failures that will recur for every remaining batch, so the
// whole run should stop rather than failing 460 times in sequence.
func isFatal(err error) bool {
	return errors.Is(err, llm.ErrAuth) || errors.Is(err, llm.ErrBudgetExhausted)
}
