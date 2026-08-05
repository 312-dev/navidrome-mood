package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeDoer stands in for Navidrome's http_send so provider logic is testable on
// the host, with no wasm and no network.
type fakeDoer struct {
	gotReq Request
	resp   *Response
	err    error
}

func (f *fakeDoer) Do(r Request) (*Response, error) {
	f.gotReq = r
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func jsonResp(status int, v any) *Response {
	b, _ := json.Marshal(v)
	return &Response{Status: status, Body: b}
}

func okAnthropicBody(labels []Label, stop string) map[string]any {
	inner, _ := json.Marshal(map[string]any{"labels": labels})
	return map[string]any{
		"stop_reason": stop,
		"content": []map[string]any{
			{"type": "tool_use", "name": labelToolName, "input": json.RawMessage(inner)},
		},
		"usage": map[string]any{
			"input_tokens": 1200, "output_tokens": 340,
			"cache_creation_input_tokens": 0, "cache_read_input_tokens": 900,
		},
	}
}

func TestAnthropicLabelParsesLabelsAndUsage(t *testing.T) {
	want := []Label{{ID: "t1", Energy: 70, Valence: 40, Intensity: 60, Organic: 30,
		Moods: []string{"brooding", "heavy"}}}
	d := &fakeDoer{resp: jsonResp(200, okAnthropicBody(want, "tool_use"))}
	a := &Anthropic{Doer: d, APIKey: "k", Model: "claude-sonnet-5"}

	got, err := a.Label("system prompt", []Track{{ID: "t1", Title: "x", Artist: "y"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Labels) != 1 || got.Labels[0].ID != "t1" || got.Labels[0].Energy != 70 {
		t.Fatalf("labels not parsed: %+v", got.Labels)
	}
	// Usage must come from the response, never from an estimate - it is what
	// charges the budget.
	wantUsage := Usage{Input: 1200, Output: 340, CacheRead: 900}
	if got.Usage != wantUsage {
		t.Fatalf("usage = %+v, want %+v", got.Usage, wantUsage)
	}
}

// The taxonomy prefix is identical across batches, so caching it is worth about
// a fifth of a full pass. Silently losing the cache_control marker would be
// invisible except on the bill.
func TestAnthropicMarksTheSystemPromptCacheable(t *testing.T) {
	d := &fakeDoer{resp: jsonResp(200, okAnthropicBody(nil, "tool_use"))}
	a := &Anthropic{Doer: d, APIKey: "k"}
	if _, err := a.Label("taxonomy", []Track{{ID: "t1"}}); err != nil {
		t.Fatal(err)
	}

	var sent anthropicReq
	if err := json.Unmarshal(d.gotReq.Body, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.System) != 1 || sent.System[0].CacheControl == nil {
		t.Fatal("system prompt was not marked cache_control: ephemeral")
	}
	if sent.System[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("cache_control type = %q, want ephemeral", sent.System[0].CacheControl.Type)
	}
	if sent.Stream {
		t.Fatal("stream must be false: the host cannot stream responses")
	}
	if sent.ToolChoice == nil || sent.ToolChoice.Name != labelToolName {
		t.Fatal("tool choice not forced, so output is not schema-constrained")
	}
}

// Truncation at max_tokens produces a valid response holding a partial tool call.
// Treating that as "fewer tracks needed labelling" would silently skip them.
func TestAnthropicDetectsMaxTokensTruncation(t *testing.T) {
	d := &fakeDoer{resp: jsonResp(200, okAnthropicBody([]Label{{ID: "t1"}}, "max_tokens"))}
	a := &Anthropic{Doer: d, APIKey: "k"}

	res, err := a.Label("s", []Track{{ID: "t1"}, {ID: "t2"}})
	if err == nil {
		t.Fatal("truncated batch was accepted; those tracks would be skipped silently")
	}
	if !strings.Contains(err.Error(), "max_tokens") {
		t.Fatalf("error does not name the cause: %v", err)
	}
	// Usage is still returned, because those tokens were spent and must be charged.
	if res == nil || res.Usage.Output == 0 {
		t.Fatal("usage was discarded on truncation; the spend would go uncounted")
	}
}

func TestAnthropicTypedErrors(t *testing.T) {
	cases := map[int]error{401: ErrAuth, 403: ErrAuth, 429: ErrRateLimited}
	for status, want := range cases {
		d := &fakeDoer{resp: jsonResp(status, map[string]any{
			"error": map[string]any{"message": "nope", "type": "invalid_request_error"},
		})}
		a := &Anthropic{Doer: d, APIKey: "k"}
		_, err := a.Label("s", []Track{{ID: "t1"}})
		if !errors.Is(err, want) {
			t.Fatalf("status %d gave %v, want %v", status, err, want)
		}
	}
}

// Provider error bodies echo request context and can include the credential.
// They must never reach a log verbatim.
func TestErrorBodyIsNeverEchoedVerbatim(t *testing.T) {
	secret := "sk-ant-SUPERSECRET-abc123"
	d := &fakeDoer{resp: &Response{
		Status: 400,
		Body:   []byte(`{"raw":"request failed with header x-api-key: ` + secret + `"}`),
	}}
	a := &Anthropic{Doer: d, APIKey: secret}

	_, err := a.Label("s", []Track{{ID: "t1"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("API key leaked into the error message: %v", err)
	}
}

// The host truncates at 10 MB with no error, so an oversized response arrives as
// malformed JSON. Report the real cause instead.
func TestDetectsSilentHostTruncation(t *testing.T) {
	d := &fakeDoer{resp: &Response{Status: 200, Body: make([]byte, maxResponseBytes)}}
	a := &Anthropic{Doer: d, APIKey: "k"}
	_, err := a.Label("s", []Track{{ID: "t1"}})
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("got %v, want ErrTruncated", err)
	}
}

func TestEmptyBatchMakesNoRequest(t *testing.T) {
	d := &fakeDoer{err: errors.New("should not be called")}
	a := &Anthropic{Doer: d, APIKey: "k"}
	res, err := a.Label("s", nil)
	if err != nil || len(res.Labels) != 0 {
		t.Fatalf("empty batch did something: %v %+v", err, res)
	}
}

func TestDefaultModelIsPricedAndNotTheMostExpensive(t *testing.T) {
	p, ok := Prices[DefaultAnthropicModel]
	if !ok {
		t.Fatalf("default model %q has no price, so the spend cap could not be enforced",
			DefaultAnthropicModel)
	}
	if opus := Prices["claude-opus-5"]; p.Out >= opus.Out {
		t.Fatalf("default model costs %.2f/Mtok out, not cheaper than opus at %.2f; "+
			"a public plugin should not default to the priciest option", p.Out, opus.Out)
	}
}
