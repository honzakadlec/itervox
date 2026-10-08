package tracker

import (
	"regexp"
	"strings"
)

var (
	// inlineBlockerRe matches "Blocked by #12" anywhere in the body.
	inlineBlockerRe = regexp.MustCompile(`(?i)blocked\s+by\s+#(\d+)`)
	// blockerHeadingRe matches a Markdown heading that opens a blocker list,
	// e.g. "## Blocked by" or "### Depends on:".
	blockerHeadingRe = regexp.MustCompile(`(?i)^#{1,6}\s+(?:blocked\s+by|depends\s+on)\s*:?\s*$`)
	headingRe        = regexp.MustCompile(`^#{1,6}\s`)
	// listItemRe matches a list item and captures a leading "#12" reference.
	listItemRe = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+(?:\[[ xX]\]\s+)?(?:#(\d+))?`)
)

// ParseHashBlockers returns the issue numbers an issue body declares as
// blockers, in first-seen order without duplicates. Two forms are recognised:
//
//   - inline "Blocked by #12" anywhere in the body;
//   - a "## Blocked by" / "## Depends on" heading followed by list items
//     that start with "#12". The section ends at the next heading or the
//     first non-blank line that is not a list item.
func ParseHashBlockers(body string) []string {
	var result []string
	seen := map[string]bool{}
	add := func(num string) {
		if num != "" && !seen[num] {
			seen[num] = true
			result = append(result, num)
		}
	}

	for _, m := range inlineBlockerRe.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}

	inSection := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case blockerHeadingRe.MatchString(line):
			inSection = true
		case !inSection || line == "":
		case headingRe.MatchString(line):
			inSection = false
		default:
			m := listItemRe.FindStringSubmatch(line)
			if m == nil {
				inSection = false
				continue
			}
			add(m[1])
		}
	}
	return result
}
