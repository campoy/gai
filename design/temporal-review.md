# Temporal path review

A review of `temporal/temporal.go` and the `worker` / `temporal` entry points in
`main.go` against the Temporal Go SDK's rules and this repo's own conventions.
Scope is the workflow-backed path only; the local `agent.Run` path is unchanged
and is not under review here.

The good news first: **the workflow body itself is deterministic.** No clock, no
randomness, no I/O, no bare goroutines, no map iteration where order matters.
`assistant.ToolCalls` is a slice, so iteration order is stable across replay. The
split between orchestration and I/O is drawn in the right place. Everything below
is about what happens around that split, not the split itself.

Findings are ordered by how much they hurt, not by how hard they are to fix.

## Blocking

### 1. Concurrent activities corrupt each other's workspace

`tools.SetWorkspace` writes the package-level `workspace` string in
[tools/file.go:43-52](../tools/file.go#L43-L52); `resolve` reads it on every file
tool call. Both `ChatCompletionActivity` and `RunToolActivity` call
`SetWorkspace` on entry ([temporal/temporal.go:162](../temporal/temporal.go#L162),
[:189](../temporal/temporal.go#L189)).

`worker.Options{}` is empty, so the worker runs with the SDK default of **1000
concurrent activity slots** (`defaultMaxConcurrentActivityExecutionSize`,
`internal/internal_worker.go:55`). Any two concurrent workflow executions — two
CLI invocations, or a retry overlapping its original — race on that one string.
Two consequences, the second worse than the first:

- An unsynchronised write/read of a package-level variable across goroutines: a
  genuine data race that `go test -race` would flag if anything exercised it.
- A tool call from run A resolving its path against run B's directory. The
  sandbox invariant still holds — `resolve` still rejects absolute paths and
  escapes — but it is guarding the wrong directory. Run A reads and writes run
  B's files.

AGENTS.md already names this pattern and rejects it: *"A tool needing outside
state closes over it at construction — `NewWebSearch(client)` — rather than
reading a package-level variable set by an initializer."* `SetWorkspace` is
exactly the initializer that convention exists to prevent. It predates the
Temporal work and is harmless in the single-run CLI; concurrency is what makes it
a bug.

**Fix:** thread the workspace through explicitly, the way the client already is.
Give the file tools a workspace field and build the tool set per activity
invocation, so the directory travels with the call rather than living in package
state.

Separately: `ChatCompletionActivity` calls `SetWorkspace` and then never touches
the filesystem. That call does nothing but widen the race — delete it.

### 2. The workspace path means nothing across processes

`runTemporal` creates a temp directory **in the client process**
([main.go:124](../main.go#L124)), passes its path as a workflow argument, and
deletes it on return. The activity runs on the worker, where `SetWorkspace` does
`os.MkdirAll` and silently creates a *different* directory that merely happens to
share the path string.

That works only when exactly one worker is co-located with the client. Otherwise:

- Client and worker on different hosts: the client deletes an empty directory it
  created, and the real workspace is stranded on the worker.
- Two workers on the queue: turn 2's tool call can land on a different host from
  turn 1's write and find an empty workspace. The migration plan's Phase 2 exit
  criterion — *"a prompt that uses a file tool completes with the workspace
  preserved for the next turn"* — holds by luck of scheduling, not by design.
- Nothing on the worker side ever cleans up. Every run leaks a directory, and the
  `os.MkdirAll` inside `SetWorkspace` means the leak is silent.

This is Phase 3's open *"decide and document the workspace persistence
strategy"* checkbox. The code has an implicit answer already; it should be an
explicit one. The three honest options are a session worker pinned per
conversation (task-queue-per-run), a durable store with the workflow passing a
key rather than a path (the claim-check pattern), or documenting single-worker
co-location as a hard constraint. Any of them is fine. Silence is not.

### 3. Permanent failures are retried

`unknown tool %s`, `missing API key`, `no choices returned`, and every argument
error out of `json.Unmarshal` or `resolve` are returned as plain errors, so the
retry policy hits them 3× with backoff. A hallucinated tool name costs ~3s of
sleeping before the model gets told it was wrong; a missing API key burns the
full budget on every activity before failing an unrecoverable run.

Wrap them in `temporal.NewNonRetryableApplicationError`. The package already
imports `go.temporal.io/sdk/temporal` and uses it for nothing but the retry
policy struct.

### 4. Mutating tools are not idempotent, and delivery is at-least-once

If a `write_file` activity succeeds but its completion is lost to a partition,
Temporal runs it again. `write_file` refuses to clobber unless `overwrite=true`
([tools/file.go:145-152](../tools/file.go#L145-L152)) — deliberately, and
`evals/EVALS.md` records why. So attempt 2 fails with *"already exists"*, the
activity exhausts its attempts, and the workflow reports a tool error for an
operation that in fact succeeded. `delete_file` has the mirror problem: attempt 2
gets `os.Stat`'s "no such file".

The retry policy that protects the model call is actively harmful for the tools.
Two workable answers:

- Give `RunToolActivity` its own options with `MaximumAttempts: 1` and let the
  *model* retry through the loop. That is already how the local path treats tool
  failure — errors go back as text so the model can recover — and it keeps the
  file tools' semantics unchanged.
- Or make the file tools idempotent per attempt, which means unpicking the
  no-clobber rule that the evals exist to defend. Not worth it.

The first is the right call. It also means the two activities want different
options, which the single `defaultActivityOptions()` currently prevents.

### 5. Compaction is dead on this path

The workflow builds `messages` from scratch and grows it without bound.
`CompletionResult.Usage` is captured with a comment saying it is there *"so the
workflow can make compaction decisions if needed"* — and is then never read.
`agent.compact`, which AGENTS.md describes as the thing that *"keeps the
conversation affordable"*, does not run here at all. `compactAfter` is 8000
tokens; nothing on this path observes it.

Temporal makes this more than a cost problem. Every step ships the **entire**
history as an activity input, and Temporal records every activity input in Event
History permanently. With the 10-step cap and `read_file` returning up to 64 KiB
per call (`maxFileSize`), the largest single payload approaches the same order of
magnitude as the 2 MB per-payload limit, and total bytes recorded grow
quadratically in the step count. Compaction is not a nice-to-have here; it is
what keeps the payloads under the cap.

Moving summarization behind an activity is already Phase 2's last open box. Note
the ordering constraint it inherits: `cutPoint` must land on a user message
(AGENTS.md calls this "the load-bearing part"), and the compaction decision has
to be made in *workflow* code from the recorded `Usage` so it replays identically
— only the summarising model call belongs in the activity.

## Design

### 6. The workflow is a second implementation of the agent loop

AGENTS.md: *"Do not add a second implementation alongside one that already
exists — this happened once with the datetime tool."* `AgentWorkflow` re-derives
the step loop, the tool dispatch, the terminal condition, and the step cap
(`maxWorkflowSteps = 10`, duplicating `agent.maxSteps = 10` — two constants that
will drift).

It gets the important convention right: tool errors go back as tool messages
rather than failing the run ([temporal/temporal.go:150](../temporal/temporal.go#L150)),
matching `runTool`. But two things `agent.Run` does are missing:

- **Telemetry.** `agent.Run` opens an `agent` span, `StartLLM` per model call,
  `StartTool` per tool. The workflow path produces none of these. Beyond losing
  traces, this is what makes the path unevaluable: `evals/trajectory_test.go`
  scores trajectories by reading the run's own OpenTelemetry spans, so the eval
  suite cannot see a workflow run at all. The Go SDK ships an OTel interceptor
  (`contrib/opentelemetry`) that would cover the workflow and activity spans;
  the LLM and tool spans need the activities to open them.
- **Compaction**, as above.

The duplication is defensible as a migration stage — the plan says so — but it
should be temporary and the drift should be bounded. The narrower the workflow
body is, the less of `agent.Run` it has to restate.

### 7. Activity options are too coarse

One `defaultActivityOptions()` covers both activities, and it is thin:

- **No `ScheduleToCloseTimeout`.** A permanently unreachable dependency burns
  3 × 5 min ≈ 15 minutes of wall clock before the workflow learns about it.
- **No `HeartbeatTimeout` on `ChatCompletionActivity`.** The model call is the
  one thing here that can genuinely hang on a stalled connection, and a
  5-minute `StartToClose` is the only thing that catches it. A heartbeat turns
  a 5-minute stall into a few seconds.
- **5 minutes is wrong for most tools.** `read_file` and `current_datetime`
  should be measured in seconds. `web_search` is the only tool that makes a
  model call of its own and needs real headroom.

Finding 4 already forces these apart. Give each activity options that match what
it does.

### 8. Versioning — decide, then write it down

There is no `workflow.GetVersion` anywhere, and the migration plan has two
changes queued that alter the *sequence* of workflow API calls: a compaction
activity and approval signals. Either one breaks an execution running across the
deploy.

The honest read is that this may not matter: a `gai` run lasts seconds, so the
window where an execution spans a deploy is nearly empty. If that is the
intended posture, **say so in AGENTS.md** and the versioning obligation goes away
for the price of a sentence. If workflows are ever meant to be long-lived —
which approval signals imply, since they wait on a human — then `GetVersion` is
mandatory from the first change. What is not fine is leaving it undecided until
the first orphaned execution.

Related: `maxWorkflowSteps` and `agent.SystemPrompt` both feed workflow-side
decisions. Changing the prompt is safe (activity *inputs* are not re-validated on
replay); changing the step cap is not, if an execution is mid-loop.

### 9. Smaller things

- **Clients are never closed.** `NewWorker` dials a client and never returns or
  closes it ([temporal/temporal.go:87-96](../temporal/temporal.go#L87-L96));
  `runTemporal` does not close its own. Trivial for a CLI, a real leak in a
  long-lived worker.
- **`main` builds a workspace it doesn't use.** `tools.NewWorkspace()` runs at
  [main.go:57](../main.go#L57), before the subcommand switch, so `gai worker`
  creates a throwaway directory and sets the global — which every activity then
  immediately overwrites. Move it into the local path.
- **API key via `os.Setenv`.** `ConfigureAPIKey` mutates process environment
  ([temporal/temporal.go:106-111](../temporal/temporal.go#L106-L111)). It is not
  a determinism problem — it is read in activity code, which is unrestricted —
  but it is the same invisible-global pattern as the workspace, and it means the
  worker's key cannot be rotated or scoped per queue. Closing over the key when
  building the activity struct is the same fix as finding 1.
- **Workflow ID `gai-<UnixNano>`** is unique by construction, which switches off
  the deduplication Temporal would otherwise give for free: a CLI invocation
  retried after a network blip starts a second execution and a second billed
  run. Probably intended for a CLI — worth a comment saying so.
- **`Future.Get(ctx, …)` is passed the parent context** rather than
  `activityCtx` (lines [129](../temporal/temporal.go#L129) and
  [148](../temporal/temporal.go#L148)). Legal and harmless: the options were
  bound when the future was created. `activityCtx` reads more clearly.

## Testing

`temporal/temporal_test.go` asserts that `defaultActivityOptions()` returns the
constants it is constructed from. It restates the code rather than testing
behaviour, and would still pass if `AgentWorkflow` were deleted outright. Against
this repo's test culture — a compaction cut rule pinned down case by case, the
whole loop driven against an `httptest` stub — it is the weakest test in the
tree.

What is missing, and costs no API calls:

- A `testsuite.TestWorkflowEnvironment` test driving `AgentWorkflow` with both
  activities mocked — the direct analogue of `agent/run_test.go`. Cases worth
  pinning: an answer with no tool calls returns immediately; a tool call round
  trip produces a correctly paired `tool_calls` / tool-result history; an
  activity error becomes a tool message and the loop continues; the step cap is
  reported rather than hanging.
- A `worker.WorkflowReplayer` test over a recorded history — Phase 3's open
  checkbox, and the only thing that will catch a determinism regression before
  production does.
- A payload round-trip test (see below), which is cheap insurance on a silent
  failure mode.

## Verified, not a problem

Three things that look risky and are not. Recorded so nobody re-investigates
them.

- **`openai.ChatCompletionMessageParamUnion` survives Temporal's data
  converter.** This is the one that would fail silently and lose tool calls
  mid-conversation, so I checked it rather than reasoning about it: a system,
  user, assistant-with-tool-call and tool message round-tripped through
  `converter.GetDefaultDataConverter()` and came back with role discrimination,
  tool call IDs, function names and arguments all intact. The union's custom
  `MarshalJSON` / `UnmarshalJSON` do the right thing. **Worth promoting to a real
  test**, since it depends on openai-go's union encoding and would break quietly
  on an SDK bump.
- **The workflow body is deterministic**, as noted at the top.
- **`go vet ./...` and `go test ./...` are clean**, and `gofmt -l .` is silent.

## Suggested order

1. Workspace ownership (findings 1 and 2) — the only findings that produce wrong
   answers rather than slow or expensive ones, and 2 is a decision that has to be
   made before the code can be right.
2. Per-activity options: non-retryable permanent errors, `MaximumAttempts: 1` for
   mutating tools, `ScheduleToCloseTimeout`, heartbeat on the model call
   (findings 3, 4, 7).
3. The `TestWorkflowEnvironment` test and the payload round-trip test — cheap,
   free to run, and they make everything after this safe to change.
4. Compaction behind an activity, with the cut decision staying in workflow code
   (finding 5).
5. Telemetry interceptor, so the eval suite can see this path at all (finding 6).
6. Write the versioning posture down, whichever way it goes (finding 8).
