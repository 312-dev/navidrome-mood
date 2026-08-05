package flac

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// copyFixture puts a fixture in a temp dir so tests never mutate testdata.
func copyFixture(t *testing.T, src string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.WriteFile(dst, raw, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
	return dst
}

// audioOf returns every byte after the metadata chain. This is the thing that must
// never change: STREAMINFO stores an MD5 of the DECODED audio, so if the encoded
// bytes are identical the checksum stays valid by construction.
func audioOf(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	f, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return raw[f.AudioOffset:]
}

func blockSizes(t *testing.T, path string) map[BlockType]int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	f, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := map[BlockType]int{}
	for _, b := range f.Blocks {
		out[b.Type] += len(b.Data)
	}
	return out
}

// The headline safety property, asserted for every fixture.
func TestWritePreservesAudioExactly(t *testing.T) {
	for _, fx := range fixtures(t) {
		t.Run(filepath.Base(fx), func(t *testing.T) {
			path := copyFixture(t, fx)
			before := audioOf(t, path)
			sizeBefore := fileSize(t, path)

			strat, err := UpdateTags(path, func(c *Comments) error {
				c.Set("MOOD", "wistful", "hushed", "nocturnal")
				return nil
			})
			if err != nil {
				t.Fatalf("UpdateTags: %v", err)
			}

			if got := audioOf(t, path); !bytes.Equal(got, before) {
				t.Fatalf("AUDIO CHANGED (%s): %d bytes before, %d after",
					strat, len(before), len(got))
			}
			if strat == InPlace && fileSize(t, path) != sizeBefore {
				t.Fatalf("in-place write changed file size: %d -> %d",
					sizeBefore, fileSize(t, path))
			}

			f := parseAt(t, path)
			c, err := f.Comments()
			if err != nil {
				t.Fatalf("Comments: %v", err)
			}
			if got := c.Get("MOOD"); len(got) != 3 || got[0] != "wistful" {
				t.Fatalf("MOOD did not round trip: %v", got)
			}
			t.Logf("%s: strategy=%s", filepath.Base(fx), strat)
		})
	}
}

// Everything that is not the comment block must survive - especially PICTURE,
// which is where embedded cover art lives and is the most expensive thing to lose.
func TestWritePreservesOtherBlocks(t *testing.T) {
	for _, fx := range fixtures(t) {
		t.Run(filepath.Base(fx), func(t *testing.T) {
			path := copyFixture(t, fx)
			before := blockSizes(t, path)

			if _, err := UpdateTags(path, func(c *Comments) error {
				c.Set("MOOD", "brooding")
				return nil
			}); err != nil {
				t.Fatalf("UpdateTags: %v", err)
			}

			after := blockSizes(t, path)
			for typ, size := range before {
				// Comment and padding are expected to move; nothing else may.
				if typ == VorbisComment || typ == Padding {
					continue
				}
				if after[typ] != size {
					t.Fatalf("%s changed: %d -> %d bytes", typ, size, after[typ])
				}
			}
			if after[StreamInfo] != 34 {
				t.Fatalf("STREAMINFO is %d bytes, want 34", after[StreamInfo])
			}
		})
	}
}

// Existing tags written by other tools (Picard, beets, ffmpeg) must not be
// disturbed - the plugin adds MOOD, it does not own the file.
func TestWriteLeavesExistingTagsAlone(t *testing.T) {
	path := copyFixture(t, "../testdata/MultipleArtists.flac")

	f := parseAt(t, path)
	before, err := f.Comments()
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}

	if _, err := UpdateTags(path, func(c *Comments) error {
		c.Set("MOOD", "icy")
		return nil
	}); err != nil {
		t.Fatalf("UpdateTags: %v", err)
	}

	after, err := parseAt(t, path).Comments()
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if after.Vendor != before.Vendor {
		t.Fatalf("vendor changed: %q -> %q", before.Vendor, after.Vendor)
	}
	// Repeated ARTIST values are the interesting case: a naive map-based
	// implementation would collapse these three into one.
	for _, key := range []string{"ARTIST", "GENRE", "MUSICBRAINZ_ARTISTID", "TITLE"} {
		b, a := before.Get(key), after.Get(key)
		if len(b) != len(a) {
			t.Fatalf("%s: %d values before, %d after (%v -> %v)", key, len(b), len(a), b, a)
		}
		for i := range b {
			if b[i] != a[i] {
				t.Fatalf("%s[%d]: %q -> %q", key, i, b[i], a[i])
			}
		}
	}
}

