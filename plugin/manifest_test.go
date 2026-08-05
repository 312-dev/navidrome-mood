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

// subsonicapi requires users, enforced by Manifest.Validate in Navidrome rather
// than by the JSON schema - so the schema test above would not catch it.
func TestSubsonicAPIDeclaresUsers(t *testing.T) {
	perms, _ := loadManifest(t)["permissions"].(map[string]any)
	if _, ok := perms["subsonicapi"]; !ok {
		return
	}
	if _, ok := perms["users"]; !ok {
		t.Fatal("subsonicapi is declared without users; Navidrome refuses to load this")
	}
}

// manifest.website is the one clickable link Navidrome renders on the plugin's
// detail page. Someone who installed navidrome-mood.ndp clicks it and must land
// somewhere unmistakably the same project, so the host has to track the plugin
// name rather than drift into something shorter and cleverer.
func TestWebsiteAndRelayMatchThePluginName(t *testing.T) {
	m := loadManifest(t)
	name, _ := m["name"].(string)
	if name == "" {
		t.Fatal("manifest has no name")
	}
	wantHost := "https://" + name + ".312.dev"

	if got, _ := m["website"].(string); got != wantHost {
		t.Errorf("website = %q, want %q", got, wantHost)
	}

	props := m["config"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)
	relay := props["relayUrl"].(map[string]any)["default"]
	if relay != wantHost {
		t.Errorf("relayUrl default = %v, want %q", relay, wantHost)
	}
}

// The vocabulary override help text promises terms cannot contain a separator.
// Keep the manifest and the code telling the same story.
func TestManifestDocumentsSeparatorConstraint(t *testing.T) {
	raw, err := os.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"semicolon", "slash", "comma"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("manifest does not warn about %q in the vocabulary override", want)
		}
	}
}
