//go:build !wasip1

// Manifest validation runs on the host, not in wasm. Navidrome rejects an invalid
// manifest at install time with an error the user sees but cannot act on, so it is
// worth catching in CI instead.
package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaPath is Navidrome's own plugins/manifest-schema.json, vendored from
// v0.63.2. Refresh it when targeting a newer Navidrome.
const schemaPath = "testdata/manifest-schema.json"

func compiled(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(schemaPath)
	if err != nil {
		t.Fatalf("open schema: %v", err)
	}
	defer f.Close()

	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("manifest-schema.json", doc); err != nil {
		t.Fatalf("add resource: %v", err)
	}
	s, err := c.Compile("manifest-schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return s
}

func loadManifest(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return m
}

func TestManifestMatchesNavidromeSchema(t *testing.T) {
	if err := compiled(t).Validate(loadManifest(t)); err != nil {
		t.Fatalf("manifest.json is invalid:\n%v", err)
	}
}

// Without this, the test above could pass while validating nothing.
func TestSchemaValidationActuallyRejects(t *testing.T) {
	s := compiled(t)

	cases := map[string]func(map[string]any){
		"unknown permission": func(m map[string]any) {
			m["permissions"].(map[string]any)["nosuchservice"] = map[string]any{"reason": "x"}
		},
		"missing author": func(m map[string]any) { delete(m, "author") },
		"config without schema": func(m map[string]any) {
			m["config"] = map[string]any{"uiSchema": map[string]any{}}
		},
		"filesystem not a bool": func(m map[string]any) {
			m["permissions"].(map[string]any)["library"].(map[string]any)["filesystem"] = "yes"
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := loadManifest(t)
			mutate(m)
			if err := s.Validate(m); err == nil {
				t.Fatal("mutation accepted; schema validation is not checking this")
			}
		})
	}
}

// subsonicapi and users travel together, in both directions.
//
// subsonicapi requires users, enforced by Manifest.Validate in Navidrome rather
// than by the JSON schema, so the schema test above would not catch it. The
// converse is not a load failure but is worse in its own way: users on its own
// buys nothing, and someone reading the permission list before installing has no
// way to tell an unused grant from a used one.
func TestSubsonicAPIAndUsersTravelTogether(t *testing.T) {
	perms, _ := loadManifest(t)["permissions"].(map[string]any)
	_, subsonic := perms["subsonicapi"]
	_, users := perms["users"]

	if subsonic && !users {
		t.Error("subsonicapi is declared without users; Navidrome refuses to load this")
	}
	if users && !subsonic {
		t.Error("users is declared without subsonicapi, so nothing can exercise it")
	}
}

// manifest.website is the one clickable link Navidrome renders on the plugin's
// detail page. Someone who installed navidrome-mood.ndp clicks it and must land
// somewhere unmistakably the same project, so the host has to track the plugin
// name rather than drift into something shorter and cleverer.
// The progress relay was removed rather than left as a settings form that
// promises something no code does. A permission reason or a config field
// describing a feature that does not exist is read by users deciding whether to
// install, so it is worse than a missing feature.
func TestNoSettingsPromiseAFeatureThatDoesNotExist(t *testing.T) {
	m := loadManifest(t)
	props := m["config"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)
	for _, gone := range []string{"statusToken", "relayUrl", "sendTrackTitles"} {
		if _, ok := props[gone]; ok {
			t.Errorf("config still offers %q, which nothing reads", gone)
		}
	}
	httpReason := m["permissions"].(map[string]any)["http"].(map[string]any)["reason"].(string)
	for _, claim := range []string{"progress", "status endpoint", "webhook"} {
		if strings.Contains(strings.ToLower(httpReason), claim) {
			t.Errorf("the http permission still claims %q", claim)
		}
	}
}

// Config enums and the code that switches on them must not drift apart. Renaming
// a value in the manifest without changing the switch produces a dropdown that
// silently does nothing - no error, no log line, just a setting that has no
// effect. Caught here rather than by a user wondering why nothing happened.
func TestConfigEnumsMatchTheCode(t *testing.T) {
	m := loadManifest(t)
	props := m["config"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)

	enumOf := func(field string) []string {
		spec, ok := props[field].(map[string]any)
		if !ok {
			t.Fatalf("no such config field: %s", field)
		}
		raw, ok := spec["enum"].([]any)
		if !ok {
			t.Fatalf("%s has no enum", field)
		}
		var out []string
		for _, v := range raw {
			out = append(out, v.(string))
		}
		return out
	}

	// Every value the code branches on, and nothing else.
	for field, handled := range map[string][]string{
		"run":      {"off", "sample", "everything"},
		"selfTest": {"off", "read", "write"},
		"provider": {"anthropic", "openai-compatible"},
	} {
		got := enumOf(field)
		if len(got) != len(handled) {
			t.Errorf("%s: manifest offers %v, code handles %v", field, got, handled)
			continue
		}
		for i := range handled {
			if got[i] != handled[i] {
				t.Errorf("%s: manifest offers %v, code handles %v", field, got, handled)
				break
			}
		}
	}

	// Spend limits must have a floor. A zero or absent minimum lets someone save
	// an empty field and end up with no limit at all.
	for _, field := range []string{"maxSpendUsd", "lifetimeCapUsd"} {
		spec := props[field].(map[string]any)
		min, ok := spec["minimum"].(float64)
		if !ok || min <= 0 {
			t.Errorf("%s has no positive minimum, so it can be set to unlimited by clearing it", field)
		}
		if _, ok := spec["default"].(float64); !ok {
			t.Errorf("%s has no default", field)
		}
	}
}

// Every property must appear somewhere in the uiSchema, or it is unreachable in
// the form and might as well not exist.
func TestEveryConfigFieldIsReachableInTheForm(t *testing.T) {
	m := loadManifest(t)
	cfg := m["config"].(map[string]any)
	props := cfg["schema"].(map[string]any)["properties"].(map[string]any)

	seen := map[string]bool{}
	var walk func(any)
	walk = func(n any) {
		switch v := n.(type) {
		case map[string]any:
			if s, ok := v["scope"].(string); ok {
				seen[strings.TrimPrefix(s, "#/properties/")] = true
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(cfg["uiSchema"])

	for name := range props {
		if !seen[name] {
			t.Errorf("config field %q is not in the uiSchema, so nobody can set it", name)
		}
	}
}
