package orchestrator

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/vnovick/itervox/internal/workspace"
)

// hookQuestionFileEnv names the file a before_run hook writes its question to
// before exiting workspace.HookExitInputRequired.
const hookQuestionFileEnv = "ITERVOX_HOOK_QUESTION_FILE"

// maxHookDeferReasonBytes bounds the defer reason shown on the retry row.
const maxHookDeferReasonBytes = 500

// runBeforeRunHook runs the before_run hook with ITERVOX_HOOK_QUESTION_FILE
// set. It returns the hook error unchanged plus, for an
// workspace.HookExitInputRequired exit, the question the hook asked.
func (o *Orchestrator) runBeforeRunHook(ctx context.Context, script, wsPath string, timeoutMs int, identifier, runLogID, profileName string) (string, error) {
	if strings.TrimSpace(script) == "" {
		return "", nil
	}
	env := hookRunEnv(identifier, runLogID, profileName, "")
	questionFile, err := os.CreateTemp("", "itervox-hook-question-*")
	if err == nil {
		_ = questionFile.Close()
		defer func() { _ = os.Remove(questionFile.Name()) }()
		env[hookQuestionFileEnv] = questionFile.Name()
	}
	hookErr := workspace.RunHookWithEnv(ctx, script, wsPath, timeoutMs, env, o.hookLogFn(identifier, runLogID))
	if workspace.HookExitCode(hookErr) != workspace.HookExitInputRequired {
		return "", hookErr
	}
	question := ""
	if questionFile != nil {
		if b, readErr := os.ReadFile(questionFile.Name()); readErr == nil {
			question = strings.TrimSpace(string(b))
		}
	}
	if question == "" {
		var exitErr *workspace.HookExitError
		if errors.As(hookErr, &exitErr) {
			question = trimInputRequiredContext(exitErr.Output)
		}
	}
	if question == "" {
		question = "The before_run hook needs input but did not say what."
	}
	return question, hookErr
}

// hookDeferReason is the last non-empty output line of a deferring hook,
// bounded for the retry row. Hooks log progress first and print why they
// defer just before exiting.
func hookDeferReason(hookErr error) string {
	var exitErr *workspace.HookExitError
	if errors.As(hookErr, &exitErr) {
		lines := strings.Split(exitErr.Output, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if line := strings.TrimSpace(lines[i]); line != "" {
				if len(line) > maxHookDeferReasonBytes {
					line = line[:maxHookDeferReasonBytes]
				}
				return line
			}
		}
	}
	return "before_run hook deferred the run"
}
