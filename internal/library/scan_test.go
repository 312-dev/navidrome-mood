package library

import (
	"os"
	"path/filepath"
	"testing"
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

func TestPendingRespectsExistingTags(t *testing.T) {
	res := scanTestdata(t)
	all := res.Pending(false)
	if len(all) != len(res.Tracks) {
		t.Fatalf("skipTagged=false returned %d of %d", len(all), len(res.Tracks))
	}

	// None of the fixtures carry MOOD, so skipping changes nothing yet.
	if got := len(res.Pending(true)); got != len(res.Tracks) {
		t.Fatalf("pending = %d, want %d (no fixture has MOOD)", got, len(res.Tracks))
	}

	// Mark one as tagged and confirm it drops out.
	res.Tracks[0].HasMood = true
	if got := len(res.Pending(true)); got != len(res.Tracks)-1 {
		t.Fatalf("pending = %d, want %d", got, len(res.Tracks)-1)
	}
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

	paths, unsupported, err := ListFiles(os.DirFS(dir), "/m")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want both flacs including the broken one", paths)
	}
	if paths[0] != "/m/broken.flac" || paths[1] != "/m/good.flac" {
		t.Fatalf("paths not sorted or not rooted: %v", paths)
	}
	if len(unsupported) != 1 {
		t.Fatalf("unsupported = %v, want the mp3", unsupported)
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
