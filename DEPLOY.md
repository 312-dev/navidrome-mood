# Deploying to the Mac Mini

Specific to this install: Navidrome runs as the `navidrome` container in the
`music-stack` compose project, its `/data` is the named volume
`music-stack_navidrome-data`, and `/music` is `/host_mnt/Volumes/Music/library`.
`docker` is at `/usr/local/bin/docker` and is not on the PATH a non-interactive
ssh session gets, so every command below exports it.

Everything in "Already done" was verified on 2026-08-09 and does not need
repeating.

## Already done

- The nine custom tags are declared in `/data/navidrome.toml` and Navidrome has
  loaded them. Confirmed by the only test that can fail: an undeclared field is
  ignored and returns the whole library, a declared one filters. `?vibe=workout`
  and `?moodenergy=88` return 0 of 9,310 while `?notarealtag=zzz` returns 9,310.
- `allow_write_access` is 1 for this plugin, so writes are permitted.
- `/music` is mounted read-write.
- Plugins are enabled and the plugin host loads WASM plugins successfully.

## What is not done

0.2.0 is registered and its package hash matches the local build, but it is
disabled pending approval of its new permission set, so nothing is running. See
step 2.

`dryRun` is still on and the run counter still reads $24.98 of a $25 limit, so
even once enabled it will write nothing and every batch will fail on the cap
until step 4.

The labels that $24.98 bought cannot be reused. They carry four axes against the
old 60-word vocabulary, and `density`, `tempo` and `vocal` were never judged, so
those tracks need another pass whatever happens. `RecordSchema = 2` forces
exactly that.

## Deploy

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

Going from 0.1.0 to 0.2.0 drops `subsonicapi` and `users`, so it happens on this
upgrade. Enable it in Navidrome's plugin settings and confirm both flags:

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

### 4. Turn off preview mode and reset the spend counter

In Navidrome's plugin settings for navidrome-mood:

- **Preview only, do not change my files**: turn **off**. This is what cost $25
  last time. It generates labels and writes nothing, at full price.
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

Set **Label my library** to `everything` and raise the run limit. Budget roughly
$15 for about 9,000 tracks on the default model, from the one measured data
point of $27.22 for 9,311 tracks on Opus and Sonnet priced at 0.6x on both input
and output. Treat it as an estimate: the ten-field record is larger than the
six-field one that number came from, so the real figure is likely higher.

The lifetime cap is the real backstop. It never resets, and unlike the run limit
it is what stops repeated runs adding up to a number you did not intend.

## If something looks wrong

- **`written=0`** with labelling working: preview mode is on, or write access was
  revoked. Both are in the plugin settings.
- **Every batch fails on the spend cap**: the run limit was not changed, so the
  counter was not reset.
- **Tags in the files but nothing in Navidrome**: the `Tags` block in
  `/data/navidrome.toml` was lost. An undeclared tag is discarded silently by
  the scanner. Re-check with the 0-versus-9,310 test above.
- **A warning about stale records on every load**: expected until a
  `run: everything` pass completes. Records from before the ten-tag write cannot
  be migrated because the missing fields were never judged.
