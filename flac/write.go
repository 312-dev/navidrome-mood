package flac

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// DefaultPadding is inserted when a full rewrite happens on a file that had no
// PADDING block. Without it, every future tag edit would need another full
// rewrite; with it, subsequent edits are a few-hundred-byte seek-and-write.
const DefaultPadding = 8192

// Strategy records how UpdateTags wrote a file. Exposed for tests and logging -
// on a healthy library nearly every write should be InPlace.
type Strategy string

const (
	// InPlace overwrote only the metadata region. Audio bytes were never read or
	// written and the file size is unchanged.
	InPlace Strategy = "in-place"
	// FullRewrite rebuilt the file via a temp file and an atomic rename, because
	// the new metadata could not be made to fit the old region.
	FullRewrite Strategy = "full-rewrite"
	// Unchanged means the edit produced identical bytes, so nothing was written.
	// Notably this leaves mtime alone, which means Navidrome will not rescan -
	// correct, because there is nothing to rescan.
	Unchanged Strategy = "unchanged"
)

var ErrStreamInfoChanged = errors.New("flac: refusing to write, STREAMINFO changed")

// UpdateTags applies edit to the file's Vorbis comments and writes the result.
//
// It never decodes, re-encodes, or moves audio data. STREAMINFO - which carries
// the MD5 of the decoded audio - is compared before and after and the write is
// refused if it differs, so a bug in the block layer cannot silently invalidate
// every checksum in a library.
//
// mtime is deliberately allowed to change. Navidrome's folder hash is over name +
// size + mtime, and an in-place tag edit does not change size, so mtime is the
// only signal that a rescan is needed. Preserving it would make edits invisible.
func UpdateTags(path string, edit func(*Comments) error) (Strategy, error) {
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer src.Close()

	f, err := Parse(src)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}

	beforeSI := snapshotStreamInfo(f)
	oldRegion, err := f.metadataBytes()
	if err != nil {
		return "", err
	}

	c, err := f.Comments()
	if errors.Is(err, ErrNoCommentBlk) {
		c = &Comments{Vendor: "navidrome-mood"}
	} else if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}

	if err := edit(c); err != nil {
		return "", err
	}
	if err := f.SetComments(c); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}

	if !bytes.Equal(beforeSI, f.streamInfo()) {
		return "", fmt.Errorf("%s: %w", path, ErrStreamInfoChanged)
	}

	// Try to keep the metadata region exactly the same length by absorbing the
	// difference into PADDING. This is the common case: fixtures carry 1-8 KB of
	// padding and a mood tag costs tens of bytes.
	if f.fitPadding(int64(len(oldRegion))) {
		newRegion, err := f.metadataBytes()
		if err != nil {
			return "", err
		}
		if bytes.Equal(newRegion, oldRegion) {
			return Unchanged, nil
		}
		if int64(len(newRegion)) != int64(len(oldRegion)) {
			return "", fmt.Errorf("%s: internal error: fitPadding returned wrong size", path)
		}
		src.Close()
		if err := writeInPlace(path, newRegion); err != nil {
			return "", err
		}
		return InPlace, nil
	}

	if _, err := src.Seek(f.AudioOffset, io.SeekStart); err != nil {
		return "", err
	}
	if err := f.rewrite(path, src); err != nil {
		return "", err
	}
	return FullRewrite, nil
}

// snapshotStreamInfo copies the STREAMINFO block for later comparison.
//
// The copy is the entire point. streamInfo() returns a slice that aliases the
// block's backing array, so a snapshot taken without copying would mutate along
// with the block and compare equal to itself no matter what changed - making the
// guard in UpdateTags a silent no-op. That bug shipped and survived every test
// until mutation testing on 2026-08-05 refused to fail.
// See TestStreamInfoSnapshotDoesNotAlias.
func snapshotStreamInfo(f *File) []byte {
	return append([]byte(nil), f.streamInfo()...)
}

// streamInfo returns the STREAMINFO block data, or nil.
func (f *File) streamInfo() []byte {
	if i := f.find(StreamInfo); i >= 0 {
		return f.Blocks[i].Data
	}
	return nil
}

func (f *File) metadataBytes() ([]byte, error) {
	var buf bytes.Buffer
	if err := f.WriteMetadata(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// fitPadding adjusts (or inserts) a PADDING block so the metadata region is
// exactly want bytes. Reports whether it succeeded.
func (f *File) fitPadding(want int64) bool {
	cur := int64(len(Magic)) + f.MetadataLen()
	if cur == want {
		return true
	}

	i := f.find(Padding)
	if i < 0 {
		// No padding to absorb the change. Inserting one costs its own 4-byte
		// header, so it only helps when we need to grow the region by >= 4.
		grow := want - cur
		if grow < headerLen {
			return false
		}
		f.Blocks = append(f.Blocks, Block{
			Type: Padding,
			Data: make([]byte, grow-headerLen),
		})
		return true
	}

	// delta > 0 means the region is currently too small and padding must grow.
	delta := want - cur
	size := int64(len(f.Blocks[i].Data)) + delta
	if size < 0 || size > maxBlockLen {
		return false
	}
	f.Blocks[i].Data = make([]byte, size)
	return true
}

// writeInPlace overwrites the first len(region) bytes of path, leaving the rest of
// the file - all of the audio - untouched. The file is not truncated and its size
// does not change.
func writeInPlace(path string, region []byte) error {
	fh, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer fh.Close()

	if _, err := fh.WriteAt(region, 0); err != nil {
		return err
	}
	// Durability matters here: a torn metadata region on a power loss would leave
	// an unplayable file, and this runs across a whole library.
	if err := fh.Sync(); err != nil {
		return err
	}
	return fh.Close()
}

// rewrite builds a new file from the current blocks plus audio read from r, then
// atomically replaces path. r must already be positioned at the first audio frame.
func (f *File) rewrite(path string, r io.Reader) error {
	// If the file had no padding, give it some so the next edit can be in-place.
	if f.find(Padding) < 0 {
		f.Blocks = append(f.Blocks, Block{Type: Padding, Data: make([]byte, DefaultPadding)})
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".navidrome-mood-*.flac")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Any failure past this point must not leave a stray temp file behind.
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	if err := f.WriteMetadata(tmp); err != nil {
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// Carry over the original permissions; CreateTemp makes 0600.
	if st, err := os.Stat(path); err == nil {
		if err := os.Chmod(tmpName, st.Mode().Perm()); err != nil {
			return err
		}
	}
	return os.Rename(tmpName, path)
}
