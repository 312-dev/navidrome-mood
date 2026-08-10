package library

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/312-dev/navidrome-mood/flac"
	"github.com/312-dev/navidrome-mood/internal/runner"
)

func scanTestdata(t *testing.T) *Result {
	t.Helper()
	res, err := Scan(os.DirFS("../../testdata"), "/libraries/1")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Tracks) == 0 {
		t.Fatal("no tracks found in testdata")
	}
	return res
}

func TestScanReadsMetadataFromTheFiles(t *testing.T) {
	res := scanTestdata(t)
	if len(res.Failed) != 0 {
		t.Fatalf("failures: %v", res.Failed)
	}

	byBase := map[string]Track{}
	for _, tr := range res.Tracks {
		byBase[filepath.Base(tr.Path)] = tr
	}

	got, ok := byBase["MultipleArtists.flac"]
	if !ok {
		t.Fatalf("fixture missing; found %v", byBase)
	}
	if got.Meta.Title != "Test Track" || got.Meta.Album != "Test Album" {
		t.Fatalf("metadata wrong: %+v", got.Meta)
	}
	if got.Meta.Year != 2024 {
		t.Fatalf("year = %d, want 2024", got.Meta.Year)
	}
	// GENRE repeats in that fixture; both values must survive, since a naive
	// single-value read would lose half the genre information.
	if len(got.Meta.Genres) != 2 {
		t.Fatalf("genres = %v, want 2 values", got.Meta.Genres)
	}
	// Paths must be rooted at the mount, or nothing can open them later.
	if got.Path != "/libraries/1/MultipleArtists.flac" {
		t.Fatalf("path = %q, not rooted at the mount", got.Path)
	}
}

func TestScanIsDeterministic(t *testing.T) {
	first := scanTestdata(t)
	for i := 0; i < 5; i++ {
		again := scanTestdata(t)
		if len(again.Tracks) != len(first.Tracks) {
			t.Fatal("track count varied between scans")
		}
		for j := range first.Tracks {
			if again.Tracks[j].Path != first.Tracks[j].Path {
				t.Fatalf("order varied: %q vs %q", again.Tracks[j].Path, first.Tracks[j].Path)
			}
		}
	}
}

// A resumed run must enumerate identically, or batches drift against what was
// already done.
func TestScanOrderIsSorted(t *testing.T) {
	res := scanTestdata(t)
	for i := 1; i < len(res.Tracks); i++ {
		if res.Tracks[i-1].Path >= res.Tracks[i].Path {
			t.Fatalf("not sorted at %d: %q then %q",
				i, res.Tracks[i-1].Path, res.Tracks[i].Path)
		}
	}
}

// Untaggable audio must be reported rather than silently dropped: a gap of 2
// today becomes an unexplained gap of 200 once someone adds ALAC.
func TestUnsupportedAudioIsReportedNotSkipped(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile("../../testdata/test.flac")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("real.flac", raw)
	write("song.mp3", []byte("ID3 not really an mp3"))
	write("song.m4a", []byte("not really m4a"))
	write("cover.jpg", []byte("jpeg"))
	write("notes.txt", []byte("text"))

	res, err := Scan(os.DirFS(dir), "/m")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(res.Tracks))
	}
	if len(res.Unsupported) != 2 {
		t.Fatalf("unsupported = %v, want the mp3 and the m4a", res.Unsupported)
	}
	// Non-audio must not be reported at all - a cover image is not a gap.
	for _, u := range res.Unsupported {
		if filepath.Ext(u.Path) == ".jpg" || filepath.Ext(u.Path) == ".txt" {
			t.Fatalf("non-audio reported as unsupported: %v", u)
		}
	}
}

// A FLAC that will not parse is a different problem from an unsupported format,
// and conflating them hides real corruption.
func TestUnparseableFLACIsRecordedAsFailed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.flac"), []byte("fLaCnope"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Scan(os.DirFS(dir), "/m")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tracks) != 0 {
		t.Fatal("a broken file was returned as a track")
	}
	if len(res.Failed) != 1 {
		t.Fatalf("failed = %v, want 1 entry", res.Failed)
	}
	if len(res.Unsupported) != 0 {
		t.Fatal("a broken FLAC was misreported as an unsupported format")
	}
}

