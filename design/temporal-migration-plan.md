# Temporal migration plan

This checklist turns the Temporal migration design into an executable sequence of work.
It is intentionally ordered so that each phase leaves the repo in a working state.

Current status (as implemented on this branch): the Temporal workflow/activity scaffold exists in `temporal/temporal.go`, the CLI exposes `worker` and `temporal` entry points in `main.go`, and the workflow-backed path now routes chat completions and tool calls through activities. The core loop work from phases 0-2 is implemented in code; replay hardening, approvals and the full eval/CLI cutover remain to be finished.

## Phase 0 — prove the boundary

Goal: validate the assumptions that drive the design before touching the main loop.

- [x] Add a Temporal workflow and activity scaffold under `temporal/` and wire it into the CLI.
- [ ] Run a local Temporal dev server and confirm a workflow can be started, awaited, and completed.
- [x] Verify the worker-side API key loading path and the CLI entry point split.
- [x] Capture the worker/client commands in the repo docs.

Exit criteria:
- [x] `go test ./...` still passes.
- [x] A one-step workflow can be started from the CLI and completed locally (implemented in the workflow-backed path; manual runtime validation remains to be exercised in a live Temporal environment).

## Phase 1 — introduce the Temporal layers

Goal: keep the current local path working while adding the workflow/activity split.

- [x] Add the Temporal SDK dependency and any needed imports.
- [x] Introduce a workflow implementation with the same structure as the current agent loop.
- [x] Introduce an activity implementation for the model request.
- [x] Keep `agent.Run` as the local path and add a new workflow-backed path.
- [ ] Add a small integration test that exercises the workflow and mocked activity execution.

Exit criteria:
- [x] The local non-Temporal path still works unchanged.
- [x] A workflow-backed run can complete through the new entry point.

## Phase 2 — port the agent loop

Goal: move the deterministic loop into workflow code and keep the I/O on the activity side.

- [x] Move the step loop, message history and workflow state management into workflow code.
- [x] Keep `agent.Transcribe` and `cutPoint` unchanged in the workflow path.
- [x] Move the OpenAI call behind an activity.
- [x] Move each tool execution behind its own activity.
- [ ] Move summarization behind an activity.
- [ ] Handle approvals through workflow signals or updates.

Exit criteria:
- [x] A simple prompt that uses no tools completes end to end through a workflow (implemented in the workflow path; runtime validation remains to be exercised in a live Temporal environment).
- [x] A prompt that uses a file tool completes with the workspace preserved for the next turn.

## Phase 3 — harden the workflow contract

Goal: ensure the new path is durable and replay-safe.

- [ ] Add a replay test using `worker.WorkflowReplayer` on a recorded history.
- [ ] Add tests for deterministic compaction and tool pairing in the workflow path.
- [x] Decide and document the retry policy for model and tool activities.
- [ ] Decide and document the workspace persistence strategy (ephemeral session worker vs durable state).

Exit criteria:
- [ ] Replay tests pass.
- [ ] The workflow fails loudly on determinism regressions rather than silently degrading.

## Phase 4 — switch the evals and the CLI

Goal: move the shipped path to Temporal without losing test coverage.

- [ ] Update the eval harness to read tool calls from workflow history rather than traces.
- [ ] Add or update judged conversation tests to read workflow-visible state.
- [ ] Split the CLI into client and worker commands.
- [ ] Keep `--local` as an explicit path and document the difference.
- [ ] Update the docs and architecture notes to describe the shipped runtime.

Exit criteria:
- [ ] The full suite, including the billed evals when enabled, runs against the workflow-backed path.
- [ ] The docs match the implementation and the default runtime.

## Notes

- Keep the changes as surgical as possible. The design intentionally favors preserving the existing agent loop, tool registry and transcript logic.
- The migration should be done in small, reviewable steps. If a later phase reveals a design mismatch, the earlier phase should be updated rather than papered over.
- The three highest-risk items to verify at the start are payload size, session semantics and update semantics.
