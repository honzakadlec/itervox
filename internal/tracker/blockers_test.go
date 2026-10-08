package tracker

import (
	"reflect"
	"testing"
)

func TestParseHashBlockers(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{name: "empty", body: "", want: nil},
		{name: "inline", body: "Blocked by #10", want: []string{"10"}},
		{
			name: "inline multiple",
			body: "This is blocked by #10 and also blocked by #20.",
			want: []string{"10", "20"},
		},
		{
			name: "heading with bullets",
			body: "## What to build\n\nStuff.\n\n## Blocked by\n\n- #13\n- #14 (route gate)\n\n<details>see #99</details>\n",
			want: []string{"13", "14"},
		},
		{
			name: "depends on heading, mixed bullet styles",
			body: "### Depends on:\n* #3\n+ #4\n1. #5\n- [ ] #6\n",
			want: []string{"3", "4", "5", "6"},
		},
		{
			name: "section ends at next heading",
			body: "## Blocked by\n- #1\n## Notes\n- #2\n",
			want: []string{"1"},
		},
		{
			name: "section ends at first non-list line",
			body: "## Blocked by\n\n- #1\n\nSee also #2.\n- #3\n",
			want: []string{"1"},
		},
		{
			name: "none bullet",
			body: "## Blocked by\n\n- None — can start immediately\n",
			want: nil,
		},
		{
			name: "mentions outside section ignored",
			body: "Related to #7.\n\n- #8\n",
			want: nil,
		},
		{
			name: "dedupes inline and section",
			body: "Blocked by #13\n\n## Blocked by\n\n- #13\n- Blocked by #14\n",
			want: []string{"13", "14"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseHashBlockers(tt.body)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseHashBlockers() = %v, want %v", got, tt.want)
			}
		})
	}
}
