//go:build !wasip1

package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/312-dev/navidrome-mood/internal/library"
)

func at(path string, unix int64) library.File {
	return library.File{Path: path, ModTime: time.Unix(unix, 0)}
}

func paths(files []library.File) []string { return library.Paths(files) }

// The point of the watermark: a file untouched since the last check is never
// opened. Opening every file on every tick is what does not fit in Navidrome's
// 30-second ceiling, and a 15-minute cron would hit it forever.
func TestAutoSyncOnlyLooksAtFilesModifiedSinceTheCursor(t *testing.T) {
	files := []library.File{
		at("/m/old.flac", 1000),
		at("/m/same.flac", 2000),
		at("/m/new.flac", 3000),
	}
	got := paths(changedSince(files, 2000, 500))
	if len(got) != 1 || got[0] != "/m/new.flac" {
		t.Fatalf("changed = %v, want only the file newer than the cursor", got)
	}
}

// With no cursor there is nothing to have checked, so everything is a candidate.
func TestAutoSyncChecksEverythingOnTheFirstRun(t *testing.T) {
	files := []library.File{at("/m/a.flac", 1000), at("/m/b.flac", 2000)}
	if got := paths(changedSince(files, 0, 500)); len(got) != 2 {
		t.Fatalf("changed = %v, want every file", got)
	}
}

// Oldest first, because the last one checked is what the cursor becomes. Any
// other order would move the watermark past files that were never opened.
func TestAutoSyncReturnsOldestFirst(t *testing.T) {
	files := []library.File{
		at("/m/c.flac", 3000),
		at("/m/a.flac", 1000),
		at("/m/b.flac", 2000),
	}
	got := changedSince(files, 0, 500)
	for i := 1; i < len(got); i++ {
		if got[i].ModTime.Before(got[i-1].ModTime) {
			t.Fatalf("not oldest first: %v", paths(got))
		}
	}
	if w := got[len(got)-1].ModTime.Unix(); w != 3000 {
		t.Fatalf("watermark would be %d, want 3000", w)
	}
}

// The cap is what keeps the first tick over an unlabelled library inside the
// invocation ceiling. The remainder is not lost: it is older than the watermark
// this tick records, so the next tick starts exactly where this one stopped.
func TestAutoSyncCapsHowManyFilesOneTickOpens(t *testing.T) {
	var files []library.File
	for i := 0; i < 10; i++ {
		files = append(files, at("/m/f.flac", int64(1000+i)))
	}
	got := changedSince(files, 0, 4)
	if len(got) != 4 {
		t.Fatalf("took %d files, want 4", len(got))
	}
	watermark := got[len(got)-1].ModTime.Unix()
	if watermark != 1003 {
		t.Fatalf("watermark = %d, want 1003", watermark)
	}
	// Resuming from that watermark must pick up the rest and nothing already done.
	next := changedSince(files, watermark, 4)
	if len(next) != 4 || next[0].ModTime.Unix() != 1004 {
		t.Fatalf("next tick = %v, want it to resume at 1004", paths(next))
	}
}

// A bulk import lands thousands of files on the same second. Cutting that second
// in half would leave the remainder permanently below the watermark, so the cap
// stretches to cover it.
func TestAutoSyncNeverCutsASecondInHalf(t *testing.T) {
	var files []library.File
	for i := 0; i < 10; i++ {
		files = append(files, at("/m/f.flac", 1000))
	}
	files = append(files, at("/m/later.flac", 2000))

	got := changedSince(files, 0, 4)
	if len(got) != 10 {
		t.Fatalf("took %d files, want all 10 sharing the second", len(got))
	}
	if w := got[len(got)-1].ModTime.Unix(); w != 1000 {
		t.Fatalf("watermark = %d, want 1000", w)
	}
	if next := changedSince(files, 1000, 4); len(next) != 1 {
		t.Fatalf("next tick = %v, want only the later file", paths(next))
	}
}

// A file listed with no modification time is one whose directory entry could not
// be stat'ed. Auto-sync leaves it to a whole-library pass rather than opening it
// on every tick forever.
func TestAutoSyncIgnoresFilesWithNoModTime(t *testing.T) {
	files := []library.File{{Path: "/m/unstattable.flac"}}
	if got := changedSince(files, 0, 500); len(got) != 0 {
		t.Fatalf("changed = %v, want nothing", paths(got))
	}
}

