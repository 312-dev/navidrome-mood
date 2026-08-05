// Package flac reads and rewrites FLAC metadata blocks.
//
// The scope is deliberately narrow: parse the metadata block chain, edit the
// VORBIS_COMMENT block, and write the file back without touching a single byte of
// encoded audio. It does not decode audio and never will - that is what makes it
// safe to point at an irreplaceable library.
//
// Format reference: https://xiph.org/flac/format.html
package flac

import (
	"errors"
	"fmt"
	"io"
)

// Magic is the 4-byte stream marker every FLAC file starts with.
var Magic = []byte("fLaC")

// BlockType identifies a METADATA_BLOCK. Values are from the FLAC spec.
type BlockType uint8

const (
	StreamInfo    BlockType = 0
	Padding       BlockType = 1
	Application   BlockType = 2
	SeekTable     BlockType = 3
	VorbisComment BlockType = 4
	CueSheet      BlockType = 5
	Picture       BlockType = 6
	// 7..126 are reserved; 127 is invalid.
)

func (t BlockType) String() string {
	switch t {
	case StreamInfo:
		return "STREAMINFO"
	case Padding:
		return "PADDING"
	case Application:
		return "APPLICATION"
	case SeekTable:
		return "SEEKTABLE"
	case VorbisComment:
		return "VORBIS_COMMENT"
	case CueSheet:
		return "CUESHEET"
	case Picture:
		return "PICTURE"
	}
	return fmt.Sprintf("RESERVED(%d)", uint8(t))
}

// maxBlockLen is the largest value the 24-bit length field can hold.
const maxBlockLen = 1<<24 - 1

// headerLen is the fixed size of a METADATA_BLOCK_HEADER.
const headerLen = 4

// Block is one METADATA_BLOCK. Last is not stored per-block when writing - it is
// recomputed from position - but it is recorded on parse so a malformed chain can
// be reported accurately.
type Block struct {
	Type BlockType
	Last bool
	Data []byte
}

// File is a parsed FLAC file: its metadata chain plus the byte offset at which
// encoded audio begins. Audio is never read into memory.
type File struct {
	Blocks []Block

	// AudioOffset is the offset of the first audio frame in the source file,
	// i.e. len(Magic) + total size of every metadata block including headers.
	AudioOffset int64
}

var (
	ErrNotFLAC       = errors.New("flac: missing fLaC stream marker")
	ErrNoStreamInfo  = errors.New("flac: first metadata block must be STREAMINFO")
	ErrBlockTooLarge = errors.New("flac: metadata block exceeds 24-bit length field")
	ErrTruncated     = errors.New("flac: file ended inside the metadata chain")
)

// Parse reads the stream marker and the full metadata block chain from r, stopping
// at the first audio frame. r is left positioned at the start of the audio.
func Parse(r io.Reader) (*File, error) {
	var marker [4]byte
	if _, err := io.ReadFull(r, marker[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrNotFLAC
		}
		return nil, err
	}
	if string(marker[:]) != string(Magic) {
		return nil, ErrNotFLAC
	}

	f := &File{AudioOffset: int64(len(Magic))}

	for {
		var hdr [headerLen]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, ErrTruncated
			}
			return nil, err
		}

		// Byte 0: bit 7 is the last-block flag, bits 6..0 are the type.
		// Bytes 1..3: 24-bit big-endian length of the block data.
		last := hdr[0]&0x80 != 0
		typ := BlockType(hdr[0] & 0x7f)
		length := int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])

		if len(f.Blocks) == 0 && typ != StreamInfo {
			return nil, ErrNoStreamInfo
		}

		data := make([]byte, length)
		if _, err := io.ReadFull(r, data); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, ErrTruncated
			}
			return nil, err
		}

		f.Blocks = append(f.Blocks, Block{Type: typ, Last: last, Data: data})
		f.AudioOffset += int64(headerLen + length)

		if last {
			return f, nil
		}
	}
}

// MetadataLen is the total on-disk size of the metadata region that WriteMetadata
// would emit, excluding the 4-byte stream marker.
func (f *File) MetadataLen() int64 {
	var n int64
	for _, b := range f.Blocks {
		n += headerLen + int64(len(b.Data))
	}
	return n
}

// WriteMetadata writes the stream marker followed by every metadata block.
//
// The last-block flag is recomputed from position rather than trusted from the
// parsed value, so reordering or removing blocks cannot produce a chain that runs
// off into the audio.
func (f *File) WriteMetadata(w io.Writer) error {
	if len(f.Blocks) == 0 {
		return ErrNoStreamInfo
	}
	if f.Blocks[0].Type != StreamInfo {
		return ErrNoStreamInfo
	}
	if _, err := w.Write(Magic); err != nil {
		return err
	}
	for i, b := range f.Blocks {
		if len(b.Data) > maxBlockLen {
			return fmt.Errorf("%w: %s is %d bytes", ErrBlockTooLarge, b.Type, len(b.Data))
		}
		var hdr [headerLen]byte
		hdr[0] = byte(b.Type) & 0x7f
		if i == len(f.Blocks)-1 {
			hdr[0] |= 0x80
		}
		n := len(b.Data)
		hdr[1] = byte(n >> 16)
		hdr[2] = byte(n >> 8)
		hdr[3] = byte(n)
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := w.Write(b.Data); err != nil {
			return err
		}
	}
	return nil
}

// find returns the index of the first block of type t, or -1.
func (f *File) find(t BlockType) int {
	for i, b := range f.Blocks {
		if b.Type == t {
			return i
		}
	}
	return -1
}
