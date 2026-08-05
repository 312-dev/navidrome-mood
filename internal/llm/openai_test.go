package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func okOpenAIBody(labels []Label, finish string, prompt, cached, completion int64) map[string]any {
	inner, _ := json.Marshal(map[string]any{"labels": labels})
	return map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": string(inner)}, "finish_reason": finish},
		},
		"usage": map[string]any{
			"prompt_tokens":         prompt,
			"completion_tokens":     completion,
			"prompt_tokens_details": map[string]any{"cached_tokens": cached},
		},
	}
}

func TestOpenAILabelParses(t *testing.T) {
	want := []Label{{ID: "t1", Energy: 55, Moods: []string{"breezy", "sunny"}}}
	d := &fakeDoer{resp: jsonResp(200, okOpenAIBody(want, "stop", 1000, 0, 200))}
	o := &OpenAICompatible{Doer: d, APIKey: "k", Model: "gpt-4o-mini"}

	got, err := o.Label("sys", []Track{{ID: "t1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Labels) != 1 || got.Labels[0].ID != "t1" {
		t.Fatalf("labels not parsed: %+v", got.Labels)
	}
}

// cached_tokens is reported INSIDE prompt_tokens, not alongside it. Adding both
// would charge the cached portion twice.
func TestOpenAIDoesNotDoubleCountCachedTokens(t *testing.T) {
	d := &fakeDoer{resp: jsonResp(200, okOpenAIBody(nil, "stop", 1000, 400, 200))}
	o := &OpenAICompatible{Doer: d, APIKey: "k", Model: "gpt-4o-mini"}

	got, err := o.Label("sys", []Track{{ID: "t1"}})
	if err != nil {
		t.Fatal(err)
	}
	if total := got.Usage.Input + got.Usage.CacheRead; total != 1000 {
		t.Fatalf("input+cacheRead = %d, want 1000 (prompt_tokens already includes cached)", total)
	}
	if got.Usage.Input != 600 || got.Usage.CacheRead != 400 {
		t.Fatalf("usage = %+v, want Input 600 / CacheRead 400", got.Usage)
	}
}

func TestOpenAIDetectsLengthTruncation(t *testing.T) {
	d := &fakeDoer{resp: jsonResp(200, okOpenAIBody([]Label{{ID: "t1"}}, "length", 100, 0, 50))}
	o := &OpenAICompatible{Doer: d, APIKey: "k", Model: "gpt-4o-mini"}

	res, err := o.Label("sys", []Track{{ID: "t1"}, {ID: "t2"}})
	if err == nil {
		t.Fatal("truncated response accepted; those tracks would be skipped silently")
	}
	if res.Usage.Output == 0 {
		t.Fatal("usage discarded on truncation; the spend would go uncounted")
	}
}

func TestOpenAIBaseURLRouting(t *testing.T) {
	cases := map[string]string{
		"":                              defaultOpenAIBase + "/chat/completions",
		"https://openrouter.ai/api/v1":  "https://openrouter.ai/api/v1/chat/completions",
		"https://openrouter.ai/api/v1/": "https://openrouter.ai/api/v1/chat/completions",
		"http://127.0.0.1:11434/v1":     "http://127.0.0.1:11434/v1/chat/completions",
	}
	for base, want := range cases {
		d := &fakeDoer{resp: jsonResp(200, okOpenAIBody(nil, "stop", 1, 0, 1))}
		o := &OpenAICompatible{Doer: d, APIKey: "k", Model: "m", BaseURL: base}
		if _, err := o.Label("s", []Track{{ID: "t"}}); err != nil {
			t.Fatal(err)
		}
		if d.gotReq.URL != want {
			t.Fatalf("base %q routed to %q, want %q", base, d.gotReq.URL, want)
		}
	}
}

// Labelling must be reproducible: the same track should not land in different
// moods run to run because of sampling noise.
func TestOpenAIUsesDeterministicSampling(t *testing.T) {
	d := &fakeDoer{resp: jsonResp(200, okOpenAIBody(nil, "stop", 1, 0, 1))}
	o := &OpenAICompatible{Doer: d, APIKey: "k", Model: "m"}
	if _, err := o.Label("s", []Track{{ID: "t"}}); err != nil {
		t.Fatal(err)
	}
	var sent openAIReq
	if err := json.Unmarshal(d.gotReq.Body, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Temperature != 0 {
		t.Fatalf("temperature = %v, want 0", sent.Temperature)
	}
	if sent.Stream {
		t.Fatal("stream must be false: the host cannot stream responses")
	}
	if sent.Format == nil || sent.Format.Type != "json_schema" {
		t.Fatal("response_format not requested; output would be unconstrained prose")
	}
}

// Many compatible servers ignore json_schema and return prose. That must be a
// legible error naming the likely cause, not a JSON parse failure.
func TestOpenAIExplainsUnsupportedSchemaMode(t *testing.T) {
	body := okOpenAIBody(nil, "stop", 100, 0, 20)
	body["choices"].([]map[string]any)[0]["message"] = map[string]any{
		"content": "Sure! Here are the moods you asked for:",
	}
	d := &fakeDoer{resp: jsonResp(200, body)}
	o := &OpenAICompatible{Doer: d, APIKey: "k", Model: "m", BaseURL: "https://example.invalid/v1"}

	_, err := o.Label("s", []Track{{ID: "t"}})
	if err == nil {
		t.Fatal("prose response was accepted as labels")
	}
	if !strings.Contains(err.Error(), "schema mode") {
		t.Fatalf("error does not point at the likely cause: %v", err)
	}
}

func TestOpenAITypedErrors(t *testing.T) {
	for status, want := range map[int]error{401: ErrAuth, 429: ErrRateLimited} {
		d := &fakeDoer{resp: jsonResp(status, map[string]any{
			"error": map[string]any{"message": "denied"},
		})}
		o := &OpenAICompatible{Doer: d, APIKey: "k", Model: "m"}
		if _, err := o.Label("s", []Track{{ID: "t"}}); !errors.Is(err, want) {
			t.Fatalf("status %d gave %v, want %v", status, err, want)
		}
	}
}

// Declining batch support is deliberate: this adapter cannot tell from a base URL
// whether the server behind it has a batch endpoint, and claiming support then
// failing is worse than greying the option out.
func TestOpenAIDoesNotClaimBatchSupport(t *testing.T) {
	o := &OpenAICompatible{}
	if o.SupportsBatch() {
		t.Fatal("claims batch support it cannot verify")
	}
	a := &Anthropic{}
	if !a.SupportsBatch() {
		t.Fatal("Anthropic does offer a batch API and should say so")
	}
}

// Both adapters must satisfy Provider, or the runner cannot treat them uniformly.
func TestBothAdaptersImplementProvider(t *testing.T) {
	var _ Provider = (*Anthropic)(nil)
	var _ Provider = (*OpenAICompatible)(nil)
}
