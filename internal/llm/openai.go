package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OpenAICompatible speaks the /chat/completions shape, which is the closest
// thing the ecosystem has to a lingua franca.
//
// A configurable BaseURL is what turns one adapter into most of the ecosystem:
// OpenAI, OpenRouter, Groq, Together, Fireworks, DeepSeek, LM Studio and Ollama
// all serve it. That is the entire reason only two adapters are needed rather
// than one per vendor.
type OpenAICompatible struct {
	Doer   Doer
	APIKey string
	Model  string
	// BaseURL omits the trailing /chat/completions, e.g.
	// https://openrouter.ai/api/v1 or http://127.0.0.1:11434/v1 for Ollama.
	// Reaching a loopback address requires the -local plugin build; the default
	// build declares no requiredHosts, which blocks private and loopback ranges.
	BaseURL   string
	MaxTokens int
}

const defaultOpenAIBase = "https://api.openai.com/v1"

func (o *OpenAICompatible) Name() string { return "openai-compatible" }

// ModelID returns the configured model. There is no default: the right model
// depends entirely on which server BaseURL points at.
func (o *OpenAICompatible) ModelID() string { return o.Model }

// SupportsBatch reports false because only OpenAI proper offers a discounted
// batch endpoint, and this adapter cannot tell from a base URL whether the thing
// behind it does. Claiming batch support and then failing would be worse than
// declining it: the UI greys the option out instead.
func (o *OpenAICompatible) SupportsBatch() bool { return false }

func (o *OpenAICompatible) endpoint() string {
	base := strings.TrimRight(o.BaseURL, "/")
	if base == "" {
		base = defaultOpenAIBase
	}
	return base + "/chat/completions"
}

func (o *OpenAICompatible) maxTokens() int {
	if o.MaxTokens > 0 {
		return o.MaxTokens
	}
	return 4096
}

type openAIReq struct {
	Model       string         `json:"model"`
	Messages    []openAIMsg    `json:"messages"`
	MaxTokens   int            `json:"max_tokens"`
	Temperature float64        `json:"temperature"`
	Stream      bool           `json:"stream"`
	Format      *openAIRespFmt `json:"response_format,omitempty"`
}

type openAIMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIRespFmt requests JSON output. json_schema with strict:true is honoured by
// OpenAI and a growing number of compatible servers; those that do not recognise
// it generally fall back to plain JSON mode rather than erroring, which is why
// the response is still validated rather than trusted.
type openAIRespFmt struct {
	Type       string          `json:"type"`
	JSONSchema *openAIJSONSpec `json:"json_schema,omitempty"`
}

type openAIJSONSpec struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type openAIResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func (o *OpenAICompatible) Label(system string, tracks []Track) (*Result, error) {
	if len(tracks) == 0 {
		return &Result{}, nil
	}
	payload, err := json.Marshal(tracks)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(openAIReq{
		Model: o.Model,
		Messages: []openAIMsg{
			{Role: "system", Content: system},
			{Role: "user", Content: string(payload)},
		},
		MaxTokens: o.maxTokens(),
		// Labelling should be reproducible: the same track twice should not land
		// in different moods because of sampling noise.
		Temperature: 0,
		Stream:      false,
		Format: &openAIRespFmt{
			Type: "json_schema",
			JSONSchema: &openAIJSONSpec{
				Name:   "emit_labels",
				Strict: false,
				Schema: labelSchema(),
			},
		},
	})
	if err != nil {
		return nil, err
	}

	resp, err := o.Doer.Do(Request{
		Method: "POST",
		URL:    o.endpoint(),
		Headers: map[string]string{
			"content-type":  "application/json",
			"authorization": "Bearer " + o.APIKey,
		},
		Body:      body,
		TimeoutMs: 25_000,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTransport, err)
	}
	if err := checkResponse(o.Name(), resp); err != nil {
		return nil, err
	}

	var out openAIResp
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("llm: openai-compatible response was not JSON: %w", err)
	}

	// Cached prompt tokens are reported inside prompt_tokens, not alongside it,
	// so counting both would double-charge the cached portion.
	cached := out.Usage.PromptTokensDetails.CachedTokens
	usage := Usage{
		Input:     out.Usage.PromptTokens - cached,
		CacheRead: cached,
		Output:    out.Usage.CompletionTokens,
	}

	if len(out.Choices) == 0 {
		return &Result{Usage: usage}, fmt.Errorf("llm: provider returned no choices")
	}
	// Same trap as Anthropic's max_tokens: a length-truncated response is
	// syntactically fine and simply contains fewer labels, which would silently
	// mark unlabelled tracks as done.
	if fr := out.Choices[0].FinishReason; fr == "length" {
		return &Result{Usage: usage}, fmt.Errorf(
			"llm: response truncated at max_tokens (%d); lower the batch size", o.maxTokens())
	}

	var wrapper struct {
		Labels []Label `json:"labels"`
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if err := json.Unmarshal([]byte(content), &wrapper); err != nil {
		return &Result{Usage: usage}, fmt.Errorf(
			"llm: provider did not return the expected JSON object (schema mode may be unsupported): %w", err)
	}
	return &Result{Labels: wrapper.Labels, Usage: usage}, nil
}
