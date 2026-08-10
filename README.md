# navidrome-mood

A Navidrome plugin that listens to your library with an LLM and writes what it
hears back into the audio files as tags.

Every track gets a position in a five-axis mood space, a coarse tempo feel, a
note of whether anyone is singing, two to four mood words from a fixed
vocabulary, the times of day it suits, and the named regions of mood space it
falls into. Ten tags in total. Once they are in the files, Navidrome's own smart
playlists can filter on them, and anything reading the same library through
Navidrome sees them too. Nothing else has to be installed and nothing has to
stay running.

The tags are the product. This plugin does not create playlists, does not run a
server, and does not keep a database you have to back up. If you uninstall it,
the labels stay in your music.

## Before you start

- **FLAC only.** The tag writer is FLAC-only. Other audio files are enumerated,
  reported as unsupported, and left untouched. Pretending otherwise would mean
  writing tags nothing can read.
- **You need an API key.** Anthropic is the default. Any OpenAI-compatible
  endpoint also works, including OpenRouter, Groq, Together, DeepSeek, and a
  local Ollama or LM Studio.
- **Navidrome needs plugin support turned on** and the mount for your library
  visible to plugins. `navidrome plugin list` tells you whether that is the case.
- **Budget $30 to $55** for 9,000 tracks on the default model, and read
  [What it costs](#what-it-costs) before starting. The two measured runs disagree.

## Install

`make` produces `dist/navidrome-mood.ndp`, which is a ZIP of the manifest and the
compiled WASM. Copy it into Navidrome's plugins folder (`Plugins.Folder`,
which defaults to `<DataFolder>/plugins`) and pick it up:

```bash
make
cp dist/navidrome-mood.ndp /path/to/navidrome/data/plugins/
navidrome plugin rescan
navidrome plugin list
```

Then open the plugin's settings in Navidrome and fill in the provider and API
key. The form is ordered the way the work is: choose a provider, set your spend
limits, then run it.

**Grant write access in the same screen.** Navidrome gates a plugin's ability to
modify your files behind a per-plugin permission, stored as `allow_write_access`
and defaulting to off. Declaring `library: { filesystem: true }` in the manifest
is this plugin asking; that toggle is you agreeing. Without it the labelling runs
and costs money and every write is refused at the boundary.

To check it from the command line rather than the UI:

```bash
sqlite3 -readonly <DataFolder>/navidrome.db \
  "select id, enabled, allow_write_access from plugin;"
```

## Declare the tags, or Navidrome will throw them away

**Do this before running a labelling pass.** Navidrome only stores tags it has
been told about. A tag in a file whose name is not declared is dropped by the
scanner: it never reaches the database and never appears in the API. There is no
wildcard and no catch-all, and no error is logged. The plugin will report that it
wrote ten tags to every track and Navidrome will show you nothing, which looks
exactly like the plugin not working.

Of the ten, only `mood` is built in. The other nine have to be declared in
Navidrome's own config file. In TOML:

```toml
# The five axes. Type = "int" is what makes them comparable as numbers, so that
# "moodenergy above 70" is an arithmetic test rather than a string one.
[Tags.moodenergy]
Aliases = ["moodenergy"]
Type = "int"

[Tags.moodvalence]
Aliases = ["moodvalence"]
Type = "int"

[Tags.moodintensity]
Aliases = ["moodintensity"]
Type = "int"

[Tags.moodacousticness]
Aliases = ["moodacousticness"]
Type = "int"

[Tags.mooddensity]
Aliases = ["mooddensity"]
Type = "int"

# Single-valued. No Split, because these hold exactly one word.
[Tags.moodtempo]
Aliases = ["moodtempo"]

[Tags.moodvocal]
Aliases = ["moodvocal"]

# Multi-valued, split the same way Navidrome already splits `mood`.
[Tags.moodtime]
Aliases = ["moodtime"]
Split = [";", "/", ","]

[Tags.vibe]
Aliases = ["vibe"]
Split = [";", "/", ","]
```

Three things about that block are worth understanding rather than copying:

- **`Aliases` is what actually matches the tag in the file.** For a tag Navidrome
  has never heard of there is no built-in entry to fall back on, so an entry with
  no `Aliases` matches nothing at all and fails silently. This is the mistake to
  watch for.
- **`Type = "int"` is the whole of numeric registration.** Navidrome collects
  every tag typed `int` or `float` on config load and registers them as numeric,
  which is what puts the value through `CAST(value AS REAL)` in a smart-playlist
  comparison. Without it, `moodenergy` sorts and compares as text and `"9"` is
  greater than `"80"`. There is no separate step to perform.