// A comment too large for the padding must force the full-rewrite path, and that
// path must be just as safe.
func TestFullRewritePathIsSafeAndAddsPadding(t *testing.T) {
	path := copyFixture(t, "../testdata/MultipleArtists.flac")
	before := audioOf(t, path)

	big := string(bytes.Repeat([]byte("x"), 4096)) // exceeds the 1024-byte padding
	strat, err := UpdateTags(path, func(c *Comments) error {
		c.Set("COMMENT", big)
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateTags: %v", err)
	}
	if strat != FullRewrite {
		t.Fatalf("strategy = %s, want %s", strat, FullRewrite)
	}
	if got := audioOf(t, path); !bytes.Equal(got, before) {
		t.Fatalf("AUDIO CHANGED during full rewrite")
	}
	if got := parseAt(t, path); got.find(Padding) < 0 {
		t.Fatalf("full rewrite left no padding, so the next edit would rewrite again")
	}
	// No temp files left behind.
	entries, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".navidrome-mood-*"))
	if len(entries) != 0 {
		t.Fatalf("temp files leaked: %v", entries)
	}
}

// A file with no PADDING block at all - the case the plan flagged as needing
// special handling.
func TestFileWithNoPadding(t *testing.T) {
	path := copyFixture(t, "../testdata/MultipleArtists.flac")

	// Strip padding via a full rewrite, then confirm a later edit still works.
	raw, _ := os.ReadFile(path)
	f, _ := Parse(bytes.NewReader(raw))
	var kept []Block
	for _, b := range f.Blocks {
		if b.Type != Padding {
			kept = append(kept, b)
		}
	}
	f.Blocks = kept
	var buf bytes.Buffer
	if err := f.WriteMetadata(&buf); err != nil {
		t.Fatalf("WriteMetadata: %v", err)
	}
	stripped := append(buf.Bytes(), raw[f.AudioOffset:]...)
	if err := os.WriteFile(path, stripped, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := parseAt(t, path); got.find(Padding) >= 0 {
		t.Fatalf("setup failed: padding still present")
	}

	before := audioOf(t, path)
	strat, err := UpdateTags(path, func(c *Comments) error {
		c.Set("MOOD", "hushed")
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateTags on zero-padding file: %v", err)
	}
	if got := audioOf(t, path); !bytes.Equal(got, before) {
		t.Fatalf("AUDIO CHANGED on zero-padding file (%s)", strat)
	}
	if _, err := parseAt(t, path).Comments(); err != nil {
		t.Fatalf("unreadable after write: %v", err)
	}
}

// mtime must move, or Navidrome never notices the edit. An in-place write does not
// change file size, so mtime is the only signal the scanner has.
func TestWriteBumpsMtime(t *testing.T) {
	path := copyFixture(t, "../testdata/test.flac")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if _, err := UpdateTags(path, func(c *Comments) error {
		c.Set("MOOD", "surging")
		return nil
	}); err != nil {
		t.Fatalf("UpdateTags: %v", err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !st.ModTime().After(old) {
		t.Fatalf("mtime did not advance: %v", st.ModTime())
	}
}

// A no-op edit must not rewrite the file, so re-running the pass over an already
// tagged library is free and leaves mtime alone.
func TestNoOpEditWritesNothing(t *testing.T) {
	path := copyFixture(t, "../testdata/test.flac")
	if _, err := UpdateTags(path, func(c *Comments) error {
		c.Set("MOOD", "wistful")
		return nil
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	st1, _ := os.Stat(path)

	strat, err := UpdateTags(path, func(c *Comments) error {
		c.Set("MOOD", "wistful")
		return nil
	})
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if strat != Unchanged {
		t.Fatalf("strategy = %s, want %s", strat, Unchanged)
	}
	st2, _ := os.Stat(path)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatalf("no-op edit touched mtime: %v -> %v", st1.ModTime(), st2.ModTime())
	}
}

// Regression test for a bug that shipped and passed every other test: the
// STREAMINFO snapshot must not alias the block it is snapshotting, or the guard in
// UpdateTags compares a slice against itself and can never fire. Found by mutation
// testing on 2026-08-05 - corrupting STREAMINFO produced no failure at all.
func TestStreamInfoSnapshotDoesNotAlias(t *testing.T) {
	f := parseAt(t, "../testdata/test.flac")
	snap := snapshotStreamInfo(f)
	if len(snap) == 0 {
		t.Fatal("no STREAMINFO to snapshot")
	}

	i := f.find(StreamInfo)
	f.Blocks[i].Data[0] ^= 0xff

	if bytes.Equal(snap, f.streamInfo()) {
		t.Fatal("snapshot aliases the block: the STREAMINFO guard is a no-op")
	}
}

// An error from the edit callback must abort cleanly, leaving the file untouched.
func TestEditErrorLeavesFileUntouched(t *testing.T) {
	path := copyFixture(t, "../testdata/test.flac")
	orig, _ := os.ReadFile(path)

	_, err := UpdateTags(path, func(c *Comments) error {
		c.Set("MOOD", "should-not-persist")
		return errTest
	})
	if err == nil {
		t.Fatal("expected error")
	}
	now, _ := os.ReadFile(path)
	if !bytes.Equal(orig, now) {
		t.Fatal("file was modified despite the edit failing")
	}
}

var errTest = errTestType{}

type errTestType struct{}

func (errTestType) Error() string { return "test error" }

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	return st.Size()
}

func parseAt(t *testing.T, path string) *File {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	f, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return f
}