// Whether a track has already been labelled is answered from its own tags, so
// those tags have to come back out of the same pass that reads the title and the
// artist. Reading them separately would mean opening every file twice.
func TestReadTracksReportsTheMoodTagsInTheFile(t *testing.T) {
	dir := t.TempDir()
	labelled := writeFixture(t, dir, "labelled.flac", map[string][]string{
		runner.TagEnergy:       {"70"},
		runner.TagValence:      {"40"},
		runner.TagIntensity:    {"60"},
		runner.TagAcousticness: {"30"},
		runner.TagDensity:      {"55"},
		runner.TagTempo:        {"mid"},
		runner.TagVocal:        {"sung"},
		runner.TagMood:         {"melancholy", "warm"},
	})
	bare := writeFixture(t, dir, "bare.flac", nil)

	tracks, failed := ReadTracks(os.DirFS(dir), "/m", []string{labelled, bare})
	if len(failed) != 0 {
		t.Fatalf("failed: %v", failed)
	}
	byPath := map[string]Track{}
	for _, tr := range tracks {
		byPath[tr.Path] = tr
	}

	got := byPath[labelled]
	if !runner.FullyLabelled(got.Tags) {
		t.Fatalf("a file carrying the full set does not read as labelled: %v", got.Tags)
	}
	// Multi-valued tags must survive whole; keeping only the first would make a
	// two-word mood look like a one-word one.
	if v := got.Tags[runner.TagMood]; len(v) != 2 {
		t.Fatalf("%s = %v, want both values", runner.TagMood, v)
	}
	// Only the plugin's own names, so nothing else in the file is carried around.
	for name := range got.Tags {
		if !contains(runner.TagNames, name) {
			t.Errorf("read %q, which is not one of the plugin's tags", name)
		}
	}

	if runner.FullyLabelled(byPath[bare].Tags) {
		t.Fatalf("a file with no mood tags reads as labelled: %v", byPath[bare].Tags)
	}
}