- **Do not declare `mood`.** It already ships with Navidrome, aliased to `tmoo`,
  `mood`, `wm/mood` and the iTunes form, split on `;`, `/` and `,`. Redeclaring
  it would overwrite a working entry with a worse one. That built-in split rule
  is also why no mood word, time slot or region name in this plugin may contain
  a comma, semicolon or slash: the plugin refuses to load if one does.

Restart Navidrome after editing the config.

### Check that it took

This is the step that can actually fail, so run it before spending money on a
full pass. After a sample run and a library rescan, ask Navidrome which tag names
it knows about:

```
GET /api/tag
```

That is the web UI's own API, so the simplest way to call it is from a browser
already logged in to Navidrome. If `moodenergy` and friends are in the list, the
config took. If only `mood` is there, the other nine are being discarded and the
config is wrong or was not reloaded. `GET /api/song/<id>` shows the `tags` object
for one track, which is the same answer one level down.

## The ten tags

| Tag | Value | Multi-valued |
|---|---|---|
| `mood` | 2 to 4 words from a fixed 52-term vocabulary | yes |
| `moodenergy` | 0 to 100, how activated it is | no |
| `moodvalence` | 0 to 100, how positive it sounds | no |
| `moodintensity` | 0 to 100, how forceful | no |
| `moodacousticness` | 0 to 100, acoustic at the top | no |
| `mooddensity` | 0 to 100, how much is going on | no |
| `moodtempo` | `still` `slow` `mid` `driving` `frantic` | no |
| `moodvocal` | `instrumental` `sung` `rapped` `mixed` | no |
| `moodtime` | time-of-day slots it suits | yes |
| `vibe` | named regions of mood space it falls in | yes |

`moodtempo` is how fast the track *feels*, not its BPM. A slow, sparse track at
140 BPM feels `slow`.

The time slots are `early morning`, `morning`, `midday`, `afternoon`,
`golden hour`, `evening` and `late night`. The model chooses them per track.

The vibe regions are `wind down`, `slow morning`, `focus`, `background`,
`uplift`, `workout`, `hype`, `driving`, `golden hour`, `late night`,
`melancholy`, `heavy`, `dinner` and `party`. These are not chosen by the model.
Each region is an area of mood space with a centre, a radius and optional
constraints, and the plugin computes which ones a track's coordinates land in.
A track in no region is a normal outcome, not a failure, and about a third of a
library lands that way. Those tracks are still reachable by axis range and by
mood word.

The radii are fitted so each region holds roughly 8% of a real collection.
That basis matters: real music does not spread evenly through the coordinate
space. Measured over 9,195 labelled tracks, two picked at random sat 17.7 apart
where two points drawn uniformly sat 39.1 apart, so radii tuned against an even
spread were wider than the typical gap between any two tracks in the collection
and `driving` ended up on 45% of the library.

If you change a radius, add a region or rename one, set `Label my library` to
`revibe`. It re-derives the `vibe` tag from the five axes already in your files.
No provider is called and nothing is charged, because the regions are geometry
over coordinates you have already paid for.

## Why there is no "custom mood words" setting

Every one of the 52 vocabulary terms is *defined* by an anchor: a fixed point in
the five-axis space, plus a gloss the labeller is shown. That is what makes the
terms mean the same thing in a jazz collection as in a metal one, and it is what
lets the plugin compute `vibe` at all.

A word you supply has no anchor. It could be written into `mood`, but it would
sit at no coordinates, fall in no region, and be invisible to anything that
reasons about distance. It would not be a smaller version of the vocabulary; it
would be a value that silently does nothing. So the vocabulary is fixed, and the
52 terms live in `internal/mood`. Anything the model returns that does not fold
onto one of them, directly or through one of the 146 synonyms, is dropped rather
than written.

## What it costs

The default provider is Anthropic and the default model is `claude-sonnet-5`.

Two measured figures, and they disagree enough to be worth stating separately.

A 9,311-track pass on `claude-opus-5` cost **$27.22** against a pre-flight
projection of $27.30. A later partial run reached 4,239 tracks for **$24.98**,
which is $0.0059 each and puts a 9,000-track library nearer **$54**.

Take the higher one. The second run is the more recent measurement, and the
prompt has grown since both: it now carries all 52 anchors with their coordinates
and glosses, which is a larger constant on every request. That constant is
identical across batches and therefore cacheable, so the per-track cost should
fall on a long run, but no measurement of that exists yet.

