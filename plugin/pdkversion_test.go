//go:build !wasip1

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The PDK is a nested Go module with NO tags of its own - `go list -m -versions`
// returns nothing, and the parent v0.63.2 tag does not contain the package. So the
// only way to depend on it is a pseudo-version from a master commit.
//
// Master's PDK is byte-identical to v0.63.2's except that it adds one extra host
// service: storage (nd_host_storage.go). Importing that service would compile
// cleanly here and then fail at RUNTIME on Navidrome 0.63.2, because the host
// function does not exist in that release.
//
// Compiles-here-breaks-there is the hardest kind of bug to catch late, so this
// test fails the build instead. Delete it when the minimum supported Navidrome
// version includes the storage service.
func TestDoesNotUseHostServicesMissingFromTargetRelease(t *testing.T) {
	const forbidden = "nd_host_storage"

	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip our own build output and VCS metadata.
			if info.Name() == "dist" || info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The PDK exposes the service as host.Storage*; the underlying export is
		// nd_host_storage. Catch either spelling.
		src := string(raw)
		if strings.Contains(src, forbidden) || strings.Contains(src, "host.Storage") {
			t.Errorf("%s references the storage host service, which does not exist in "+
				"Navidrome 0.63.2 and would fail at runtime", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
