package llm

import (
	"encoding/json"
	"fmt"

	"github.com/312-dev/navidrome-mood/internal/mood"
)

// TimeSlots are the time-of-day buckets a track can be marked as fitting.
//
// Closed rather than free-form because the connector filters on the values
// exactly, so a slot it has no name for is a slot no playlist can ever ask for.
// The same list is shown to the model in prose by internal/prompt; this copy is
// what the request schema constrains it to.
var TimeSlots = []string{
	"early morning", "morning", "midday", "afternoon",
	"golden hour", "evening", "late night",
}

// labelSchemaJSON is built once at load. The schema is identical for every
// request, and rebuilding it per batch would re-marshal the enums on a path that
// already has a 30-second ceiling to stay inside.
var labelSchemaJSON = buildLabelSchema()

// labelSchema is the JSON Schema both providers constrain the reply with:
// Anthropic as a forced tool's input_schema, OpenAI-compatible as a
// response_format. Ranges are declared so the provider rejects out-of-band
// numbers itself rather than leaving Validate to catch every one of them.
func labelSchema() json.RawMessage { return labelSchemaJSON }

// buildLabelSchema generates the tempo and vocal enums from mood.TempoFeels and
// mood.VocalKinds rather than repeating the literals.
//
// Two copies of a closed set is how a request comes to ask for a value the
// validator will then reject: the request would keep working, the labels would
// keep arriving, and every one of them would land in the unresolved path.
func buildLabelSchema() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "labels": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id":           {"type": "string"},
          "energy":       {"type": "integer", "minimum": 0, "maximum": 100},
          "valence":      {"type": "integer", "minimum": 0, "maximum": 100},
          "intensity":    {"type": "integer", "minimum": 0, "maximum": 100},
          "acousticness": {"type": "integer", "minimum": 0, "maximum": 100},
          "density":      {"type": "integer", "minimum": 0, "maximum": 100},
          "tempo":        {"type": "string", "enum": %s},
          "vocal":        {"type": "string", "enum": %s},
          "moods":        {"type": "array", "minItems": 2, "maxItems": 4,
                           "items": {"type": "string"},
                           "description": "Short lowercase feeling descriptors."},
          "times":        {"type": "array",
                           "items": {"type": "string", "enum": %s}}
        },
        "required": ["id","energy","valence","intensity","acousticness","density","tempo","vocal","moods"]
      }
    }
  },
  "required": ["labels"]
}`, enumJSON(mood.TempoFeels), enumJSON(mood.VocalKinds), enumJSON(TimeSlots)))
}

func enumJSON[T ~string](vals []T) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	b, err := json.Marshal(out)
	if err != nil {
		// Marshalling a []string cannot fail; a panic here would mean the runtime
		// is broken, and a silently empty enum would constrain nothing.
		panic("llm: cannot marshal schema enum: " + err.Error())
	}
	return string(b)
}