The pre-flight estimate printed before each run is the number to trust over any
figure here, because it prices your actual model against your actual track count.

Two limits, both hard stops rather than estimates:

- **Most this run may cost**, default $20. Spend is computed from the token usage
  each reply reports, not projected, and labelling halts when the limit is hit.
- **Most this plugin may ever cost**, default $100. This one never resets. The
  per-run limit legitimately starts fresh when you change the model or the limit,
  so the lifetime cap is what stops a sequence of runs adding up to a number you
  did not intend.

Other things that stop it spending:

- A pre-flight estimate runs before any work is queued and refuses to start if
  the low estimate already exceeds your run limit.
- If the model has no known price, the run refuses rather than spending against a
  counter stuck at zero.
- Three consecutive batches that cost money and produced no labels halt
  everything and clear the queue. Changing any setting releases the halt.
- If the spend counter cannot be written to storage, everything stops. A limit
  that cannot be saved is not a limit.

**Preview mode costs full price and keeps nothing.** It protects your files, not
your bill: the labels are still generated, they are simply not written, and
since the tags in the files are the only record this plugin keeps, a preview
leaves the library exactly as it found it. A real run afterwards pays for the
same tracks again. This is the expensive mistake to avoid, and it has already
been made once: a library was labelled to $24.98 of a $25 limit with `dryRun`
left on, and not one tag reached a file. Nothing warns you, because from the
plugin's point of view it did exactly what it was told.

Preview is worth paying for once, to see whether you like the labels. It is not
worth paying for over a whole library, which is why turning it on caps a run at
20 tracks whatever `Label my library` says: a preview writes nothing, so running
it over everything buys exactly the answer a sample buys, at the price of the
whole library.

Start with `run: sample`. It labels 20 tracks spread across the library for a few
cents, which is enough to decide whether you like the results.

`run: revibe` is the one setting here that is free. It recomputes the `vibe` tag
from axes already in your files and never contacts a provider, so it works with
an expired key, a spent budget or a halted run. It is what you use after changing
a region, and it is safe to run at any time: a track whose vibes have not changed
is not rewritten at all.

## A worked smart playlist

Navidrome reads smart playlists from `.nsp` files placed in your music folder,
alongside `.m3u`. Once the tags are declared, they are ordinary criteria fields.

`late-night-winddown.nsp`:

```json
{
  "all": [
    { "lt": { "moodenergy": 35 } },
    { "gt": { "moodacousticness": 55 } },
    { "isNot": { "moodtempo": "frantic" } },
    { "is": { "moodtime": "late night" } }
  ],
  "sort": "random",
  "limit": 100
}
```

`is` against a multi-valued tag matches if *any* of its values match, so the
`moodtime` clause reads as "late night is one of the times this track suits".
`gt` and `lt` are the reason the axes have to be declared `Type = "int"`.

Some more useful shapes:

```json
{ "all": [ { "is": { "vibe": "focus" } }, { "is": { "moodvocal": "instrumental" } } ] }
```

```json
{ "all": [ { "lt": { "moodvalence": 30 } }, { "gt": { "moodintensity": 70 } } ] }
```

```json
{ "all": [ { "is": { "mood": "hypnotic" } }, { "gt": { "mooddensity": 60 } } ] }
```

The operators that apply here are `is`, `isNot`, `gt`, `lt`, `contains`,
`notContains`, `startsWith`, `endsWith`, `isMissing` and `isPresent`, combined
under `all` or `any`. **`inTheRange` does not work on tag fields**, and all ten
of these are tag fields: Navidrome rejects it there because a tag can hold
several values and the two bounds could be satisfied by different ones. Write a
band as a `gt` and an `lt` under the same `all`, as above.

## Keeping up with new music

`autoSync` is on by default and checks every 15 minutes. Navidrome exposes no
scan-completed hook, so polling is as close to "label on ingest" as a plugin can
get. The check is cheap when nothing has changed: one directory walk comparing
each file's modification time against the last check, and no file is opened
unless it has been touched since.

The first check on a library that has never been labelled has nothing to compare
against, so it works through the whole library a few hundred files at a time,
one tick after another. That is a consequence of Navidrome's 30-second ceiling
on a plugin call: a check that tried to open nine thousand files would be killed
partway and never get far enough to remember where it stopped.

## How the writing works

