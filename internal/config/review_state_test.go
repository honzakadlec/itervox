package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

func TestReviewStateParsedFromYAML(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, minimalV2("  completion_state: Done\n  review_state: \" Review \"\n")))
	require.NoError(t, err)
	assert.Equal(t, "Review", cfg.Tracker.ReviewState)
}

func TestReviewStateDefaultsToEmpty(t *testing.T) {
	cfg, err := config.Load(workflowWithContent(t, minimalV2("")))
	require.NoError(t, err)
	assert.Empty(t, cfg.Tracker.ReviewState)
}

func TestValidateReviewState(t *testing.T) {
	base := config.TrackerConfig{
		ActiveStates:    []string{"todo", "in-progress"},
		TerminalStates:  []string{"done", "cancelled"},
		CompletionState: "done",
	}
	cases := []struct {
		name    string
		mutate  func(*config.TrackerConfig)
		wantErr string
	}{
		{name: "empty is off", mutate: func(*config.TrackerConfig) {}},
		{name: "valid", mutate: func(tr *config.TrackerConfig) { tr.ReviewState = "review" }},
		{name: "requires completion_state", mutate: func(tr *config.TrackerConfig) {
			tr.ReviewState = "review"
			tr.CompletionState = ""
		}, wantErr: "requires tracker.completion_state"},
		{name: "equals completion_state", mutate: func(tr *config.TrackerConfig) { tr.ReviewState = "Done" }, wantErr: "must differ from tracker.completion_state"},
		{name: "active", mutate: func(tr *config.TrackerConfig) { tr.ReviewState = "In-Progress" }, wantErr: "tracker.active_states"},
		{name: "terminal", mutate: func(tr *config.TrackerConfig) {
			tr.ReviewState = "cancelled"
		}, wantErr: "tracker.terminal_states"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := base
			tc.mutate(&tr)
			err := config.ValidateReviewState(tr)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
