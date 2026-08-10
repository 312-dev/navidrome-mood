# Deploying to the Mac Mini

Specific to this install: Navidrome runs as the `navidrome` container in the
`music-stack` compose project, its `/data` is the named volume
`music-stack_navidrome-data`, and `/music` is `/host_mnt/Volumes/Music/library`.
`docker` is at `/usr/local/bin/docker` and is not on the PATH a non-interactive
ssh session gets, so every command below exports it.

## Where this install actually is

Verified on 2026-08-10.

- **The library is labelled.** 9,195 of 9,310 tracks carry the full ten tags,
  written by 0.3.6. The remaining 115 are the two non-FLAC files and the tracks
  lost to batches that failed before the retry classification was fixed; an
  `everything` pass mops them up for a few cents and skips the rest for free.
- The nine custom tags are declared in `/data/navidrome.toml` and Navidrome has
  loaded them. Confirmed by the only test that can fail: an undeclared field is
  ignored and returns the whole library, a declared one filters. `?vibe=workout`
  and `?moodenergy=88` returned 0 of 9,310 while `?notarealtag=zzz` returned
  9,310, before any track was labelled.
- `allow_write_access` is 1, `/music` is mounted read-write, `dryRun` is off,
  and the per-track key-value entries an older version left behind have drained.

Because the axes are in the files, the expensive part is done and stays done.
Every later change to the region geometry is a `revibe` pass, which reads those
axes and costs nothing.

## Upgrading an install that is already labelled

This is the common case now, and it is much shorter than a first install.
0.4.0 refits every vibe radius, so the `vibe` tags currently in the files are
stale and no other tag is affected.

1. Copy the package over and reload, as in steps 1 to 3 below. The permission
   set has not changed since 0.3.x, so the plugin should stay enabled; check
   anyway, because a disabled plugin logs nothing at all.
2. Set **Label my library** to `revibe`. Leave the spend limit alone. No
   provider is contacted, so an expired key or an exhausted budget does not
   matter, and a halt does not block it.
3. Watch for `revibe: requested=... rewritten=... cleared=...`. `cleared` counts
   tracks that no longer fall in any region, which the tightened radii make
   common and which is the intended effect rather than a loss.
4. Set **Label my library** back to `off`.

The counts to expect on a 9,195-track library: a large `rewritten`, a
substantial `cleared`, and `unlabelled=115`.

## First install

### 1. Copy the package over

From a shell that can reach the Mini:

```sh
scp ~/repos/navidrome-mood/dist/navidrome-mood.ndp macmini:/tmp/
ssh macmini 'export PATH=/usr/local/bin:$PATH; docker cp /tmp/navidrome-mood.ndp navidrome:/data/plugins/navidrome-mood.ndp'
```

### 2. Re-enable it, because upgrading disables it

An upgrade whose manifest declares a different permission set leaves the plugin
registered but `enabled = 0`, pending your approval of the new permissions. This
is the right behaviour and it is silent: a disabled plugin never loads, so it
logs nothing at all, which reads like a failed install rather than a consent
prompt.

It happens on any upgrade that changes the permission set. Enable it in
Navidrome's plugin settings and confirm both flags:

```sh
docker exec navidrome sqlite3 -readonly /data/navidrome.db \
  "select id, enabled, allow_write_access, manifest->>'version' from plugin;"
```

`enabled` and `allow_write_access` both need to be 1. Check the second one even
if you granted it before, since the row was rewritten.

### 3. Pick it up and confirm the version changed

```sh
ssh macmini 'export PATH=/usr/local/bin:$PATH; docker restart navidrome && sleep 12; docker logs --tail 60 navidrome 2>&1 | grep -i "navidrome-mood"'
```

The line to look for is the readiness log. It must say **52 mood terms, 146
synonyms**. If it still says 60 terms and 28 synonyms, the old `.ndp` is still
being loaded and nothing below is worth doing.

The next line should say it reclaimed 500 per-track storage entries and name how
many remain. That number falls by 500 on each load and each auto-sync tick and
reaches zero within a couple of hours. It does not block anything: the 8 MB
allowance leaves room for the store to work while it drains.

### 4. Turn off preview mode and reset the spend counter

In Navidrome's plugin settings for navidrome-mood:

- **Preview only, do not change my files**: turn **off**, and check it after
  saving. This has now silently cost money twice: $24.98 on a full pass, then
  four more batches on 2026-08-10 that were labelled, billed and thrown away.
  The batch line says `written=0` and a warning names the cause, but only if
  someone reads it.
- **Most this run may cost (USD)**: change it to a new value, e.g. `5`. Changing
  the limit is what starts the per-run counter fresh; leaving it at 25 leaves it
  at $24.98 spent and every batch will keep failing.
- **Label my library**: set to `sample`.

`sample` labels 20 tracks spread across the library for a few cents. Do not set
`everything` until step 5 has passed.

### 5. Verify tags actually reached the files

Wait for the queue to run, then:

```sh
ssh macmini 'export PATH=/usr/local/bin:$PATH; docker logs --tail 40 navidrome 2>&1 | grep -i "navidrome-mood"'
```

A healthy batch line reads `labelled=20 written=20`. If it reads `written=0`
there is now an explicit warning naming the two causes, rather than a summary
that looks fine because every other number is healthy.

Then confirm from outside the box, which is the check that matters because it
goes through Navidrome's scanner rather than trusting the plugin's own report:

```sh
sqlite3 -readonly /data/navidrome.db \
  "select tag_name, count(*) from tag where tag_name like 'mood%' or tag_name='vibe' group by tag_name;"
```

Ten rows, one per tag, is success. No rows means the scanner has not picked the
files up yet: `ND_SCANSCHEDULE` is `1m`, so give it a minute.

### 6. Only then, the full pass

Set **Label my library** to `everything` and raise the run limit. Budget $30 to
$55 for about 9,000 tracks. The two measured runs disagree: 9,311 tracks on Opus
cost $27.22, while a later partial run reached 4,239 tracks for $24.98, which is
$0.0059 each and implies about $54. Take the higher one, and trust the
pre-flight estimate printed at the start of the run over either, since it prices
your actual model against your actual track count.

The lifetime cap is the real backstop. It never resets, and unlike the run limit
it is what stops repeated runs adding up to a number you did not intend.

## If something looks wrong

- **`written=0`** with labelling working: preview mode is on, or write access was
  revoked. Both are in the plugin settings.
- **Every batch fails on the spend cap**: the run limit was not changed, so the
  counter was not reset. This cannot happen on a `revibe` pass, which never
  reads the counter.
- **`revibe` reports a large `unlabelled`**: those tracks do not carry the five
  axes plus tempo and vocal, so there is nothing to recompute a vibe from.
  Recomputation never consults a model to fill a gap. Send them through
  `everything` first.
- **Tags in the files but nothing in Navidrome**: the `Tags` block in
  `/data/navidrome.toml` was lost. An undeclared tag is discarded silently by
  the scanner. Re-check with the 0-versus-9,310 test above.
- **Every write fails with `storage limit exceeded`**: the per-track entries
  described above have not finished clearing. Reload the plugin, or wait for the
  next auto-sync tick, and watch the reclaimed count fall.
- **A warning on load about tracks carrying a mood tag and nothing else**: those
  are tracks something wrote a `mood` on without the five axes, tempo or vocal,
  which is everything a smart playlist filters on. Nothing can tell whether this
  plugin or Picard wrote them, so they are left alone. Turning off **Leave
  existing mood tags alone** sends them through labelling and replaces whatever
  wrote them.
