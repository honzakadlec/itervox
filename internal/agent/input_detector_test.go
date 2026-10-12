package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/vnovick/itervox/internal/agent"
)

func TestDetectInputRequiredFallback_ChoicePrompt(t *testing.T) {
	decision := agent.DetectInputRequiredFallback(`Implementation complete. What would you like to do?

1. Merge back to main locally
2. Push and create a Pull Request
3. Keep the branch as-is (I'll handle it later)
4. Discard this work

Which option?`)

	assert.True(t, decision.NeedsInput)
	assert.Contains(t, decision.Question, "Which option?")
	assert.Contains(t, decision.Question, "1. Merge back to main locally")
	assert.Contains(t, decision.Reason, "asks the human to choose the next action")
}

func TestDetectInputRequiredFallback_ConfirmationPrompt(t *testing.T) {
	decision := agent.DetectInputRequiredFallback(`I found no commits on this branch.

Type "discard" to confirm.`)

	assert.True(t, decision.NeedsInput)
	assert.Equal(t, `Type "discard" to confirm.`, decision.Question)
	assert.Contains(t, decision.Reason, "asks for explicit confirmation or approval")
}

func TestDetectInputRequiredFallback_BlockingStatement(t *testing.T) {
	decision := agent.DetectInputRequiredFallback(`I need your decision before proceeding with the deploy step.`)

	assert.True(t, decision.NeedsInput)
	assert.Equal(t, "I need your decision before proceeding with the deploy step.", decision.Question)
	assert.Contains(t, decision.Reason, "states that the agent is waiting before it can continue")
}

func TestDetectInputRequiredFallback_NonBlockingQuestion(t *testing.T) {
	decision := agent.DetectInputRequiredFallback(`Implemented the requested change and ran tests. Can I help with anything else?`)

	assert.False(t, decision.NeedsInput)
	assert.Empty(t, decision.Question)
}

func TestDetectInputRequiredFallback_DoesNotMatchApprovedContinuing(t *testing.T) {
	decision := agent.DetectInputRequiredFallback(`Approved, continuing with the existing branch.`)

	assert.False(t, decision.NeedsInput)
	assert.Empty(t, decision.Question)
}

func TestDetectInputRequiredFallback_DoesNotMatchPostedApprovalNarration(t *testing.T) {
	decision := agent.DetectInputRequiredFallback(`Review done. PR clean — mechanical opt-in flag add, mirrors existing pattern exact, all call sites updated, build+tests pass. No bugs. Posted approval comment. Handoff written.`)

	assert.False(t, decision.NeedsInput)
	assert.Empty(t, decision.Question)
}

func TestDetectInputRequiredFallback_Empty(t *testing.T) {
	decision := agent.DetectInputRequiredFallback("")
	assert.False(t, decision.NeedsInput)
}

// CORE-136: bare "to continue" / "to proceed" are ordinary prose in a
// finished run's summary, not a request for a human reply. They must not
// halt a successful run on their own.
func TestDetectInputRequiredFallback_SuccessfulRunMentioningContinueIsNotBlocking(t *testing.T) {
	for _, output := range []string{
		`Refactored the retry loop so the worker is able to continue after a transient tracker error. All tests pass.`,
		`Updated the migration script to proceed even when the table already exists; CI is green.`,
		`Implemented the fix and pushed the branch.

The daemon now restarts the watcher to continue polling after a config reload.`,
	} {
		decision := agent.DetectInputRequiredFallback(output)
		assert.False(t, decision.NeedsInput, "successful run flagged as input-required: %q (reason %q)", output, decision.Reason)
		assert.Empty(t, decision.Question)
	}
}

// CORE-136: the fix must keep genuine "may I go on?" prompts that use the
// same words.
func TestDetectInputRequiredFallback_GenuineProceedQuestionStillBlocks(t *testing.T) {
	for _, output := range []string{
		`Should I proceed? Please confirm to continue.`,
		`The migration drops the legacy column. Ready to proceed?`,
		`I need your approval to proceed with the force-push.`,
		`The staging credentials are missing. I need permission to continue with the production deploy.`,
	} {
		decision := agent.DetectInputRequiredFallback(output)
		assert.True(t, decision.NeedsInput, "genuine prompt not detected: %q", output)
		assert.NotEmpty(t, decision.Question)
	}
}

// CORE-158: the bare "confirm" cue matched "confirmed" in a finished run's
// summary and halted a successful run as input-required, the same overmatch
// CORE-136 fixed for "to continue".
func TestDetectInputRequiredFallback_SuccessfulRunMentioningConfirmIsNotBlocking(t *testing.T) {
	for _, output := range []string{
		`I confirmed all tests pass and pushed the branch.`,
		`Confirmed the flaky test was a race in the fixture; fixed it and CI is green.`,
		`Added a confirmation step to the deploy script. All tests pass.`,
		`Implemented the fix and pushed the branch.

I ran the migration locally to confirm it is idempotent.`,
	} {
		decision := agent.DetectInputRequiredFallback(output)
		assert.False(t, decision.NeedsInput, "successful run flagged as input-required: %q (reason %q)", output, decision.Reason)
		assert.Empty(t, decision.Question)
	}
}

