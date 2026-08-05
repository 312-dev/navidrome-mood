package llm

import (
	"encoding/json"
	"fmt"
)

// DefaultAnthropicModel is deliberately not the most capable one.
//
// The original 9,311-track run used claude-opus-5 and cost $27.22. Sonnet does
// this job well - it is description, not reasoning - at a fraction of that. A
// public plugin should not default to the most expensive option and let people
// discover the bill afterwards.
const DefaultAnthropicModel = "claude-sonnet-5"

const (
	anthropicURL     = "https://api.anthropic.com/v1/messages"
	anthropicVersion = "2023-06-01"
	// labelToolName is the tool the model is forced to call. Forcing a tool call
	// is what makes the output schema-constrained rather than prose that happens
	// to look like JSON.
	labelToolName = "emit_labels"
)

type Anthropic struct {
	Doer   Doer
	APIKey string
	Model  string
	// MaxTokens caps one batch. http_send cannot stream and Navidrome kills any
	// invocation at 30s, so this must stay small enough that the request finishes
	// inside that window with the whole response in hand.
	MaxTokens int
}

func (a *Anthropic) Name() string { return "anthropic" }

// SupportsBatch reports true because Anthropic offers a 50%-off async batch API.
// The synchronous path here is what the batch path submits; wiring the batch
// endpoint itself is separate work.
func (a *Anthropic) SupportsBatch() bool { return true }

func (a *Anthropic) model() string {
	if a.Model != "" {
		return a.Model
	}
	return DefaultAnthropicModel
}

func (a *Anthropic) maxTokens() int {
	if a.MaxTokens > 0 {
		return a.MaxTokens
	}
	// ~96 output tokens per track measured, plus headroom for the tool-call
	// envelope. A batch of 20 needs well under 4k.
	return 4096
}

type anthropicReq struct {
	Model      string             `json:"model"`
	MaxTokens  int                `json:"max_tokens"`
	System     []anthropicSysPart `json:"system,omitempty"`
	Messages   []anthropicMsg     `json:"messages"`
	Tools      []anthropicTool    `json:"tools,omitempty"`
	ToolChoice *anthropicChoice   `json:"tool_choice,omitempty"`
	// Streaming is unavailable: the host returns complete responses only.
	Stream bool `json:"stream"`
}

type anthropicSysPart struct {
	Type         string             `json:"type"`
	Text         string             `json:"text"`
	CacheControl *anthropicCacheCtl `json:"cache_control,omitempty"`
}

type anthropicCacheCtl struct {
	Type string `json:"type"`
}

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type anthropicResp struct {
	Content []struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func (a *Anthropic) Label(system string, tracks []Track) (*Result, error) {
	if len(tracks) == 0 {
		return &Result{}, nil
	}
	payload, err := json.Marshal(tracks)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(anthropicReq{
		Model:     a.model(),
		MaxTokens: a.maxTokens(),
		System: []anthropicSysPart{{
			Type: "text",
			Text: system,
			// The system prompt carries the taxonomy and is byte-identical across
			// every batch, so caching it is worth roughly a fifth of the total
			// pass. Writes cost 1.25x and reads 0.1x - see Cost.
			CacheControl: &anthropicCacheCtl{Type: "ephemeral"},
		}},
		Messages: []anthropicMsg{{Role: "user", Content: string(payload)}},
		Tools: []anthropicTool{{
			Name:        labelToolName,
			Description: "Return one label per track, in the same order as the input.",
			InputSchema: labelSchema(),
		}},
		ToolChoice: &anthropicChoice{Type: "tool", Name: labelToolName},
		Stream:     false,
	})
	if err != nil {
		return nil, err
	}

	resp, err := a.Doer.Do(Request{
		Method: "POST",
		URL:    anthropicURL,
		Headers: map[string]string{
			"content-type":      "application/json",
			"x-api-key":         a.APIKey,
			"anthropic-version": anthropicVersion,
		},
		Body:      body,
		TimeoutMs: 25_000, // under Navidrome's 30s invocation ceiling
	})
	if err != nil {
		return nil, err
	}
	if err := checkResponse(a.Name(), resp); err != nil {
		return nil, err
	}

	var out anthropicResp
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("llm: anthropic response was not JSON: %w", err)
	}

	usage := Usage{
		Input:      out.Usage.InputTokens,
		Output:     out.Usage.OutputTokens,
		CacheWrite: out.Usage.CacheCreationInputTokens,
		CacheRead:  out.Usage.CacheReadInputTokens,
	}

	// max_tokens truncation yields a syntactically valid response holding a
	// partial tool call. Without this check it reads as "the model labelled fewer
	// tracks", and those tracks would be silently skipped and never retried.
	if out.StopReason == "max_tokens" {
		return &Result{Usage: usage}, fmt.Errorf(
			"llm: anthropic hit max_tokens (%d) mid-batch; lower the batch size",
			a.maxTokens())
	}

	for _, c := range out.Content {
		if c.Type != "tool_use" || c.Name != labelToolName {
			continue
		}
		var wrapper struct {
			Labels []Label `json:"labels"`
		}
		if err := json.Unmarshal(c.Input, &wrapper); err != nil {
			return &Result{Usage: usage}, fmt.Errorf("llm: could not parse tool input: %w", err)
		}
		return &Result{Labels: wrapper.Labels, Usage: usage}, nil
	}
	return &Result{Usage: usage}, fmt.Errorf(
		"llm: anthropic returned no %s tool call (stop_reason=%s)", labelToolName, out.StopReason)
}

// labelSchema constrains the model's output. Ranges are declared so the provider
// rejects out-of-band numbers rather than leaving the plugin to sanitise them.
func labelSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "labels": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id":        {"type": "string"},
          "energy":    {"type": "integer", "minimum": 0, "maximum": 100},
          "valence":   {"type": "integer", "minimum": 0, "maximum": 100},
          "intensity": {"type": "integer", "minimum": 0, "maximum": 100},
          "organic":   {"type": "integer", "minimum": 0, "maximum": 100},
          "moods":     {"type": "array", "minItems": 2, "maxItems": 4,
                        "items": {"type": "string"},
                        "description": "Short lowercase feeling descriptors."},
          "times":     {"type": "array", "items": {"type": "string"}}
        },
        "required": ["id","energy","valence","intensity","organic","moods"]
      }
    }
  },
  "required": ["labels"]
}`)
}
