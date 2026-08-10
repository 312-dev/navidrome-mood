// This file is package llm_test rather than package llm so it can import
// internal/prompt without risking an import cycle: an external test package is
// allowed to depend on a package that depends on the one under test.
package llm_test

import (
	"strings"
	"testing"

	"github.com/312-dev/navidrome-mood/internal/llm"
	"github.com/312-dev/navidrome-mood/internal/prompt"
)

// The prompt asks the model for time slots in prose and the request schema
// constrains it to an enum. Two lists, one meaning: if they diverge, the prompt
// keeps asking for a slot the schema will refuse, and the failure shows up as
// nothing more than a `times` field that quietly stops arriving.
func TestTimeSlotsMatchTheOnesThePromptAsksFor(t *testing.T) {
	if strings.Join(llm.TimeSlots, "|") != strings.Join(prompt.TimeSlots, "|") {
		t.Fatalf("llm.TimeSlots = %v, prompt.TimeSlots = %v", llm.TimeSlots, prompt.TimeSlots)
	}
}
