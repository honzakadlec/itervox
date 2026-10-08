package orchestrator

import (
	"log/slog"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// profileLabelPrefix marks a tracker label that selects the issue's agent
// profile: "profile::tester" dispatches the issue with profile "tester". On
// GitLab the "::" makes it a scoped label, so an issue carries at most one.
const profileLabelPrefix = "profile::"

// profileFromLabels returns the first profile named by a profile::<name>
// label that exists and is enabled. Matching is case-insensitive because
// trackers lower-case labels (GitLab) while profile names keep their case.
// Labels naming an unknown or disabled profile are logged and skipped.
func profileFromLabels(identifier string, labels []string, profiles map[string]config.AgentProfile) string {
	return matchProfileLabel(labels, profiles, func(label, reason string) {
		slog.Warn("orchestrator: profile label names "+reason+" profile, ignoring",
			"identifier", identifier, "label", label)
	})
}

// LabelProfile is profileFromLabels without logging, for read paths (the
// dashboard) that resolve the label on every request.
func LabelProfile(labels []string, profiles map[string]config.AgentProfile) string {
	return matchProfileLabel(labels, profiles, func(string, string) {})
}

func matchProfileLabel(labels []string, profiles map[string]config.AgentProfile, skipped func(label, reason string)) string {
	for _, label := range labels {
		if len(label) <= len(profileLabelPrefix) || !strings.EqualFold(label[:len(profileLabelPrefix)], profileLabelPrefix) {
			continue
		}
		want := strings.TrimSpace(label[len(profileLabelPrefix):])
		name, profile, ok := lookupProfileFold(profiles, want)
		switch {
		case !ok:
			skipped(label, "unknown")
		case !config.ProfileEnabled(profile):
			skipped(label, "disabled")
		default:
			return name
		}
	}
	return ""
}

func lookupProfileFold(profiles map[string]config.AgentProfile, want string) (string, config.AgentProfile, bool) {
	if p, ok := profiles[want]; ok {
		return want, p, true
	}
	for name, p := range profiles {
		if strings.EqualFold(name, want) {
			return name, p, true
		}
	}
	return "", config.AgentProfile{}, false
}