// kvKeys is every key this plugin may store under. All of them are aggregate:
// the whole point of reading labels back out of the files is that nothing is
// kept per track, and a 9,195-track library needed 3.4 MB against Navidrome's
// 1 MB default when it was.
var kvKeys = map[string]bool{
	"keyPending":    true,
	"keyBudget":     true,
	"keySyncCursor": true,
	"keyMoodOnly":   true,
	"keyStrikes":    true,
	"keyHalted":     true,
}

// The storage seam is host-only, so this asserts the key list rather than the
// store after a run: every write names one of the constants above, and a
// per-track key cannot be one of those - it would have to be built from a track
// ID at the call site.
//
// Writes only. Reading and deleting by a key the store itself listed is how the
// leftover per-track entries are reclaimed, and a per-track read could only
// follow a per-track write, which is what this catches.
//
// The wrappers in label.go take the key as a parameter, so `key` is allowed
// inside them; their own callers are checked by the same rule, which is what
// closes the loop.
func TestNothingIsStoredPerTrack(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	stores := map[string]bool{
		"KVStoreSet": true, "writeInt": true, "writeInt64": true,
		"addInt": true, "decrement": true,
	}

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				var fn string
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					fn = f.Sel.Name
				case *ast.Ident:
					fn = f.Name
				}
				if !stores[fn] {
					return true
				}
				ident, ok := call.Args[0].(*ast.Ident)
				if !ok {
					t.Errorf("%s: %s is called with a computed key rather than "+
						"one of the aggregate constants", fset.Position(call.Pos()), fn)
					return true
				}
				if !kvKeys[ident.Name] && ident.Name != "key" {
					t.Errorf("%s: %s is called with %q, which is not one of the "+
						"aggregate keys", fset.Position(call.Pos()), fn, ident.Name)
				}
				return true
			})
		}
	}
}

// A preview over the whole library is capped to a sample.
//
// The combination has no use: a preview writes nothing, so running it over
// everything buys the same answer a sample buys for pennies, at the price of the
// whole library. It has cost real money three times on one install, twice after
// the warning naming the cause was added, which is why this is a bound rather
// than another sentence in a log.
func TestPreviewOverEverythingIsCapped(t *testing.T) {
	src, err := os.ReadFile("label.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, `if mode == "sample" {`)
	if i < 0 {
		t.Fatal("cannot find the run-mode branch")
	}
	// The branch that is NOT sample mode has to narrow the file list too.
	rest := s[i:]
	end := strings.Index(rest, "paths := library.Paths(files)")
	if end < 0 {
		t.Fatal("cannot find the end of the run-mode branch")
	}
	branch := rest[:end]
	if !strings.Contains(branch, `configBool("dryRun"`) {
		t.Error("the run-mode branch does not consult dryRun, so a preview can run over the whole library")
	}
	if strings.Count(branch, "library.SampleAcross") < 2 {
		t.Error("only one path narrows the file list; a preview over everything is unbounded")
	}
}

// Concurrency defaults to 1 and is clamped to what the manifest declares.
//
// The default matters more than the clamp: spend is recorded by reading a total
// and writing it back, so parallel batches can lose each other's updates and the
// run limit stops being exact. Someone who never touches the setting must keep
// the hard cap.
func TestConcurrencyDefaultsToOneAndIsClamped(t *testing.T) {
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	c := m["config"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)["concurrency"].(map[string]any)
	if c["default"].(float64) != 1 {
		t.Errorf("concurrency defaults to %v, want 1: the spend cap is only exact at 1", c["default"])
	}
	declared := m["permissions"].(map[string]any)["taskqueue"].(map[string]any)["maxConcurrency"].(float64)
	if c["maximum"].(float64) > declared {
		t.Errorf("the setting allows %v but the permission declares %v; Navidrome would clamp it "+
			"and the setting would read as doing something it does not", c["maximum"], declared)
	}
	// The Go constant has to agree, or the clamp in concurrency() is wrong.
	src, err := os.ReadFile("label.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "maxConcurrency = "+strconv.Itoa(int(declared))) {
		t.Errorf("label.go does not clamp to %v", declared)
	}
}
