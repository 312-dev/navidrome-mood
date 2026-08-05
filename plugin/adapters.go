// Adapters binding Navidrome's host services to the interfaces the internal
// packages are written against. Everything here is thin on purpose: this is the
// only code in the project that cannot be unit tested, because it exists solely
// to call host functions that do not exist outside wasm.
package main

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"

	"github.com/312-dev/navidrome-mood/flac"
	"github.com/312-dev/navidrome-mood/internal/llm"
)

// kvStore adapts the kvstore host service to runner.Store.
type kvStore struct{}

func (kvStore) Get(key string) ([]byte, bool, error) { return host.KVStoreGet(key) }
func (kvStore) Set(key string, value []byte) error   { return host.KVStoreSet(key, value) }

// httpDoer adapts the http host service to llm.Doer.
type httpDoer struct{}

func (httpDoer) Do(r llm.Request) (*llm.Response, error) {
	resp, err := host.HTTPSend(host.HTTPRequest{
		Method:  r.Method,
		URL:     r.URL,
		Headers: r.Headers,
		Body:    r.Body,
		// TimeoutMs is int32 on the host side. Our callers stay well under
		// Navidrome's 30s invocation ceiling, so the narrowing is safe.
		TimeoutMs: int32(r.TimeoutMs),
	})
	if err != nil {
		return nil, err
	}
	return &llm.Response{
		Status:  int(resp.StatusCode),
		Headers: resp.Headers,
		Body:    resp.Body,
	}, nil
}

// fileTagger adapts the flac package to runner.Tagger, resolving library-relative
// paths against the mount the plugin actually sees.
//
// Library.Path is where the files live on the HOST; Library.MountPoint is where
// they appear inside the WASI sandbox. Using Path here would fail every open with
// a confusing not-exist error, because that path is meaningless in here.
type fileTagger struct {
	mounts []string
}

func newFileTagger() (*fileTagger, error) {
	libs, err := host.LibraryGetAllLibraries()
	if err != nil {
		return nil, fmt.Errorf("listing libraries: %w", err)
	}
	t := &fileTagger{}
	for _, l := range libs {
		mp := l.MountPoint
		if mp == "" {
			mp = l.Path
		}
		t.mounts = append(t.mounts, mp)
	}
	if len(t.mounts) == 0 {
		return nil, fmt.Errorf("no libraries are visible to the plugin")
	}
	return t, nil
}

// resolve maps whatever path Navidrome hands us onto something openable here.
//
// Absolute paths that already sit under a mount are used as-is; anything else is
// tried relative to each mount in turn. Reporting every candidate on failure
// matters because "file not found" inside a sandboxed filesystem is otherwise
// almost impossible to diagnose from a log line.
func (t *fileTagger) resolve(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	var tried []string
	if path.IsAbs(p) {
		for _, m := range t.mounts {
			if strings.HasPrefix(p, m) {
				if _, err := os.Stat(p); err == nil {
					return p, nil
				}
				tried = append(tried, p)
			}
		}
	}
	rel := strings.TrimPrefix(p, "/")
	for _, m := range t.mounts {
		cand := path.Join(m, rel)
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
		tried = append(tried, cand)
	}
	return "", fmt.Errorf("cannot find %q under any mount (tried %s)", p, strings.Join(tried, ", "))
}

func (t *fileTagger) ReadMood(p string) ([]string, error) {
	full, err := t.resolve(p)
	if err != nil {
		return nil, err
	}
	fh, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer fh.Close()

	f, err := flac.Parse(fh)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", full, err)
	}
	c, err := f.Comments()
	if err != nil {
		// A file with no comment block simply has no moods; that is not an error.
		return nil, nil
	}
	return c.Get("MOOD"), nil
}

func (t *fileTagger) WriteMood(p string, values []string) (string, error) {
	full, err := t.resolve(p)
	if err != nil {
		return "", err
	}
	strat, err := flac.UpdateTags(full, func(c *flac.Comments) error {
		c.Set("MOOD", values...)
		return nil
	})
	return string(strat), err
}