// writeFixture copies the test FLAC into dir under name, sets the given tags on
// it, and returns the path as ReadTracks will see it.
func writeFixture(t *testing.T, dir, name string, tags map[string][]string) string {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/test.flac")
	if err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if len(tags) > 0 {
		if _, err := flac.UpdateTags(full, func(c *flac.Comments) error {
			for k, v := range tags {
				c.Set(k, v...)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return "/m/" + name
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func TestBatch(t *testing.T) {
	mk := func(n int) []Track {
		out := make([]Track, n)
		for i := range out {
			out[i] = Track{Path: string(rune('a' + i))}
		}
		return out
	}
	cases := []struct{ items, size, batches, last int }{
		{10, 3, 4, 1},
		{9, 3, 3, 3},
		{1, 20, 1, 1},
		{0, 20, 0, 0},
	}
	for _, c := range cases {
		got := Batch(mk(c.items), c.size)
		if len(got) != c.batches {
			t.Fatalf("%d items / %d = %d batches, want %d", c.items, c.size, len(got), c.batches)
		}
		if c.batches > 0 && len(got[len(got)-1]) != c.last {
			t.Fatalf("last batch has %d, want %d", len(got[len(got)-1]), c.last)
		}
		// No track may be lost or duplicated.
		total := 0
		for _, b := range got {
			total += len(b)
		}
		if total != c.items {
			t.Fatalf("batching lost tracks: %d of %d", total, c.items)
		}
	}
	// A nonsense batch size must not produce an infinite loop or a zero-size batch.
	if got := Batch(mk(5), 0); len(got) != 1 {
		t.Fatalf("zero batch size gave %d batches", len(got))
	}
}

func TestParseYear(t *testing.T) {
	cases := map[string]int{
		"2013": 2013, "2013-08-16": 2013, "2013-08": 2013,
		"": 0, "nope": 0, "20": 0, "0001": 0, "9999": 0,
	}
	for in, want := range cases {
		if got := parseYear(in); got != want {
			t.Errorf("parseYear(%q) = %d, want %d", in, got, want)
		}
	}
	// Falls through to the second value when the first is unusable.
	if got := parseYear("", "1997"); got != 1997 {
		t.Errorf("fallback failed: %d", got)
	}
}

func TestSampleAcrossSpreads(t *testing.T) {
	items := make([]string, 100)
	for i := range items {
		items[i] = string(rune('a' + i/10))
	}
	got := SampleAcross(items, 5)
	distinct := map[string]bool{}
	for _, g := range got {
		distinct[g] = true
	}
	if len(distinct) < 4 {
		t.Fatalf("sample %v covers %d runs; it is taking the head", got, len(distinct))
	}
	if len(SampleAcross(items, 0)) != 0 || len(SampleAcross([]string{}, 5)) != 0 {
		t.Fatal("edge cases returned items")
	}
	if got := SampleAcross([]string{"a", "b"}, 9); len(got) != 2 {
		t.Fatalf("got %v, want both", got)
	}
}

// ListFiles must not open files: enumerating a whole library inside Navidrome's
// 30-second invocation ceiling depends on it being a directory walk only.
func TestListFilesFindsPathsWithoutParsing(t *testing.T) {
	dir := t.TempDir()
	raw, _ := os.ReadFile("../../testdata/test.flac")
	if err := os.WriteFile(filepath.Join(dir, "good.flac"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// A file that would FAIL to parse must still be listed - proving no parsing
	// happened during enumeration.
	if err := os.WriteFile(filepath.Join(dir, "broken.flac"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "song.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, unsupported, err := ListFiles(os.DirFS(dir), "/m")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %v, want both flacs including the broken one", files)
	}
	paths := Paths(files)
	if paths[0] != "/m/broken.flac" || paths[1] != "/m/good.flac" {
		t.Fatalf("paths not sorted or not rooted: %v", paths)
	}
	if len(unsupported) != 1 {
		t.Fatalf("unsupported = %v, want the mp3", unsupported)
	}
	// Auto-sync decides what to open from these, so a listing without them makes
	// every file look untouched since the beginning of time.
	for _, f := range files {
		if f.ModTime.IsZero() {
			t.Errorf("%s was listed with no modification time", f.Path)
		}
	}
}

// The mtime is the signal auto-sync works from, so it has to be the file's own
// and not the time of the walk.
func TestListFilesReportsEachFilesOwnModTime(t *testing.T) {
	dir := t.TempDir()
	raw, _ := os.ReadFile("../../testdata/test.flac")
	for _, name := range []string{"old.flac", "new.flac"} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Date(2020, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "old.flac"), old, old); err != nil {
		t.Fatal(err)
	}

	files, _, err := ListFiles(os.DirFS(dir), "/m")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]File{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	if got := byPath["/m/old.flac"].ModTime.UTC(); !got.Equal(old) {
		t.Fatalf("old.flac mtime = %v, want %v", got, old)
	}
	if !byPath["/m/new.flac"].ModTime.After(old) {
		t.Fatalf("new.flac mtime = %v, want something after %v",
			byPath["/m/new.flac"].ModTime, old)
	}
}

// ReadTracks is the per-batch half: it maps absolute paths back into fsys and
// reports failures without dropping the rest of the batch.
func TestReadTracksHandlesAMixedBatch(t *testing.T) {
	dir := t.TempDir()
	raw, _ := os.ReadFile("../../testdata/MultipleArtists.flac")
	os.WriteFile(filepath.Join(dir, "good.flac"), raw, 0o644)
	os.WriteFile(filepath.Join(dir, "broken.flac"), []byte("nope"), 0o644)

	tracks, failed := ReadTracks(os.DirFS(dir), "/m",
		[]string{"/m/good.flac", "/m/broken.flac", "/m/missing.flac"})

	if len(tracks) != 1 || tracks[0].Meta.Title != "Test Track" {
		t.Fatalf("tracks = %+v", tracks)
	}
	if len(failed) != 2 {
		t.Fatalf("failed = %v, want the broken and the missing one", failed)
	}
}
