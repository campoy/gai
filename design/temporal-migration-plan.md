# Temporal migration plan

This checklist turns the Temporal migration design into an executable sequence of work.
It is intentionally ordered so that each phase leaves the repo in a working state.

## Phase 0 — prove the boundary

Goal: validate the assumptions that drive the design before touching the main loop.

- [ ] Add a tiny Temporal workflow and activity scaffold under `cmd/worker`/`cmd/gai`.
- [ ] Run a local Temporal dev server and confirm a workflow can be started, awaited, and completed.
- [ ] Verify the worker-side API key loading path and the CLI entry point split.
- [ ] Capture the worker/client commands in the repo docs.

Exit criteria:
- `go test ./...` still passes.
- A one-step workflow can be started from the CLI and completed locally.

## Phase 1 — introduce the Temporal layers

Goal: keep the current local path working while adding the workflow/activity split.

- [ ] Add the Temporal SDK dependency and any needed imports.
- [ ] Introduce a workflow implementation with the same structure as the current agent loop.
- [ ] Introduce an activity implementation for the model request.
- [ ] Keep `agent.Run` as the `--local` path and add a new workflow-backed path.
- [ ] Add a small integration test that exercises the workflow and mocked activity execution.

Exit criteria:
- The local non-Temporal path still works unchanged.
- A workflow-backed run can complete through the new entry point.

## Phase 2 — port the agent loop

Goal: move the deterministic loop into workflow code and keep the I/O on the activity side.

- [ ] Move the step loop, message history, compaction decision and workflow state management into workflow code.
- [ ] Keep `agent.Transcribe` and `cutPoint` unchanged in the workflow path.
- [ ] Move the OpenAI call behind an activity.
- [ ] Move each tool execution behind its own activity.
- [ ] Move summarization behind an activity.
- [ ] Handle approvals through workflow signals or updates.

Exit criteria:
- A simple prompt that uses no tools completes end to end through a workflow.
- A prompt that uses a file tool completes with the workspace preserved for the next turn.

## Phase 3 — harden the workflow contract

Goal: ensure the new path is durable and replay-safe.

- [ ] Add a replay test using `worker.WorkflowReplayer` on a recorded history.
- [ ] Add tests for deterministic compaction and tool pairing in the workflow path.
- [ ] Decide and document the retry policy for model and tool activities.
- [ ] Decide and document the workspace persistence strategy (ephemeral session worker vs durable state).

Exit criteria:
- Replay tests pass.
- The workflow fails loudly on determinism regressions rather than silently degrading.

## Phase 4 — switch the evals and the CLI

Goal: move the shipped path to Temporal without losing test coverage.

- [ ] Update the eval harness to read tool calls from workflow history rather than traces.
- [ ] Add or update judged conversation tests to read workflow-visible state.
- [ ] Split the CLI into client and worker commands.
- [ ] Keep `--local` as an explicit path and document the difference.
- [ ] Update the docs and architecture notes to describe the shipped runtime.

Exit criteria:
- The full suite, including the billed evals when enabled, runs against the workflow-backed path.
- The docs match the implementation and the default runtime.

## Notes

- Keep the changes as surgical as possible. The design intentionally favors preserving the existing agent loop, tool registry and transcript logic.
- The migration should be done in small, reviewable steps. If a later phase reveals a design mismatch, the earlier phase should be updated rather than papered over.
- The three highest-risk items to verify at the start are payload size, session semantics and update semantics.