// CORE-158: the fix must keep genuine confirmation requests.
func TestDetectInputRequiredFallback_GenuineConfirmRequestStillBlocks(t *testing.T) {
	for _, output := range []string{
		`Please confirm whether I should delete the legacy tables?`,
		`Please confirm the target environment before I deploy.`,
		`Can you confirm this is the right branch to rebase onto?`,
		`Type "discard" to confirm.`,
		`I need your confirmation to force-push.`,
	} {
		decision := agent.DetectInputRequiredFallback(output)
		assert.True(t, decision.NeedsInput, "genuine confirmation request not detected: %q", output)
		assert.NotEmpty(t, decision.Question)
	}
}

// CORE-159: the bare "approval" cue scored the full two points, so a
// successful run's "Got approval and merged the PR." was flagged
// input-required. The cue sweep found the same overmatch in every other bare
// cue; each sentence below tripped the detector before the fix.
func TestDetectInputRequiredFallback_SuccessfulRunBareCuesAreNotBlocking(t *testing.T) {
	for _, output := range []string{
		// approval family (CORE-159)
		`Got approval and merged the PR.`,
		`Added an approval gate to the deploy workflow; CI is green.`,
		`The deploy job now requires approval from a maintainer. All tests pass.`,
		`The release PR needs approval from a second reviewer; I pushed the branch.`,
		`Added a confirmation required banner to the deploy page.`,
		// choice words used descriptively
		`Documented which one of the two retry paths runs first.`,
		`The README now explains which option to use in CI.`,
		`Added a debug log of which path the resolver takes.`,
		`Added a 'Select an option' placeholder to the profile dropdown.`,
		`The picker now lets users choose one repo at a time.`,
		// reply instructions used descriptively
		`The handler will now respond with a 404 for unknown issues.`,
		`The bot can now reply with the PR link after merging.`,
		`Changed the field type 'int' to 'int64' and regenerated the mocks.`,
		`Added a new type "OutboxEntry" and wired it into the flusher.`,
		`Implemented the approve this button on the review page.`,
		// continuation phrases used descriptively
		`Ran the full test suite before proceeding with the refactor. All green.`,
		`I rebased onto main before I proceeded with the fix; CI is green.`,
		`Stubbed the tracker so I can continue testing offline; all tests pass.`,
		`Wrapped the call so the worker has permission to proceed after a retry.`,
	} {
		decision := agent.DetectInputRequiredFallback(output)
		assert.False(t, decision.NeedsInput, "successful run flagged as input-required: %q (reason %q)", output, decision.Reason)
		assert.Empty(t, decision.Question)
	}
}

// CORE-159 guard: demoting bare cues must keep genuine requests that use the
// same words.
func TestDetectInputRequiredFallback_GenuineApprovalAndChoiceRequestsStillBlock(t *testing.T) {
	for _, output := range []string{
		`Awaiting your approval before I proceed — should I continue?`,
		`I need approval to force-push to main.`,
		`The deploy requires approval. Should I request it from the on-call?`,
		`Please approve the migration plan.`,
		`I need your permission to drop the legacy table.`,
		`Which option do you prefer?`,
		`Which path should I take for the auth refactor?`,
		`Pick one: rebase onto main, or merge main in.`,
		`Reply with yes or no.`,
		`Reply with 'yes' to proceed.`,
		`Before I proceed, can you confirm the target environment?`,
	} {
		decision := agent.DetectInputRequiredFallback(output)
		assert.True(t, decision.NeedsInput, "genuine request not detected: %q", output)
		assert.NotEmpty(t, decision.Question)
	}
}

// A finished run's summary is often one markdown bullet list. Any bullet list
// scored "includes reply options", so a single weak cue inside a bullet ("the
// CI job needs to confirm it") reached the threshold and parked a completed
// run as input-required (appserver#29). A list is reply options only when
// text outside it asks for something.
func TestDetectInputRequiredFallback_BulletedSummaryIsNotBlocking(t *testing.T) {
	for _, output := range []string{
		`Both MRs are open into the integration branch, and the old MRs are closed as superseded.

- **MRs:** appserver !722 and plugin !167, each with a merge-order section.
- **Go tests:** all named tests passed under -race.
- **Not verified locally:** I could not run the plugin unit test, so the CI unit-test job needs to confirm it.
- **Handoff:** written to .itervox/handoff/2026-10-09_implementer.md.`,
		`Both MRs are open into the integration branch.
- **Review:** got approval from the reviewer profile.
- **Checks:** go test and go vet pass.`,
		`Done.

1. Added the migration.
2. Ran it locally to confirm it is idempotent.
3. Pushed the branch.`,
	} {
		decision := agent.DetectInputRequiredFallback(output)
		assert.False(t, decision.NeedsInput, "successful run flagged as input-required: %q (reason %q)", output, decision.Reason)
		assert.Empty(t, decision.Question)
	}
}

// Guard: options with an ask outside the list, or a direct ask inside a
// bullet, still block.
func TestDetectInputRequiredFallback_GenuineOptionListsStillBlock(t *testing.T) {
	for _, output := range []string{
		`Which one should I use?
- Rotate the secret
- Extend the TTL`,
		`Two ways forward:

1. Rebase onto main
2. Merge main in

Which one?`,
		`Summary of the blocker:
- The migration drops a column.
- Should I run it against production?`,
	} {
		decision := agent.DetectInputRequiredFallback(output)
		assert.True(t, decision.NeedsInput, "genuine prompt not detected: %q", output)
		assert.NotEmpty(t, decision.Question)
	}
}
