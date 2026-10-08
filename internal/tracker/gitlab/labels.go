package gitlab

import (
	"context"
	"fmt"
	"net/http"
)

// defaultLabelColor is used for any status::* label itervox auto-creates.
// Purely cosmetic — GitLab requires a color on label creation.
const defaultLabelColor = "#428BCA"

// EnsureStatusLabels idempotently creates any status::<state> scoped label
// (for active, terminal, backlog, and review states) missing from the GitLab
// project. Called both by "itervox init" (immediate feedback on project
// setup) and on daemon startup (self-heal if labels were deleted later).
// Safe to call repeatedly — a no-op once all labels exist.
func (c *Client) EnsureStatusLabels(ctx context.Context) error {
	wanted := make(map[string]struct{})
	for _, s := range c.cfg.ActiveStates {
		wanted[statusLabel(s)] = struct{}{}
	}
	for _, s := range c.cfg.TerminalStates {
		wanted[statusLabel(s)] = struct{}{}
	}
	for _, s := range c.cfg.BacklogStates {
		wanted[statusLabel(s)] = struct{}{}
	}
	if c.cfg.ReviewState != "" {
		wanted[statusLabel(c.cfg.ReviewState)] = struct{}{}
	}
	if len(wanted) == 0 {
		return nil
	}

	existing, err := c.fetchAllLabelNames(ctx)
	if err != nil {
		return fmt.Errorf("gitlab_ensure_status_labels: list labels: %w", err)
	}

	for name := range wanted {
		if _, ok := existing[name]; ok {
			continue
		}
		if err := c.createLabel(ctx, name); err != nil {
			return fmt.Errorf("gitlab_ensure_status_labels: create %q: %w", name, err)
		}
	}
	return nil
}

// fetchAllLabelNames lists every label defined on the configured project, paginated.
func (c *Client) fetchAllLabelNames(ctx context.Context) (map[string]struct{}, error) {
	names := make(map[string]struct{})
	nextURL := c.projectURL(fmt.Sprintf("/labels?per_page=%d", pageSize))
	for nextURL != "" {
		body, linkHeader, err := c.get(ctx, nextURL)
		if err != nil {
			return nil, err
		}
		rawItems, ok := body.([]any)
		if !ok {
			return nil, fmt.Errorf("gitlab_unknown_payload: expected array response")
		}
		for _, item := range rawItems {
			label, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if name, ok := label["name"].(string); ok && name != "" {
				names[name] = struct{}{}
			}
		}
		next, err := ParseNextLink(linkHeader)
		if err != nil || next == "" {
			break
		}
		nextURL = next
	}
	return names, nil
}

// createLabel creates a single project label with the default itervox color.
func (c *Client) createLabel(ctx context.Context, name string) error {
	payload := map[string]string{"name": name, "color": defaultLabelColor}
	_, err := c.doJSON(ctx, http.MethodPost, c.projectURL("/labels"), payload)
	return err
}
