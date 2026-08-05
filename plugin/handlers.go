package main

import (
	"fmt"

	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/taskworker"

	"github.com/312-dev/navidrome-mood/internal/mood"
)

// Queue names. The taskqueue host service is SQLite-backed per plugin and
// recovers tasks stuck in `running` after a restart, so these survive a Navidrome
// restart mid-pass.
const (
	queueLabel = "label"
)

// OnInit runs once when the plugin is loaded - notably NOT on hot reload, so it
// must not be the only place invariants are checked.
func (p *plugin) OnInit() error {
	// The vocabulary gate. A term containing , ; or / would be split by Navidrome
	// into multiple moods and fragment that mood across the whole library while
	// still looking like it worked. Refuse to load rather than write bad tags.
	if err := mood.Validate(); err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("navidrome-mood: %v", err))
		return err
	}
	pdk.Log(pdk.LogInfo, fmt.Sprintf(
		"navidrome-mood ready: %d mood terms, %d synonyms",
		len(mood.Vocabulary), len(mood.Synonyms)))
	return nil
}

// OnTaskExecute labels one batch of tracks. One invocation is one unit of work
// small enough to finish inside the 30-second ceiling.
func (p *plugin) OnTaskExecute(req taskworker.TaskExecuteRequest) (string, error) {
	switch req.QueueName {
	case queueLabel:
		return "", fmt.Errorf("not implemented: labelling batch %s (attempt %d)",
			req.TaskID, req.Attempt)
	default:
		return "", fmt.Errorf("unknown queue %q", req.QueueName)
	}
}

// OnCallback drives the batch poll loop and the prompted-playlist refresh.
func (p *plugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	return fmt.Errorf("not implemented: schedule %s (recurring=%v)",
		req.ScheduleID, req.IsRecurring)
}
