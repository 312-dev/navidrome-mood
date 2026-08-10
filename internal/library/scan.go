// Package library enumerates taggable tracks and reads their metadata straight
// from the files.
//
// Enumeration goes through the filesystem rather than the Subsonic API. Reading
// the files directly means the paths are correct by construction, since they come
// from the same mount the tagger writes through, and the metadata is whatever is
// actually in the file rather than whatever Navidrome last indexed. No API
// credentials are involved, which is why the plugin declares no subsonicapi
// permission at all.
package library

import (
	"errors"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/312-dev/navidrome-mood/flac"
	"github.com/312-dev/navidrome-mood/internal/llm"
	"github.com/312-dev/navidrome-mood/internal/runner"
)

// Track is one enumerated file: the metadata to label it with, where it is, and
// what it already carries.
type Track struct {
	Path string
	Meta llm.Track
	// Tags holds the plugin's own ten tag names as this file currently carries
	// them, omitting the ones it does not. They come out of the same comment
	// block the metadata above is parsed from, so reading them costs nothing
	// beyond the open that had to happen anyway - which is what lets the files be
	// the record of what has already been labelled.
	Tags map[string][]string
}

// File is one taggable path and when it was last modified.
//
// The modification time is what auto-sync works from. Navidrome exposes no
// scan-completed hook, so the only way to find music that arrived since the last
// check is to compare mtimes against a watermark, and stat is cheap where
// opening every file to read its tags is not.
type File struct {
	Path    string
	ModTime time.Time
}

// Unsupported records a file that cannot be tagged, so the counts reconcile.
//
// Silently skipping is how a gap of 2 today becomes an unexplained gap of 200
// after someone adds ALAC. Report the format and move on.
type Unsupported struct {
	Path   string
	Reason string
}

// Result is one enumeration pass.
type Result struct {
	Tracks      []Track
	Unsupported []Unsupported
	// Failed are files that look taggable but could not be parsed. Distinct from
	// Unsupported: these are FLACs that should have worked.
	Failed map[string]string
}

// taggable is the set of extensions this plugin can write. FLAC only, for now,
// because the writer is FLAC-only and pretending otherwise would mean writing
// tags nothing can read.
var taggable = map[string]bool{".flac": true}

// audioExts is everything we recognise as music, used to distinguish "a file we
// cannot tag" from "a cover image sitting in the folder".
var audioExts = map[string]bool{
	".flac": true, ".mp3": true, ".m4a": true, ".alac": true, ".ogg": true,
	".opus": true, ".wav": true, ".aiff": true, ".wma": true, ".ape": true,
	".wv": true, ".mpc": true, ".dsf": true,
}

