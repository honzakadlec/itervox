package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunHookThreadsItervoxBinIntoSubprocess(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "captured.txt")
	t.Setenv("ITERVOX_BIN", "/path/to/dev-build/itervox")

	script := "printenv ITERVOX_BIN > " + outPath
	if err := RunHook(context.Background(), script, tmp, 5000); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read %s: %v", outPath, err)
	}
	got := strings.TrimSpace(string(data))
	if got != "/path/to/dev-build/itervox" {
		t.Errorf("hook saw ITERVOX_BIN=%q, want /path/to/dev-build/itervox", got)
	}
}

// TestRunHookDropsInheritedGitDir: a hook script that runs git must act on
// the workspace, not on the repository an inherited GIT_DIR names.
func TestRunHookDropsInheritedGitDir(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "captured.txt")
	t.Setenv("GIT_DIR", "/victim/.git")
	t.Setenv("GIT_WORK_TREE", "/victim")

	script := `printf '%s|%s' "${GIT_DIR-unset}" "${GIT_WORK_TREE-unset}" > ` + outPath
	if err := RunHook(context.Background(), script, tmp, 5000); err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read %s: %v", outPath, err)
	}
	if got := string(data); got != "unset|unset" {
		t.Errorf("hook saw GIT_DIR|GIT_WORK_TREE = %q, want unset|unset", got)
	}
}

func TestHookEnvOverridesInheritedItervoxBin(t *testing.T) {
	t.Setenv("ITERVOX_BIN", "/correct/value")
	base := []string{
		"PATH=/usr/bin",
		"ITERVOX_BIN=/stale/inherited/value",
		"HOME=/tmp",
	}
	env := hookEnv(base)
	var found string
	for _, kv := range env {
		if strings.HasPrefix(kv, "ITERVOX_BIN=") {
			found = kv
		}
	}
	if found != "ITERVOX_BIN=/correct/value" {
		t.Errorf("expected daemon ITERVOX_BIN to override inherited; got %q", found)
	}
}

func TestHookEnvOmitsItervoxBinWhenUnset(t *testing.T) {
	t.Setenv("ITERVOX_BIN", "")
	base := []string{"PATH=/usr/bin", "HOME=/tmp"}
	env := hookEnv(base)
	for _, kv := range env {
		if strings.HasPrefix(kv, "ITERVOX_BIN=") {
			t.Errorf("ITERVOX_BIN should not be set; got %q", kv)
		}
	}
}

func TestRunHookReceivesIssueIdentifier(t *testing.T) {
	tmp := t.TempDir()
	outPath := filepath.Join(tmp, "captured.txt")
	// A stale inherited value must lose to the per-run value.
	t.Setenv("ITERVOX_ISSUE_IDENTIFIER", "STALE-1")

	script := `printf '%s|%s' "$ITERVOX_ISSUE_IDENTIFIER" "$ITERVOX_RUN_ID" > ` + outPath
	env := map[string]string{
		"ITERVOX_ISSUE_IDENTIFIER": "ENG-42",
		"ITERVOX_RUN_ID":           "run-abc",
	}
	if err := RunHookWithEnv(context.Background(), script, tmp, 5000, env); err != nil {
		t.Fatalf("RunHookWithEnv: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read %s: %v", outPath, err)
	}
	if got := string(data); got != "ENG-42|run-abc" {
		t.Errorf("hook saw %q, want ENG-42|run-abc", got)
	}
}

func TestWithExtraEnvSkipsEmptyValues(t *testing.T) {
	base := []string{"PATH=/usr/bin", "ITERVOX_RUN_ID=old"}
	env := withExtraEnv(base, map[string]string{"ITERVOX_RUN_ID": "", "ITERVOX_ISSUE_IDENTIFIER": "ENG-1"})
	want := []string{"PATH=/usr/bin", "ITERVOX_RUN_ID=old", "ITERVOX_ISSUE_IDENTIFIER=ENG-1"}
	if strings.Join(env, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", env, want)
	}
}
