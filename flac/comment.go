package flac

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Two traps in this block type, both of which produce plausible-looking garbage
// rather than an error if you get them wrong:
//
//  1. VORBIS_COMMENT is LITTLE-endian, while every other length in FLAC is
//     big-endian. Mixing them up yields absurd lengths on real files.
//  2. FLAC's VORBIS_COMMENT has NO framing bit. Ogg Vorbis appends one; copying an
//     Ogg implementation adds a stray trailing byte that some parsers tolerate and
//     others reject.

var (
	ErrBadComment    = errors.New("flac: malformed VORBIS_COMMENT block")
	ErrInvalidKey    = errors.New("flac: invalid comment key")
	ErrNoCommentBlk  = errors.New("flac: file has no VORBIS_COMMENT block")
	ErrCommentTooBig = errors.New("flac: comment block exceeds 24-bit length field")
)

// Field is a single "KEY=value" entry. Keys are case-insensitive per the spec and
// may repeat - repetition is exactly how a multi-valued tag is expressed, which is
// what makes multiple moods on one track possible.
type Field struct {
	Key   string
	Value string
}

// Comments is a decoded VORBIS_COMMENT block. Order is preserved so that editing
// one tag does not reshuffle the rest of the file.
type Comments struct {
	Vendor string
	Fields []Field
}

// DecodeComments parses VORBIS_COMMENT block data.
func DecodeComments(data []byte) (*Comments, error) {
	p := 0
	readU32 := func() (uint32, error) {
		if p+4 > len(data) {
			return 0, ErrBadComment
		}
		v := binary.LittleEndian.Uint32(data[p : p+4])
		p += 4
		return v, nil
	}

	vlen, err := readU32()
	if err != nil {
		return nil, err
	}
	if p+int(vlen) > len(data) {
		return nil, ErrBadComment
	}
	c := &Comments{Vendor: string(data[p : p+int(vlen)])}
	p += int(vlen)

	n, err := readU32()
	if err != nil {
		return nil, err
	}
	// Guard against a corrupt count claiming billions of entries: each entry needs
	// at least 4 bytes, so anything beyond the remaining length is malformed.
	if int(n) > (len(data)-p)/4 {
		return nil, ErrBadComment
	}

	c.Fields = make([]Field, 0, n)
	for i := uint32(0); i < n; i++ {
		l, err := readU32()
		if err != nil {
			return nil, err
		}
		if p+int(l) > len(data) {
			return nil, ErrBadComment
		}
		entry := string(data[p : p+int(l)])
		p += int(l)

		// A comment with no '=' is malformed per spec. Keep it rather than failing
		// the whole file, so one bad tag written by some other tool cannot make a
		// track permanently unreadable - but never emit one ourselves.
		if k, v, ok := strings.Cut(entry, "="); ok {
			c.Fields = append(c.Fields, Field{Key: k, Value: v})
		} else {
			c.Fields = append(c.Fields, Field{Key: entry, Value: ""})
		}
	}
	return c, nil
}

// Encode serialises the block data (without the METADATA_BLOCK_HEADER).
func (c *Comments) Encode() ([]byte, error) {
	size := 4 + len(c.Vendor) + 4
	for _, f := range c.Fields {
		size += 4 + len(f.Key) + 1 + len(f.Value)
	}
	if size > maxBlockLen {
		return nil, fmt.Errorf("%w: %d bytes", ErrCommentTooBig, size)
	}

	out := make([]byte, 0, size)
	var u32 [4]byte

	binary.LittleEndian.PutUint32(u32[:], uint32(len(c.Vendor)))
	out = append(out, u32[:]...)
	out = append(out, c.Vendor...)

	binary.LittleEndian.PutUint32(u32[:], uint32(len(c.Fields)))
	out = append(out, u32[:]...)

	for _, f := range c.Fields {
		if err := validKey(f.Key); err != nil {
			return nil, err
		}
		entry := f.Key + "=" + f.Value
		binary.LittleEndian.PutUint32(u32[:], uint32(len(entry)))
		out = append(out, u32[:]...)
		out = append(out, entry...)
	}
	// No framing bit. See the note at the top of this file.
	return out, nil
}

// validKey enforces the spec's field-name rule: ASCII 0x20..0x7D excluding '='.
func validKey(k string) error {
	if k == "" {
		return fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	for _, r := range k {
		if r < 0x20 || r > 0x7d || r == '=' {
			return fmt.Errorf("%w: %q contains %q", ErrInvalidKey, k, r)
		}
	}
	return nil
}

// Get returns every value for key, in file order. Matching is case-insensitive.
func (c *Comments) Get(key string) []string {
	var out []string
	for _, f := range c.Fields {
		if strings.EqualFold(f.Key, key) {
			out = append(out, f.Value)
		}
	}
	return out
}

// Has reports whether key is present at all, regardless of value.
func (c *Comments) Has(key string) bool {
	for _, f := range c.Fields {
		if strings.EqualFold(f.Key, key) {
			return true
		}
	}
	return false
}

// Remove deletes every field with the given key.
func (c *Comments) Remove(key string) {
	out := c.Fields[:0]
	for _, f := range c.Fields {
		if !strings.EqualFold(f.Key, key) {
			out = append(out, f)
		}
	}
	c.Fields = out
}

// Set replaces all values for key with the given ones, writing one repeated field
// per value. Setting zero values is equivalent to Remove.
//
// Replacement happens at the position of the first existing occurrence so that
// rewriting a tag does not move it to the end of the block.
func (c *Comments) Set(key string, values ...string) {
	at := -1
	for i, f := range c.Fields {
		if strings.EqualFold(f.Key, key) {
			at = i
			break
		}
	}
	c.Remove(key)
	if len(values) == 0 {
		return
	}
	ins := make([]Field, 0, len(values))
	for _, v := range values {
		ins = append(ins, Field{Key: key, Value: v})
	}
	if at < 0 || at > len(c.Fields) {
		c.Fields = append(c.Fields, ins...)
		return
	}
	c.Fields = append(c.Fields[:at], append(ins, c.Fields[at:]...)...)
}

// Comments decodes the file's VORBIS_COMMENT block.
func (f *File) Comments() (*Comments, error) {
	i := f.find(VorbisComment)
	if i < 0 {
		return nil, ErrNoCommentBlk
	}
	return DecodeComments(f.Blocks[i].Data)
}

// SetComments encodes c into the file's VORBIS_COMMENT block, creating the block
// immediately after STREAMINFO if the file has none.
func (f *File) SetComments(c *Comments) error {
	data, err := c.Encode()
	if err != nil {
		return err
	}
	if i := f.find(VorbisComment); i >= 0 {
		f.Blocks[i].Data = data
		return nil
	}
	// Insert after STREAMINFO. Last-block flags are recomputed on write, so
	// position is the only thing that matters here.
	blk := Block{Type: VorbisComment, Data: data}
	f.Blocks = append(f.Blocks[:1], append([]Block{blk}, f.Blocks[1:]...)...)
	return nil
}
