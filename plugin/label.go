package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"

	"github.com/312-dev/navidrome-mood/internal/library"
	"github.com/312-dev/navidrome-mood/internal/llm"
	"github.com/312-dev/navidrome-mood/internal/prompt"
	"github.com/312-dev/navidrome-mood/internal/runner"
)

const (
	// keyPending guards re-enqueueing. OnInit re-runs on EVERY config save, so
	// without this, changing any unrelated setting mid-run would queue the whole
	// library again on top of itself.
	keyPending = "run:pending"
	keyBudget  = "run:budget"

	// keyRecordSchema is the record shape the last whole-library pass wrote.
	//
	// Records below runner.RecordSchema describe a track that was tagged with the
	// mood words alone, and the runner sends those back through labelling. Nothing
	// prompts it to: auto-sync only queues files it has never seen, so an upgraded
	// install would sit there with nine tags missing per track and no error. This
	// is what makes that state say so out loud.
	keyRecordSchema = "records:schema"

	// sampleSize is what "sample" mode labels: enough to judge whether the labels
	// are any good, cheap enough to throw away.
	sampleSize = 20

	defaultBatchSize = 20
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
		Concurrency: 1, // one LLM request at a time; the cap is spend, not speed
		MaxRetries:  3,
		BackoffMs:   5_000,
		DelayMs:     500,
		RetentionMs: 24 * 60 * 60 * 1000,
	})
}

// syncNew queues only files this plugin has never seen.
//
// The check must be cheap enough to run every few minutes, because that is what
// makes it behave like "label on ingest" rather than "label eventually". Queuing
// everything and letting each task skip the tagged ones would mean re-reading
// metadata from ~9,200 files on every poll to discover that nothing changed.
//
// So: one directory walk for paths, ONE KVStoreList call for what is already
// known, and a set difference. No file is opened unless it is actually new.
func syncNew() error {
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

	paths, _, err := library.ListFiles(os.DirFS(root), root)
	if err != nil {
		return err
	}

	known, err := host.KVStoreList(runner.RecordPrefix)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(known))
	for _, k := range known {
		seen[strings.TrimPrefix(k, runner.RecordPrefix)] = true
	}

	var fresh []string
	for _, p := range paths {
		if !seen[p] {
			fresh = append(fresh, p)
		}
	}
	if len(fresh) == 0 {
		return nil // nothing new; the whole poll cost one walk and one list
	}
	logf(pdk.LogInfo, "auto-sync: %d new file(s) since last pass", len(fresh))
	return enqueue(fresh)
}

// startRun enumerates the library and enqueues labelling work.
//
// Enumeration lists paths only. Reading metadata for ~9,200 files takes over a
// minute and this runs inside Navidrome's 30-second ceiling, so each task reads
// metadata for its own batch instead.
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

	paths, unsupported, err := library.ListFiles(os.DirFS(root), root)
	if err != nil {
		return fmt.Errorf("enumerating %s: %w", root, err)
	}
	logf(pdk.LogInfo, "run: %d taggable files under %s", len(paths), root)
	for _, u := range unsupported {
		// Reported, never silently dropped, so the counts reconcile.
		logf(pdk.LogWarn, "run: skipping %s (%s)", u.Path, u.Reason)
	}
	if len(paths) == 0 {
		return fmt.Errorf("no FLAC files found under %s", root)
	}

	if mode == "sample" {
		// Spread across the library rather than taking the head. Taking the head
		// is how the mount self-test ended up generalising from one file that
		// turned out to be one of three outliers in 9,195.
		paths = library.SampleAcross(paths, sampleSize)
		logf(pdk.LogInfo, "run: sample mode, %d files spread across the library", len(paths))
	}
	if mode == "everything" {
		// A whole-library pass is what brings older records forward, so it is the
		// point the warning can stop. Recorded on enqueue rather than on
		// completion: the batches are durable and the runner is what decides per
		// track, so a restart mid-pass resumes rather than losing the work.
		if err := writeInt(keyRecordSchema, runner.RecordSchema); err != nil {
			return err
		}
	}

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

	return enqueue(paths)
}

// warnAboutStaleRecords reports tracks carried over from an older record shape.
//
// One KVStoreList, no file opens and no per-track reads, so it is cheap enough to
// run on every load. It cannot say how many of those records are stale without
// reading each one, which is why it names the fix rather than a count.
func warnAboutStaleRecords() {
	if n, err := readInt(keyRecordSchema); err != nil || n >= runner.RecordSchema {
		return
	}
	known, err := host.KVStoreList(runner.RecordPrefix)
	if err != nil {
		return
	}
	if len(known) == 0 {
		// Nothing has ever been labelled, so there is nothing to bring forward.
		_ = writeInt(keyRecordSchema, runner.RecordSchema)
		return
	}
	logf(pdk.LogWarn, "%d tracks were labelled by an older version that wrote only the "+
		"mood words. They are missing the five axes, tempo, vocal and vibe, which is "+
		"everything a smart playlist filters on. Set 'Label my library' to 'everything' "+
		"to bring them forward; tracks already carrying all ten tags are skipped and "+
		"cost nothing.", len(known))
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

	// Every track goes to the runner, which decides what to skip. A mood tag on
	// disk is no longer enough on its own to answer that: a file this plugin
	// labelled before the ten-tag write carries one and is still missing the nine
	// that matter, and only the stored record says which of the two it is.
	// Deciding here would filter those tracks out before the runner could tell.
	skipTagged := configBool("skipTagged", true)
	items := make([]runner.Item, 0, len(tracks))
	for _, tr := range tracks {
		items = append(items, runner.Item{Track: tr.Meta, Path: tr.Path})
	}

	r := &runner.Runner{
		Provider: provider,
		Budget:   budget,
		Store:    kvStore{},
		Tagger:   t,
		System:   buildPrompt(),
		Opts: runner.Options{
			SkipTagged: skipTagged,
			DryRun:     configBool("dryRun", false),
		},
	}

	out, procErr := r.Process(items)

	// Persist even on failure: those tokens were spent either way, and a cap that
	// only counts successful batches is not counting money. If the write fails,
	// persistSpend halts everything - a limit that cannot be saved is not a limit.
	persistSpend(budget)
	recordOutcome(out.Cost, out.Labelled)
	decrement(keyPending)

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