// ListFiles walks fsys and returns taggable paths WITHOUT opening any of them.
//
// This split exists because of Navidrome's 30-second per-invocation ceiling.
// Reading metadata from ~9,200 files takes well over a minute, so enumeration
// cannot read tags: it lists paths and modification times (fast, one directory
// walk), and each task reads metadata only for its own batch. Doing it the
// obvious way would time out during startup and the plugin would never enqueue
// anything.
//
// A file whose directory entry cannot be stat'ed is still listed, with a zero
// ModTime. It is almost always one that was removed between the readdir and the
// stat, and listing it means a whole-library pass still reports it rather than
// dropping it silently; auto-sync leaves it alone, since a zero time is older
// than any watermark.
//
// Unsupported audio is still reported here, since that only needs the extension.
func ListFiles(fsys fs.FS, root string) ([]File, []Unsupported, error) {
	var files []File
	var unsupported []Unsupported

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(path.Ext(p))
		full := path.Join(root, p)
		switch {
		case taggable[ext]:
			f := File{Path: full}
			if info, ierr := d.Info(); ierr == nil {
				f.ModTime = info.ModTime()
			}
			files = append(files, f)
		case audioExts[ext]:
			unsupported = append(unsupported, Unsupported{
				Path:   full,
				Reason: "only FLAC can be tagged; " + ext + " is not supported",
			})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Slice(unsupported, func(i, j int) bool { return unsupported[i].Path < unsupported[j].Path })
	return files, unsupported, nil
}

// Paths pulls the paths out of a listing, for the callers that only enqueue
// work and never look at a modification time.
func Paths(files []File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

// ReadTracks reads metadata for specific paths, which is what a task does for its
// own batch. Paths are absolute (as returned by ListFiles); fsys is rooted at
// root so they can be mapped back.
func ReadTracks(fsys fs.FS, root string, paths []string) ([]Track, map[string]string) {
	var out []Track
	failed := map[string]string{}
	for _, full := range paths {
		rel := strings.TrimPrefix(strings.TrimPrefix(full, root), "/")
		t, err := readTrack(fsys, rel, full)
		if err != nil {
			failed[full] = err.Error()
			continue
		}
		out = append(out, *t)
	}
	return out, failed
}

// Scan walks fsys and reads metadata from every taggable file.
//
// Only suitable for small trees - see ListFiles for why a whole library cannot be
// scanned inside one plugin invocation.
//
// root is prefixed onto the returned paths so callers get something openable;
// fsys is rooted at that same directory. Splitting them keeps the walk testable
// against a plain os.DirFS over testdata.
func Scan(fsys fs.FS, root string) (*Result, error) {
	res := &Result{Failed: map[string]string{}}

	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree should not abort the whole library.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(path.Ext(p))
		full := path.Join(root, p)

		if !taggable[ext] {
			if audioExts[ext] {
				res.Unsupported = append(res.Unsupported, Unsupported{
					Path:   full,
					Reason: "only FLAC can be tagged; " + ext + " is not supported",
				})
			}
			return nil
		}

		t, rerr := readTrack(fsys, p, full)
		if rerr != nil {
			res.Failed[full] = rerr.Error()
			return nil
		}
		res.Tracks = append(res.Tracks, *t)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Deterministic order, so a resumed run enumerates identically and batches
	// line up with what was already done.
	sort.Slice(res.Tracks, func(i, j int) bool { return res.Tracks[i].Path < res.Tracks[j].Path })
	sort.Slice(res.Unsupported, func(i, j int) bool { return res.Unsupported[i].Path < res.Unsupported[j].Path })
	return res, nil
}

func readTrack(fsys fs.FS, p, full string) (*Track, error) {
	fh, err := fsys.Open(p)
	if err != nil {
		return nil, err
	}
	defer fh.Close()

	f, err := flac.Parse(fh)
	if err != nil {
		return nil, err
	}

	t := &Track{Path: full, Meta: llm.Track{ID: full}}
	c, err := f.Comments()
	if err != nil {
		if errors.Is(err, flac.ErrNoCommentBlk) {
			// No tags at all. Still labelable from the filename alone, which is
			// better than skipping it.
			t.Meta.Title = strings.TrimSuffix(path.Base(full), path.Ext(full))
			return t, nil
		}
		return nil, err
	}

	t.Meta.Title = first(c, "TITLE")
	t.Meta.Artist = first(c, "ARTIST")
	t.Meta.Album = first(c, "ALBUM")
	t.Meta.Genres = c.Get("GENRE")
	t.Meta.Year = parseYear(first(c, "DATE"), first(c, "YEAR"))

	// The plugin's own tags, read from the block already in hand.
	// runner.ReadableTagNames is the single list of them, shared with the writer
	// so that a rename cannot leave the reader looking for a name nothing writes.
	// It includes the names an older version used, because a file carrying only
	// those is fully labelled and reading it as blank would send a finished
	// library back through paid labelling. Comments.Get matches
	// case-insensitively, so the lower-case contract names find the upper-case
	// fields every tagger actually writes.
	for _, name := range runner.ReadableTagNames() {
		if v := c.Get(name); len(v) > 0 {
			if t.Tags == nil {
				t.Tags = make(map[string][]string, len(runner.TagNames))
			}
			t.Tags[name] = v
		}
	}

	if t.Meta.Title == "" {
		t.Meta.Title = strings.TrimSuffix(path.Base(full), path.Ext(full))
	}
	return t, nil
}

func first(c *flac.Comments, key string) string {
	if v := c.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// parseYear pulls a year out of whatever the tagger wrote. DATE is commonly
// "2013", "2013-08-16" or "2013-08", and YEAR is a fallback some tools use.
func parseYear(values ...string) int {
	for _, v := range values {
		if len(v) < 4 {
			continue
		}
		if y, err := strconv.Atoi(v[:4]); err == nil && y > 1500 && y < 2200 {
			return y
		}
	}
	return 0
}

// Batch splits tracks into groups of n.
//
// Batch size is bounded by Navidrome's 30-second per-invocation ceiling, not by
// provider limits: one task must finish a whole request inside that window with
// the entire response in hand, because the host cannot stream.
func Batch(tracks []Track, n int) [][]Track {
	if n <= 0 {
		n = 20
	}
	var out [][]Track
	for i := 0; i < len(tracks); i += n {
		end := i + n
		if end > len(tracks) {
			end = len(tracks)
		}
		out = append(out, tracks[i:end])
	}
	return out
}

// SampleAcross picks n items spread evenly through a slice rather than taking the
// head.
//
// Written because the mount self-test took the alphabetically first file and I
// then generalised its 45 bytes of padding to the whole library - which turned
// out to be one of only three such files in 9,195. A sample of one is a sample of
// one even when it is convenient.
func SampleAcross[T any](items []T, n int) []T {
	if n <= 0 || len(items) == 0 {
		return nil
	}
	if len(items) <= n {
		out := make([]T, len(items))
		copy(out, items)
		return out
	}
	out := make([]T, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, items[i*len(items)/n])
	}
	return out
}
