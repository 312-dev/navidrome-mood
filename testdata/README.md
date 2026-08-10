# Test fixtures

`test.flac` and `mixed-lyrics.flac` are copied from
[navidrome/navidrome](https://github.com/navidrome/navidrome)'s own
`tests/fixtures/`, which is licensed GPL-3.0. They are here so this plugin's
reader is tested against exactly the files Navidrome tests its own reader
against: a tag this plugin parses differently from Navidrome is a bug, and
sharing the fixtures is what makes that visible.

Each holds a tenth of a second of audio and exists for its Vorbis comment block,
not its sound. `mixed-lyrics.flac` carries the full metadata of a real release,
including two lines of lyrics, which is the point: it exercises multi-valued
fields, an unsynced lyrics block and tags with spaces in the name.

`MultipleArtists.flac` is generated, not copied.
