package runner

// MigrateOutcome reports what one rename batch did.
type MigrateOutcome struct {
	Requested int
	// Current counts files already using the new names. Nothing is written for
	// them, which is what makes a second pass over a migrated library cost one
	// read per file and no writes.
	Current int
	Renamed int
	Errors  map[string]string
}

// MigrateTags rewrites a file's tags under their current names.
//
// A tag name is not data, it is an address, and this library's addresses moved
// once already and may move again. Renaming by relabelling would cost about $19
// on nine thousand tracks to re-derive values that are already correct and
// already in the files, which is the same trap the vibe radii were in before
// Revibe existed. The values are read from the file and written straight back
// under the new names, so this calls no provider and costs nothing.
//
// The write names both addresses: the new one carrying the value, the old one
// carrying nothing, which is what removes it. Writing the new name alone would
// leave every file holding both, and the old copy would then be a second answer
// to the same question that nothing would ever update again.
//
// Only the names in LegacyTagNames are touched. A file's other tags are not
// named in the write, so nothing else can be disturbed, and a track that has
// already moved is left completely alone.
func MigrateTags(items []Item, tagger Tagger, dryRun bool) MigrateOutcome {
	out := MigrateOutcome{Requested: len(items), Errors: map[string]string{}}

	for _, it := range items {
		write := map[string][]string{}
		for old, current := range LegacyTagNames {
			values := nonEmpty(it.Tags[old])
			if len(values) == 0 {
				continue
			}
			// The new name wins if both are somehow present. That can only happen
			// if a write was interrupted partway, and the new name is the one a
			// later pass wrote, so it is the newer answer.
			if len(nonEmpty(it.Tags[current])) == 0 {
				write[current] = values
			}
			write[old] = nil
		}
		if len(write) == 0 {
			out.Current++
			continue
		}
		if dryRun {
			out.Renamed++
			continue
		}
		if _, err := tagger.WriteTags(it.Path, write); err != nil {
			out.Errors[it.Path] = err.Error()
			continue
		}
		out.Renamed++
	}
	return out
}

// NeedsMigration reports whether a file still carries any tag under an old name.
//
// Used to keep a labelling run from paying for one. A track whose whole label
// sits under the old names looks unlabelled to FullyLabelled, which reads the
// current names only, so without this check an `everything` pass would judge an
// already-judged library from scratch and bill for all of it.
func NeedsMigration(tags map[string][]string) bool {
	for old := range LegacyTagNames {
		if len(nonEmpty(tags[old])) > 0 {
			return true
		}
	}
	return false
}