Tags are written by editing the FLAC metadata region directly. Audio bytes are
never decoded, re-encoded or moved. STREAMINFO, which carries the MD5 of the
decoded audio, is compared before and after and the write is refused if it
differs, so a bug in the block layer cannot quietly invalidate the checksums
across a library. Most writes fit in the existing padding and touch a few hundred
bytes; when they do not, the file is rebuilt through a temp file and an atomic
rename.

Modification time is deliberately allowed to change. Navidrome's folder hash is
over name, size and mtime, and an in-place tag edit does not change size, so
mtime is the only signal that a rescan is needed. It is also what the check for
new music runs on, which means a track this plugin has just labelled comes back
on the next check; that costs one metadata read, because it now reads as fully
labelled and is skipped.

If tagging is not working, `selfTest: read` opens one file and reports what it
found, and `selfTest: write` additionally writes a marker tag, reads it back,
removes it and checks the removal.

## What counts as already labelled

The tags are the only record. There is no database of what has been done: a
track's own tags answer that, and they are read from the same metadata block the
title and artist come from, so asking costs nothing beyond the read a labelling
pass has to do anyway.

A track is finished when it carries all seven of the values that are read
all-or-nothing - the five axes, `moodtempo` and `moodvocal` - plus at least one
`mood` word. `moodtime` and `vibe` are not required, because a track in no vibe
region is a normal outcome and requiring one would send a large slice of any
library back through paid labelling every pass. That is the same rule the
navidrome-mcp connector applies when it reads these tags back, and the two have
to agree or a track one of them calls labelled is invisible to the other.

Finished tracks are always skipped, whatever `skipTagged` says, because redoing
them costs money and changes nothing.

`skipTagged` is on by default and decides the ambiguous case: a track carrying a
`mood` and none of the seven. That is either an older version of this plugin or
Picard, beets or a hand edit, and nothing can tell which, so leaving it alone is
the default. Turning `skipTagged` off relabels those tracks, and doing so
replaces whatever wrote them. This plugin adds `mood`; it does not own it.

There is no version marker anywhere and there is deliberately no migration. A
future eleventh tag would be absent from every track written today, and that
absence is what would mark them as needing another pass - which is exactly how
today's ten-tag tracks are told apart from an older build's `mood`-only ones.

## Known limits

- **FLAC only**, as above.
- **A track whose every descriptor missed the vocabulary is judged again.** All
  2 to 4 words have to fold onto none of the 52 anchored terms for this to
  happen, so it is rare, but such a track ends up with no `mood` word and a
  `mood` word is part of what makes a track count as finished. The alternative
  is being unable to tell a finished track from a partial write, which is worse.
- **It asks for 8 MB of storage it does not need.** What has been labelled is
  recorded in the files, so the plugin's own state is a few hundred bytes: a
  spend counter, a queue depth, a watermark. The allowance is headroom for
  deleting the per-track entries versions before 0.3.0 left behind, which it
  does as it goes. Once those are gone the declaration can drop to Navidrome's
  1 MB default.
- **The batch API is not wired up.** The `batchMode` setting and the provider's
  `SupportsBatch` flag exist, but nothing currently submits work through a batch
  endpoint, so the 50% discount is not being taken. Cost figures here assume
  ordinary requests.
- **There is no progress reporting beyond Navidrome's log.** A run says what it
  did in the log and nowhere else, which is how a plugin quietly doing nothing
  goes unnoticed for days. Watching a labelling pass means watching the log.
- **Whether third-party clients surface these tags is untested.** Navidrome
  exposes declared tags through its API, but no Subsonic client and no Music
  Assistant instance has been checked against these nine custom names.

## What has actually been observed

The round trip is verified, on Navidrome 0.63.2, twice. Two containers were fed
the same fixtures, one with the `Tags` config block above and one without: with
it all ten tags came back through `/api/song` with multi-word values intact,
without it only `mood` survived and the other nine were dropped silently. A
`.nsp` smart playlist combining `gt moodenergy 50`, `lt moodvalence 45` and
`is moodvocal sung` then selected the right track, which is what proves the
axes really do compare as numbers rather than as text.

The same was then observed on a real 9,310-track library: tags written by this
plugin, read back by Navidrome's own scanner, spread across 13 of the 14 vibe
regions.

What has NOT been observed is a full pass. The largest completed run is a
sample.

## Building

```bash
make check   # gofmt, vet, tests
make         # dist/navidrome-mood.ndp
```

Standard Go targeting `wasip1`, not TinyGo. `GOTOOLCHAIN=auto` is set in the
Makefile, so the host Go version does not matter.
