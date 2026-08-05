package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"

	"github.com/312-dev/navidrome-mood/flac"
)

// selfTestTag is deliberately not a real vocabulary term, so a leftover from an
// aborted self-test can never be mistaken for a genuine label or matched by a
// smart playlist.
const selfTestTag = "navidrome-mood-selftest"

// runSelfTest proves the library mount works end to end before anything touches
// the library in bulk.
//
// It exists because the mount is the one part of this plugin that cannot be
// tested outside wasm: the FLAC writer is exhaustively tested on the host, but
// the host does not have Docker Desktop's virtiofs layer underneath it. Whether
// a write actually lands through that is a separate question from whether the
// writer is correct, and it is much better answered against one file than during
// a 9,000-file pass.
//
// The write is self-reverting: the tag is removed and the removal verified.
func runSelfTest(write bool) error {
	libs, err := host.LibraryGetAllLibraries()
	if err != nil {
		return fmt.Errorf("listing libraries: %w", err)
	}
	for _, l := range libs {
		logf(pdk.LogInfo, "selftest: library %d %q hostPath=%q mountPoint=%q songs=%d",
			l.ID, l.Name, l.Path, l.MountPoint, l.TotalSongs)
	}

	t, err := newFileTagger()
	if err != nil {
		return err
	}
	logf(pdk.LogInfo, "selftest: mounts visible to the plugin: %v", t.mounts)

	target, err := firstFLAC(t.mounts)
	if err != nil {
		return err
	}
	logf(pdk.LogInfo, "selftest: target %s", target)

	// --- READ ---
	fh, err := os.Open(target)
	if err != nil {
		return fmt.Errorf("READ FAILED opening %s: %w", target, err)
	}
	parsed, perr := flac.Parse(fh)
	fh.Close()
	if perr != nil {
		return fmt.Errorf("READ FAILED parsing %s: %w", target, perr)
	}
	var blocks []string
	for _, b := range parsed.Blocks {
		blocks = append(blocks, fmt.Sprintf("%s:%d", b.Type, len(b.Data)))
	}
	logf(pdk.LogInfo, "selftest: READ OK  audioAt=%d blocks=[%s]",
		parsed.AudioOffset, strings.Join(blocks, " "))

	existing, err := t.ReadMood(target)
	if err != nil {
		return fmt.Errorf("READ FAILED reading MOOD: %w", err)
	}
	logf(pdk.LogInfo, "selftest: existing MOOD=%v", existing)

	if !write {
		logf(pdk.LogInfo, "selftest: read-only mode, stopping before any write")
		return nil
	}
	if len(existing) > 0 {
		// Refuse rather than clobber: this file already carries moods from
		// somewhere, and a self-test must never be the thing that loses them.
		return fmt.Errorf("selftest: %s already has MOOD=%v, refusing to write over it",
			target, existing)
	}

	// --- WRITE ---
	before, err := os.Stat(target)
	if err != nil {
		return err
	}
	strat, err := t.WriteMood(target, []string{selfTestTag})
	if err != nil {
		return fmt.Errorf("WRITE FAILED on %s: %w", target, err)
	}
	after, err := os.Stat(target)
	if err != nil {
		return err
	}
	logf(pdk.LogInfo, "selftest: WRITE OK strategy=%s size %d->%d mtime %d->%d",
		strat, before.Size(), after.Size(),
		before.ModTime().Unix(), after.ModTime().Unix())

	// --- VERIFY ---
	got, err := t.ReadMood(target)
	if err != nil {
		return fmt.Errorf("VERIFY FAILED: %w", err)
	}
	if len(got) != 1 || got[0] != selfTestTag {
		return fmt.Errorf("VERIFY FAILED: read back %v, want [%s]", got, selfTestTag)
	}
	logf(pdk.LogInfo, "selftest: VERIFY OK read back %v", got)

	// --- REVERT ---
	if _, err := t.WriteMood(target, nil); err != nil {
		return fmt.Errorf("REVERT FAILED, %s still carries %s: %w", target, selfTestTag, err)
	}
	final, err := t.ReadMood(target)
	if err != nil {
		return fmt.Errorf("REVERT VERIFY FAILED: %w", err)
	}
	if len(final) != 0 {
		return fmt.Errorf("REVERT FAILED: %s still has MOOD=%v", target, final)
	}
	logf(pdk.LogInfo, "selftest: REVERT OK, file is back to its original state")
	logf(pdk.LogInfo, "selftest: PASSED - the library mount is writable and the FLAC writer works through it")
	return nil
}

// firstFLAC finds one .flac under the mounts, walking rather than assuming a flat
// layout - most libraries are nested even though this one is not.
func firstFLAC(mounts []string) (string, error) {
	for _, m := range mounts {
		var found string
		err := fs.WalkDir(os.DirFS(m), ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtree, keep looking
			}
			if d.IsDir() || !strings.EqualFold(path.Ext(p), ".flac") {
				return nil
			}
			found = path.Join(m, p)
			return fs.SkipAll
		})
		if err != nil && found == "" {
			continue
		}
		if found != "" {
			return found, nil
		}
	}
	return "", fmt.Errorf("no .flac found under any mount (%v)", mounts)
}

func logf(level pdk.LogLevel, format string, args ...any) {
	pdk.Log(level, fmt.Sprintf(format, args...))
}
