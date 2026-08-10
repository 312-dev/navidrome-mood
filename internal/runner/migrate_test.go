package runner

import (
	"errors"
	"testing"
)

// legacyTags is a complete label written under the names used before 0.5.0.
func legacyTags() map[string][]string {
	return map[string][]string{
		"moodenergy":       {"55"},
		"moodvalence":      {"40"},
		"moodintensity":    {"30"},
		"moodacousticness": {"70"},
		"mooddensity":      {"45"},
		"moodtempo":        {"mid"},
		"moodvocal":        {"sung"},
		"moodtime":         {"evening"},
		TagMood:            {"tender"},
		TagVibe:            {"dinner"},
	}
}

// The whole point: values move to the new name and the old name is removed.
// Writing the new one alone would leave every file carrying both, and the stale
// copy would be a second answer to the same question that nothing updates again.
func TestMigrateMovesValuesAndRemovesTheOldNames(t *testing.T) {
	tagger := newTagger()
	out := MigrateTags([]Item{{Path: "a.flac", Tags: legacyTags()}}, tagger, false)

	if out.Renamed != 1 || out.Current != 0 {
		t.Fatalf("got %+v, want one renamed", out)
	}
	wrote := tagger.written["a.flac"]

	for old, current := range LegacyTagNames {
		values, named := wrote[current]
		if !named || len(values) == 0 {
			t.Errorf("%s carries no value, so the label lost a field in the rename", current)
		}
		if v, named := wrote[old]; !named || len(v) != 0 {
			t.Errorf("%s was written as %v, want the name present with no values, "+
				"which is what removes it", old, v)
		}
	}
	if got := wrote[TagEnergy]; len(got) != 1 || got[0] != "55" {
		t.Errorf("%s = %v, want the value carried across unchanged", TagEnergy, got)
	}
}

// Nothing outside the rename may be touched. `mood` and `vibe` did not move, so
// naming them in the write would risk a value this pass has no opinion about.
func TestMigrateLeavesTagsThatDidNotMoveAlone(t *testing.T) {
	tagger := newTagger()
	MigrateTags([]Item{{Path: "a.flac", Tags: legacyTags()}}, tagger, false)

	for _, name := range []string{TagMood, TagVibe} {
		if _, named := tagger.written["a.flac"][name]; named {
			t.Errorf("the write named %s, which did not move", name)
		}
	}
}

// A file already on the current names must cost no write. Every write rewrites
// a FLAC's metadata block and bumps its mtime, which would send the whole
// library back through Navidrome's scanner on every pass.
func TestMigrateWritesNothingForAFileAlreadyCurrent(t *testing.T) {
	tagger := newTagger()
	current := map[string][]string{
		TagEnergy: {"55"}, TagValence: {"40"}, TagIntensity: {"30"},
		TagAcousticness: {"70"}, TagDensity: {"45"}, TagTempo: {"mid"},
		TagVocal: {"sung"}, TagTime: {"evening"}, TagMood: {"tender"},
	}
	out := MigrateTags([]Item{{Path: "a.flac", Tags: current}}, tagger, false)

	if out.Current != 1 || out.Renamed != 0 {
		t.Fatalf("got %+v, want one already-current and no writes", out)
	}
	if len(tagger.written) != 0 {
		t.Fatalf("wrote %v, want nothing", tagger.written)
	}
}

// An interrupted pass can leave a file holding both names. The new one is what
// a later pass wrote, so it is the newer answer and must not be overwritten by
// the value the old name still carries.
func TestMigrateKeepsTheCurrentValueWhenBothNamesArePresent(t *testing.T) {
	tags := legacyTags()
	tags[TagEnergy] = []string{"99"}

	tagger := newTagger()
	MigrateTags([]Item{{Path: "a.flac", Tags: tags}}, tagger, false)

	wrote := tagger.written["a.flac"]
	if _, named := wrote[TagEnergy]; named {
		t.Errorf("%s was rewritten as %v, want it left alone: it already holds the "+
			"newer value", TagEnergy, wrote[TagEnergy])
	}
	if v, named := wrote["moodenergy"]; !named || len(v) != 0 {
		t.Errorf("moodenergy = %v, want it removed", v)
	}
}

