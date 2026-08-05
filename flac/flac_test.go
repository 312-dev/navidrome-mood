package flac

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func fixtures(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("../testdata/*.flac")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	return paths
}

func parseFile(t *testing.T, path string) (*File, []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	f, err := Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f, raw
}

// The metadata chain must survive a parse/write cycle byte-for-byte. If this
// fails, every other guarantee is void.
func TestMetadataRoundTripIsByteIdentical(t *testing.T) {
	for _, path := range fixtures(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, raw := parseFile(t, path)

			var got bytes.Buffer
			if err := f.WriteMetadata(&got); err != nil {
				t.Fatalf("WriteMetadata: %v", err)
			}
			want := raw[:f.AudioOffset]
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("metadata region differs: got %d bytes, want %d",
					got.Len(), len(want))
			}
			if int64(got.Len()) != int64(len(Magic))+f.MetadataLen() {
				t.Fatalf("MetadataLen disagrees with WriteMetadata output")
			}
		})
	}
}

// Encode(Decode(x)) == x proves the comment codec is faithful, including the
// little-endian lengths and the absence of a framing bit.
func TestCommentCodecIsLossless(t *testing.T) {
	for _, path := range fixtures(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, _ := parseFile(t, path)
			i := f.find(VorbisComment)
			if i < 0 {
				t.Skip("fixture has no VORBIS_COMMENT")
			}
			orig := f.Blocks[i].Data

			c, err := DecodeComments(orig)
			if err != nil {
				t.Fatalf("DecodeComments: %v", err)
			}
			got, err := c.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if !bytes.Equal(got, orig) {
				t.Fatalf("re-encoded comment block differs: got %d bytes, want %d",
					len(got), len(orig))
			}
		})
	}
}

// Report what the fixtures actually contain, so a failure elsewhere is easy to
// interpret. Not an assertion.
func TestDescribeFixtures(t *testing.T) {
	for _, path := range fixtures(t) {
		f, raw := parseFile(t, path)
		t.Logf("%s: %d bytes, audio at %d", filepath.Base(path), len(raw), f.AudioOffset)
		for _, b := range f.Blocks {
			t.Logf("    %-14s %6d bytes", b.Type, len(b.Data))
		}
		if c, err := f.Comments(); err == nil {
			t.Logf("    vendor: %q", c.Vendor)
			for _, fl := range c.Fields {
				v := fl.Value
				if len(v) > 60 {
					v = v[:60] + "..."
				}
				t.Logf("    tag %s=%s", fl.Key, v)
			}
		}
	}
}

// Repeated keys are how a multi-valued tag is expressed - verified against a live
// Navidrome on 2026-08-05, where three repeated MOOD fields registered as three
// distinct mood values.
func TestRepeatedKeysAreMultipleValues(t *testing.T) {
	c := &Comments{Vendor: "test"}
	c.Set("MOOD", "wistful", "hushed", "nocturnal")

	if got := c.Get("MOOD"); len(got) != 3 {
		t.Fatalf("Get returned %d values, want 3: %v", len(got), got)
	}
	// Case-insensitive matching, per spec.
	if got := c.Get("mood"); len(got) != 3 {
		t.Fatalf("lowercase lookup returned %d values, want 3", len(got))
	}

	data, err := c.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := DecodeComments(data)
	if err != nil {
		t.Fatalf("DecodeComments: %v", err)
	}
	if got := back.Get("MOOD"); len(got) != 3 || got[0] != "wistful" || got[2] != "nocturnal" {
		t.Fatalf("round trip lost values: %v", got)
	}
}

// Set must replace in place, not move the tag to the end - otherwise every write
// reshuffles the block and diffs become unreadable.
func TestSetReplacesInPlace(t *testing.T) {
	c := &Comments{Vendor: "test"}
	c.Fields = []Field{
		{"TITLE", "a"}, {"MOOD", "old"}, {"ARTIST", "b"},
	}
	c.Set("MOOD", "new1", "new2")

	want := []Field{
		{"TITLE", "a"}, {"MOOD", "new1"}, {"MOOD", "new2"}, {"ARTIST", "b"},
	}
	if len(c.Fields) != len(want) {
		t.Fatalf("got %d fields, want %d: %v", len(c.Fields), len(want), c.Fields)
	}
	for i := range want {
		if c.Fields[i] != want[i] {
			t.Fatalf("field %d = %v, want %v", i, c.Fields[i], want[i])
		}
	}
}

func TestSetWithNoValuesRemoves(t *testing.T) {
	c := &Comments{Vendor: "test"}
	c.Fields = []Field{{"TITLE", "a"}, {"MOOD", "old"}}
	c.Set("MOOD")
	if c.Has("MOOD") {
		t.Fatalf("MOOD survived: %v", c.Fields)
	}
	if len(c.Fields) != 1 {
		t.Fatalf("other fields disturbed: %v", c.Fields)
	}
}

// The enum vocabulary must never contain these, because Navidrome splits mood
// values on them (verified 2026-08-05: "up-tempo, driving" became two moods).
// A key containing '=' is separately illegal per spec.
func TestRejectsInvalidKeys(t *testing.T) {
	for _, k := range []string{"", "MO=OD", "MO\x00OD", "MOOD\n"} {
		c := &Comments{Vendor: "test", Fields: []Field{{k, "x"}}}
		if _, err := c.Encode(); err == nil {
			t.Fatalf("key %q was accepted, want rejection", k)
		}
	}
}

func TestRejectsNonFLAC(t *testing.T) {
	for name, in := range map[string][]byte{
		"empty":       {},
		"short":       []byte("fLa"),
		"wrong magic": []byte("OggS____________"),
	} {
		if _, err := Parse(bytes.NewReader(in)); err == nil {
			t.Fatalf("%s: parsed successfully, want error", name)
		}
	}
}
