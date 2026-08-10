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
- **Budget about $15** for 9,000 tracks on the default model. See
  [What it costs](#what-it-costs).

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
A track in no region is a normal outcome, not a failure.

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

The one measured figure: a 9,311-track pass on `claude-opus-5` cost **$27.22**,
against a pre-flight projection of $27.30. Sonnet is priced at 0.6x Opus on both
input and output, so **roughly $15 for a 9,000-track library** on the default
model. That figure is derived from the Opus run rather than measured directly.

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

**Preview mode costs full price.** It protects your files, not your bill: the
labels are still generated, they are simply not written.

Start with `run: sample`. It labels 20 tracks spread across the library for a few
cents, which is enough to decide whether you like the results.

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
get. The check is cheap when nothing has changed: one directory walk and one
key-value listing, and no file is opened unless it is new.

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
mtime is the only signal that a rescan is needed.

`skipTagged` is on by default and skips any track that already has a `mood`,
including ones written by Picard, beets or by hand. This plugin adds `mood`; it
does not own it.

If tagging is not working, `selfTest: read` opens one file and reports what it
found, and `selfTest: write` additionally writes a marker tag, reads it back,
removes it and checks the removal.

## Known limits

- **FLAC only**, as above.
- **The batch API is not wired up.** The `batchMode` setting and the provider's
  `SupportsBatch` flag exist, but nothing currently submits work through a batch
  endpoint, so the 50% discount is not being taken. Cost figures here assume
  ordinary requests.
- **The progress relay is not implemented.** `statusToken`, `relayUrl` and
  `sendTrackTitles` are present in the settings form and no code reads them, so
  nothing is sent anywhere regardless of how they are set.
- **Nothing has verified the tags round-tripping through Navidrome yet.** The
  claim that a custom tag written into a FLAC comes back out of `/api/song`
  intact rests on Navidrome's declared behaviour, not on an observed run of this
  plugin. Check `/api/tag` after your first sample pass rather than building on
  the assumption.
- **Whether third-party clients surface these tags is untested.** Navidrome
  exposes declared tags through its API, but no Subsonic client and no Music
  Assistant instance has been checked against these nine custom names.

## Building

```bash
make check   # gofmt, vet, tests
make         # dist/navidrome-mood.ndp
```

Standard Go targeting `wasip1`, not TinyGo. `GOTOOLCHAIN=auto` is set in the
Makefile, so the host Go version does not matter.