// The guard that keeps a rename from costing money. A legacy file reads as
// unlabelled to FullyLabelled, which knows only the current names, so without
// this a whole library written before 0.5.0 would be judged and billed again to
// produce values it already carries.
func TestALegacyLabelIsNotSentForLabelling(t *testing.T) {
	tags := legacyTags()
	if FullyLabelled(tags) {
		t.Fatal("FullyLabelled accepted the old names, so this test proves nothing")
	}
	if !NeedsMigration(tags) {
		t.Fatal("NeedsMigration did not recognise a legacy label")
	}

	r := &Runner{Provider: nil, Tagger: newTagger()}
	out, err := r.Process([]Item{{Path: "a.flac", Tags: tags}})
	if err != nil {
		t.Fatalf("Process errored: %v", err)
	}
	if out.NeedsMigration != 1 || out.Skipped != 1 {
		t.Fatalf("got %+v, want the track skipped and counted as needing migration", out)
	}
	if out.Labelled != 0 || out.Cost != 0 {
		t.Fatalf("got %+v, want nothing labelled and nothing spent", out)
	}
}

// A file carrying no plugin tags at all is not a migration candidate, and must
// stay available to labelling.
func TestMigrateIgnoresAFileWithNoPluginTags(t *testing.T) {
	tagger := newTagger()
	out := MigrateTags([]Item{{Path: "a.flac", Tags: map[string][]string{"genre": {"Folk"}}}}, tagger, false)

	if out.Current != 1 || out.Renamed != 0 {
		t.Fatalf("got %+v, want it counted as nothing to do", out)
	}
	if NeedsMigration(map[string][]string{"genre": {"Folk"}}) {
		t.Error("a file with no plugin tags was reported as needing migration")
	}
}

func TestMigrateDryRunCountsWithoutWriting(t *testing.T) {
	tagger := newTagger()
	out := MigrateTags([]Item{{Path: "a.flac", Tags: legacyTags()}}, tagger, true)

	if out.Renamed != 1 {
		t.Fatalf("got %+v, want the rename counted", out)
	}
	if len(tagger.written) != 0 {
		t.Fatalf("preview wrote %v", tagger.written)
	}
}

// One unwritable file costs that file, not the batch.
func TestMigrateReportsWriteFailuresAndKeepsGoing(t *testing.T) {
	tagger := newTagger()
	tagger.writeErr = errors.New("read-only file system")

	out := MigrateTags([]Item{
		{Path: "a.flac", Tags: legacyTags()},
		{Path: "b.flac", Tags: legacyTags()},
	}, tagger, false)

	if len(out.Errors) != 2 || out.Renamed != 0 {
		t.Fatalf("got %+v, want both paths in Errors and nothing counted renamed", out)
	}
}

// Every legacy name must map to a name that actually exists, and none of the
// current names may still begin with `mood`. That prefix is the entire reason
// for the rename: Navidrome compiles `tag_name=mood` to `LIKE 'mood%'`, so any
// tag starting with it is swept into the song list's Mood dropdown.
func TestNoCurrentTagNameStartsWithTheCollidingPrefix(t *testing.T) {
	current := map[string]bool{}
	for _, n := range TagNames {
		current[n] = true
	}
	for old, to := range LegacyTagNames {
		if !current[to] {
			t.Errorf("legacy %s maps to %s, which is not one of the current tags", old, to)
		}
		if old == to {
			t.Errorf("%s maps to itself, so the rename does nothing for it", old)
		}
	}
	for _, n := range TagNames {
		if n == TagMood {
			// The one that legitimately is `mood`: it is Navidrome's own tag and
			// the dropdown is meant to find it.
			continue
		}
		if len(n) >= 4 && n[:4] == "mood" {
			t.Errorf("%s still starts with `mood`, so it is still swept into the "+
				"Mood dropdown by `tag_name LIKE 'mood%%'`", n)
		}
	}
}
