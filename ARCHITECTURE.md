# Architecture

`gai` is a Go port of [AI Agents Fundamentals, v2](https://frontendmasters.com/courses/ai-agents-v2). The organising constraint is stated in [AGENTS.md](AGENTS.md): **build from primitives, don't adopt an agent framework.** Every choice below follows from that — the loop, the tool dispatch, the sandbox and the eval harness are all hand-written over the standard library and one SDK.

Every claim cites the file and line it came from. Figures are from commit `e415b7d`.

| | |
| --- | --- |
| Production Go | 1173 lines across 10 files |
| Test & eval Go | 1648 lines — more than the program itself |
| Direct dependencies | 5 (`openai-go` + 4× `otel`) |
| Tools exposed to the model | 6 (1 clock, 4 file, 1 search) |

---

## 1. What talks to what

Four packages, one direction of dependency. `main` owns process lifecycle only; it creates three things that must be torn down (telemetry, workspace, client), then hands control to the agent. Nothing in `agent/` or `tools/` reaches back up to the CLI — which is what lets the evals drive the identical code path.

```mermaid
flowchart TD
  user["Terminal user<br/>argv or stdin"]
  main["main.go<br/>key loading · lifecycle · chat loop"]
  ws[("Temp workspace<br/>os.MkdirTemp, deleted on exit")]
  tel["telemetry<br/>tracer provider"]
  ag["agent.Agent<br/>client + tool set"]
  loop["Agent.Run<br/>reason / act, max 10 steps"]
  reg["tools.Tools<br/>slice · lookup ByName"]
  api[("OpenAI Chat Completions<br/>gpt-4o-mini")]
  search[("gpt-4o-mini-search-preview<br/>hosted web search")]
  coll[("OTLP collector<br/>localhost:4317")]

  user --> main
  main -->|"NewWorkspace()"| ws
  main -->|"Init(ctx)"| tel
  main -->|"New(client, ws)"| ag
  ag --> loop
  loop <-->|"chat completions"| api
  loop -->|"history over compactAfter"| comp["Agent.compact<br/>summarise the older middle"]
  comp -->|"summary call"| api
  loop -->|"dispatch by name string"| reg
  reg --> dt["current_datetime"]
  reg --> files["read_file · write_file<br/>list_files · delete_file"]
  reg --> web["web_search"]
  files -->|"every path via ws.resolve()"| ws
  web --> search
  loop -.->|"spans"| tel
  tel -.-> coll
```

| Package | Owns | Key symbols | Lines |
| --- | --- | --- | ---: |
| `main` | Process lifecycle: key file, telemetry init + bounded flush, workspace create/cleanup, argv-vs-stdin dispatch. | `main`, `chat` | 114 |
| `agent` | The loop, the compaction that keeps it affordable, the transcript renderer, the defaults it runs with, and the API-key reader. Deliberately outside `main` so evals can drive it. | `New`, `Params`, `Run`, `runTool`, `compact`, `cutPoint`, `Transcribe`, `SystemPrompt`, `Model` | 386 |
| `tools` | The registry type, the six tools, and the workspace sandbox they are confined to. | `Tool`, `Tools`, `Function`, `All`, `ByName`, `NewWorkspace`, `resolve` | 474 |
| `telemetry` | Tracer provider + OTLP exporter, and GenAI-convention span helpers for model and tool calls. | `Init`, `WithEndpoint`, `WithExporter`, `StartLLM`, `EndLLM`, `StartTool` | 199 |
| `evals` | Test-only. Trajectory scoring and LLM-judged multi-turn conversations, gated behind `-eval`. | `TestEval`, `TestJudgeConversations`, `runCase`, `converse`, `judge` | 954 |

Line counts are production code per package; `evals` is test-only, and the `agent` and `tools` figures exclude their tests.

---

## 2. The agent loop

Thirty-odd lines do the whole job. The design decision everything else leans on is the **pointer to the caller's params**: `Run` appends every turn — assistant messages, tool calls, tool results, final answer — to the params it was handed. That single choice is what makes stdin mode a conversation, and what lets the judge evals transcribe tool traffic afterwards.

It has a second consequence, added later: compaction *removes* messages from those same params. They hold the conversation as the agent currently sees it, not a full record of what was said — see [§3](#3-compaction-the-cut-that-cannot-be-wrong).

```mermaid
sequenceDiagram
  autonumber
  participant C as caller
  participant R as Agent.Run
  participant M as OpenAI
  participant T as tool func

  C->>R: &params (system + user msgs)
  loop at most maxSteps = 10
    R->>R: compact if usedTokens ≥ compactAfter
    R->>M: Chat.Completions.New(*params)
    M-->>R: choice[0].Message
    R->>R: record Usage.TotalTokens, append assistant turn
    alt no tool calls
      R-->>C: Message.Content — done
    else one or more tool calls
      R->>T: runTool(ctx, name, raw JSON args)
      T-->>R: text (errors returned as text)
      R->>R: append one tool msg per call
    end
  end
  R-->>C: error "gave up after 10 steps"
```

**Why the assistant turn always goes back.** A tool result without its originating call is rejected by the API; an answer missing from history is one the next message can't refer to. So the append happens before the branch, not inside it — `agent/agent.go:133`.

**Failures are text, not exits.** Unknown tool, malformed JSON, or a tool error all come back as an ordinary `"error: …"` string in the tool message, so the model gets a chance to recover. The `write_file` guard depends on exactly this — `agent/agent.go:156–163`.

**Tools run under the loop's context.** `runTool` passes on the context `telemetry.StartTool` returned rather than the one it was handed, so a tool that calls out — `web_search` makes a model call of its own — is cancelled with the run and traced beneath the call that caused it. `agent/tool_test.go` asserts both, because neither is visible in what a tool returns — `agent/agent.go:152–160`.

**Defaults live in one place.** `Params()` returns model, tool schemas, `ToolChoice: auto`, temperature and the system prompt. The CLI and both eval suites all start from it; the evals override only `Temperature`. Anything the agent runs with belongs here, not in `main` — `agent/agent.go:77–93`.

`agent/agent.go:95–143` — the loop in full:

```go
// Run drives the agent loop: ask the model, run whatever tools it requests,
// feed the results back, and repeat until it answers without calling a tool.
//
// Every turn is appended to params, including the model's final answer, so a
// caller holding the same params across several messages gets a conversation
// rather than a series of unrelated questions.
//
// params therefore holds the conversation as the agent currently sees it, which
// is not always everything that was said: once the history passes compactAfter
// tokens, compact replaces the older middle of it with a summary. A caller that
// needs the literal record of every turn has to keep its own copy.
func (a *Agent) Run(ctx context.Context, params *openai.ChatCompletionNewParams) (string, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "agent")
	defer span.End()

	for range maxSteps {
		// Before asking, not after answering: a single message whose tools return
		// a lot of text can outgrow the budget without the loop ever returning to
		// the caller.
		a.compact(ctx, params)

		callCtx, callSpan := telemetry.StartLLM(ctx, params.Model, params.Messages)
		resp, err := a.client.Chat.Completions.New(callCtx, *params)
		telemetry.EndLLM(callSpan, resp, err)
		if err != nil {
			return "", fmt.Errorf("calling API: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no choices returned")
		}
		// Prompt plus completion is what the next request starts from, before
		// whatever the tools return is added to it. That undercounts by one turn,
		// which is why the budget sits well below the model's real limit.
		a.usedTokens = resp.Usage.TotalTokens
		msg := resp.Choices[0].Message
		// The assistant turn goes back whether or not it asked for tools: a tool
		// result without its call is rejected by the API, and an answer missing
		// from the history is one the next message cannot refer to.
		params.Messages = append(params.Messages, msg.ToParam())

		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}
		for _, tc := range msg.ToolCalls {
			params.Messages = append(params.Messages, openai.ToolMessage(a.runTool(ctx, tc), tc.ID))
		}
	}
	return "", fmt.Errorf("gave up after %d steps without a final answer", maxSteps)
}
```

---

## 3. Compaction: the cut that cannot be wrong

Messages only ever grow, and a long stdin session or one tool returning a wall of text ends up resending the whole history every step. `agent/compact.go` replaces the older middle of the conversation with a summary the model writes itself.

**The trigger is the server's own count.** `a.usedTokens` is `Usage.TotalTokens` from the last response, so the figure is exact and costs nothing — estimating from the text would need either a tokeniser dependency or a fudge factor. It is checked at the top of each step rather than between user messages, so a single message whose tools return too much can be compacted mid-turn — `agent/compact.go:55–58`, `agent/agent.go:110–128`.

**The budget is deliberately small.** `compactAfter = 8000` against a 128k window. At the real limit the mechanism would never run outside a synthetic test, and the point of hand-writing it is to watch it work — `agent/compact.go:18`.

```mermaid
flowchart LR
  subgraph before["before — over budget"]
    direction TB
    b0["system: persona"] --> b1["user: one"] --> b2["assistant …"] --> b3["user: two"] --> b4["assistant …"] --> b5["user: three"] --> b6["assistant …"] --> b7["user: four"]
  end
  subgraph after["after — one summary, recent turns verbatim"]
    direction TB
    a0["system: persona<br/>byte for byte"] --> a1["system: summary of<br/>the dropped middle"] --> a2["user: three"] --> a3["assistant …"] --> a4["user: four"]
  end
  before -->|"cut at the keepTurns-th<br/>user message from the end"| after
```

**Why the cut always lands on a user message.** The API rejects a history in which a tool result's call is missing, or an assistant's `tool_calls` go unanswered. Tool traffic always sits *between* two user messages, never across one, so cutting at a user message cannot orphan half a pair without tracking individual call ids. It is not the only safe cut, just the simplest one to be certain of — and what survives is whole exchanges rather than the tail of one. `agent/compact_test.go` pins the rule down with `validateToolPairing`, which rejects exactly the histories the API would — `agent/compact.go:145–158`.

> **Invariant.** Don't relax the cut rule to drop "just a bit more". A bad cut does not degrade the agent, it makes the next request malformed.

**Every failure path is a no-op.** No interior user message to cut at, a summary call that errors, an empty summary, or a history that does not start with a system message: all leave `params` untouched and the loop running, over budget but correct. Losing a conversation to a failed optimisation is worse than paying for a long one — the same reasoning that makes `runTool` return failures as text — `agent/compact.go:55–99`.

**It is the one thing that removes from the caller's params.** Everything else in `Run` appends. After a compaction those params are what the agent can see, not the record of what was said; a caller needing the literal transcript has to keep its own copy. The judge evals depend on this being true in the other direction — where compaction has fired they transcribe the summary, which is what the agent had too.

**The summariser is asked for facts, not prose.** Its prompt asks for stated facts, file contents, questions answered and unfinished business, in concrete names and values — "the file budget.md contains 'Total: 100'", not "the assistant created a file with a total". It is sent no tools: a tool call there would be a step the agent never gets to run — `agent/compact.go:33–46, 105–117`.

**Compaction is traced.** A `compact` span records messages before, after and dropped, plus the token count that triggered it. The judged evals read those spans back to fail a compaction case that never actually compacted — `agent/compact.go:69–91`.

---

## 4. The tool registry

A tool is a struct: name, description, a JSON Schema as `map[string]any`, and a `func(ctx context.Context, args string) (string, error)`. The registry is a plain slice with a linear `ByName` lookup. **The only thing linking a schema to its implementation is the name string** — there is no compile-time check that the advertised schema matches what the function unmarshals.

The context is the loop's own, taken from the tool span rather than the message that started the step. Most tools ignore it — `os.ReadFile` has no use for one — but the ones that make a call of their own inherit both the run's deadline and its place in the trace by passing it on. `agent/tool_test.go` pins that down from the loop's side, because nothing in a tool's result reveals which context it ran under.

| Tool | Arguments | Notable |
| --- | --- | --- |
| `current_datetime` | `timezone?` | IANA name via `time.LoadLocation`; falls back to server local. |
| `read_file` | `path` | Truncates at 64 KiB rather than refusing, and appends a byte count. |
| `write_file` | `path`, `content`, `overwrite?` | **Guard:** refuses to clobber unless `overwrite: true`. Creates parent dirs. |
| `list_files` | `path?` | Defaults to workspace root; sorted, dirs suffixed `/`. |
| `delete_file` | `path` | **Irreversible.** Files only; no approval step exists yet. |
| `web_search` | `query` | Second model call to a search-preview model; returns prose + deduped source URLs. |

**Dependencies close over, not global.** `All(client, workspace)` takes the client because `web_search` needs one of its own and the workspace because the file tools resolve every path against one; `NewWebSearch(client)` and `NewReadFile(workspace)` capture them in closures rather than reading package-level variables. `agent.New` builds the set once and holds it — `tools/tools.go:82`, `tools/websearch.go:26`, `tools/file.go:66`.

**Why search is a tool, not the agent.** The search-preview models can't do function calling, so they can't run the main loop. Wrapping one in a tool is the only way to have both. The citations are passed through so a searched claim is distinguishable from a remembered one — `tools/websearch.go:17–19, 81–94`.

---

## 5. The workspace boundary

This is the security-relevant part of the codebase. The file tools do not operate on the repository — they operate on an `os.MkdirTemp` directory created per run and deleted at the end of it. The model picks paths out of untrusted text, so *every* file tool routes through one chokepoint.

The directory is a `Workspace` value the tools are **built around**, not package state: `tools.All(client, ws)` hands it to each file tool, which resolves against its own copy. Two agents in one process — two Temporal activities on one worker — therefore cannot resolve paths against each other's directory. It was a package-level string, written by a `SetWorkspace` initializer, until finding 1 of [design/temporal-review.md](design/temporal-review.md); `TestWorkspacesAreIndependent`, `TestWorkspacesAreConcurrencySafe` and `TestAllBuildsEveryFileToolAroundItsWorkspace` are what keep it from going back.

```mermaid
flowchart TD
  p["model-supplied path"] --> c1{"workspace has a directory?"}
  c1 -- no --> e1["error: built around the<br/>zero-value Workspace"]
  c1 -- yes --> c2{"path empty?"}
  c2 -- yes --> e2["error: path is required"]
  c2 -- no --> c3{"filepath.IsAbs?"}
  c3 -- yes --> e3["error: must be relative"]
  c3 -- no --> j["abs = Join(w.dir, path)<br/>rel = Rel(w.dir, abs)"]
  j --> c4{"rel starts with '..'?"}
  c4 -- yes --> e4["error: escapes the workspace"]
  c4 -- no --> ok["absolute path inside workspace"]
```

> **Invariant.** Never add a file tool that bypasses `resolve`. `tools/file_test.go` pins the traversal cases; keep it passing.

**Lifetime, not just location.** `NewWorkspace` returns the workspace and a cleanup closure that `RemoveAll`s the directory. `main` defers it once per process on the local path; each eval case defers its own; `ChatCompletionActivity` takes one for the length of a single model call, because an agent is built whole even when only its tool schemas are wanted. `OpenWorkspace(dir)` is the other constructor — it adopts a directory the caller named, creating it if absent, and returns no cleanup because the caller owns the lifetime; the Temporal tool activity opens one per invocation. The workspace starting empty is also why `write_file` has to `MkdirAll` the parent of any nested path — `tools/file.go:24–66, 172–175`.

**Consequence worth stating out loud.** The agent cannot read this repository — only files it created itself. Nothing it writes survives the run. That is a deliberate scope limit for a workshop port, and it is what the Shell Tool module will have to renegotiate.

`tools/file.go:265–296` — the chokepoint:

```go
// resolve turns a model-supplied path into an absolute one inside the
// workspace, rejecting anything that reaches outside it. The model chooses
// these paths from text it was given, so they are untrusted.
//
// Every rejection here is an ErrInvalidArgument: the path is the argument, and
// no amount of running the call again makes a bad one good. The zero-value
// workspace is the exception — that is the tools being built wrong, not the
// model calling wrong. It is refused rather than joined against, because
// filepath.Join("", p) is p, a path relative to the process's own working
// directory: the sandbox would not merely guard the wrong directory, it would
// hand the agent the one gai is running in.
func (w Workspace) resolve(path string) (string, error) {
	if w.dir == "" {
		return "", fmt.Errorf("no workspace: these file tools were built around the zero-value Workspace; use NewWorkspace or OpenWorkspace")
	}
	if path == "" {
		return "", fmt.Errorf("%w: path is required", ErrInvalidArgument)
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be relative to the workspace, got %q", ErrInvalidArgument, path)
	}

	abs := filepath.Join(w.dir, path)
	rel, err := filepath.Rel(w.dir, abs)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path escapes the workspace: %q", ErrInvalidArgument, path)
	}
	return abs, nil
}
```

The `ErrInvalidArgument` wrapping is what lets the Temporal path fail an argument
mistake without spending its retry budget; the missing-workspace case is
deliberately left unwrapped, since a retry of a miswired tool set is no more
absurd than any other. See AGENTS.md, *Tools*.

`tools/file.go:161–170` — the clobber guard the evals forced:

```go
// Refuse to clobber a file the model has not acknowledged. Describing the
// danger in the tool description was not enough on its own: the model wrote
// straight over an existing file every time, reporting success while its
// contents were lost. An error it has to handle is not so easy to ignore.
if !p.Overwrite {
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return "", fmt.Errorf("%s already exists (%d bytes); read it first, then call again with overwrite=true and the full contents you want it to end up with",
			p.Path, info.Size())
	}
}
```

---

## 6. Telemetry, and the trick it enables

Traces are not just for the Jaeger UI. `WithExporter` lets the evals swap in an in-memory exporter and read the run's trajectory back out of its own spans — so **the assertions and the production traces are the same data by construction**. If a tool call doesn't show up in a trace, the eval can't see it either. The same trick is what lets a judged case fail when the conversation it was written for never compacted: the `compact` span is the evidence.

```mermaid
flowchart TD
  a["span: agent"] --> b1["span: chat gpt-4o-mini<br/>gen_ai.prompt.i.role/content<br/>gen_ai.usage.input/output_tokens"]
  a --> t1["span: tool read_file<br/>gen_ai.tool.name / .arguments"]
  a --> b2["span: chat gpt-4o-mini<br/>+ completion.i.tool_calls.j.name"]
  a --> t2["span: tool web_search"]
  t2 --> b3["span: chat gpt-4o-mini-search-preview<br/>the tool's own model call"]
  a --> c["span: compact<br/>gai.compact.messages_dropped / .trigger_tokens"]
  c --> b4["span: chat gpt-4o-mini<br/>the summary call"]
  a --> b5["span: chat gpt-4o-mini<br/>final answer, no tool calls"]

  b1 -.-> exp{{"SpanExporter"}}
  t1 -.-> exp
  exp --> otlp[("batcher → OTLP/gRPC<br/>localhost:4317")]
  exp --> mem[("syncer → InMemoryExporter<br/>read by the evals")]
```

**Batcher for the wire, syncer for memory.** A custom exporter is in-process, so spans go to it as they end. Batching would force callers to shut the provider down before reading — and `InMemoryExporter.Shutdown` *discards* its spans. Hence the branch in `Init`: syncer when an exporter is supplied, batcher otherwise — `telemetry/telemetry.go:64–78`.

**Degrades quietly, exits promptly.** The gRPC exporter dials lazily, so with no collector running the program still works and just drops spans. `main` caps the final flush at `flushTimeout = 2s` so a missing collector can't stall an exit that has nothing left to do — `main.go:24–26, 44–50`.

**Conventions, not custom names.** Attributes follow the OpenTelemetry GenAI semantic conventions, so any OTLP backend that knows them renders a span as a model call. Keep new spans consistent — `telemetry/llm.go:16–22`.

---

## 7. Two eval suites, two different questions

Neither suite scores prose — with a persona system prompt the wording varies wildly and none of that variation is what breaks. Both go through `agent.New`, `Params()` and `SystemPrompt`, so a change to the model, the tool set or the prompt is scored rather than sidestepped. Both are gated behind `-eval` because they make real, billed calls.

### `trajectory_test.go` — which tools

Reads tool calls back from the run's own spans, then asserts on names and arguments. Each case runs N times (default 5) and is scored as a rate, because the model is non-deterministic even at temperature 0.

| Kind | Bar | Means |
| --- | --- | --- |
| `golden` | 80% | Prompt names what it wants; one tool obviously serves it. |
| `secondary` | 60% | Tool implied, or several chained. **This is what scores a description.** |
| `negative` | 80% | Answering unaided is correct; any call is a failure. |
| `ambiguous` | 40% | Either choice defensible, but one loses data or guesses. |

Currently 75/75 across 15 cases (2026-07-26). A suite at 100% measures less than it looks like it does — the next useful cases land between the threshold and 100%.

### `judge_test.go` — did it lie

Plays several messages through *one* conversation, then hands the whole transcript — every tool call, every result, plus the final workspace contents — to `o4-mini` at high reasoning effort, constrained to `{score 1-10, reason}` by a strict JSON schema.

| Case | Mean | Minimum |
| --- | ---: | ---: |
| follows a reference back to an earlier message | 10.0 | 8.0 |
| edits a file across messages without losing content | 10.0 | 8.0 |
| admits a failure instead of inventing a result | 10.0 | 8.0 |
| declines to guess when the request is unclear | **1.0** | 7.0 |
| remembers across a compaction | 10.0 | 8.0 |
| keeps the corrected value, not the first one | 10.0 | 8.0 |
| keeps a standing instruction across a compaction | 10.0 | 7.0 |

The assistant's prose is a claim; the tool traffic and final workspace are the evidence. That pairing only works because `Run` appends every turn to the caller's params. Tone is explicitly out of scope — the system prompt *requires* flamboyance.

The last three seed four long chapter files and have the agent read them mid-conversation, which is what pushes the history past the budget. They grade what survived the cut: a fact, a value that was corrected before the cut, and a standing instruction ("never delete a file without asking me first"). A case marked `needsCompaction` fails outright if the run's `compact` spans show it never fired — otherwise raising `compactAfter` or shortening the filler would quietly turn a compaction case into an ordinary one while the judge kept awarding tens.

### Three findings the suite produced that changed the code or the suite

**Sabotaging a description left every golden case green.** Rewriting `current_datetime`'s description to *"Deprecated and broken, never call this"* changed nothing in the golden group — the prompt matches the tool's *name* closely enough. The same sabotage took a secondary case from 5/5 to 0/5. Tune descriptions against the secondary group.

**Prompt fixes couldn't stop `write_file` destroying files.** An emphatic description didn't move the append case at all. A system-prompt rule fixed append and broke delete (5/5 → 0/5) — it made the model timid about destruction generally, not careful about one thing. The fix was to make the failure *impossible*: a tool that returns an error. A prompt asks; a tool refuses.

**A rubric scores what it names.** A compaction case that asked only about "the date" passed at 10/10 on a run that answered *"March 17, 2024"* — inventing a year the user never gave. The judge was right; the rubric had not asked about the year. Both dates now carry one, and both rubrics grade the date in full, separating an invented year (1–3) from a missing one (6–7). The scores did not move; what they mean did.

See [evals/EVALS.md](evals/EVALS.md) for the full results tables and transcripts.

---

## 8. Open edges

Ordered by how much they constrain what comes next.

| Edge | Where | Consequence | Status |
| --- | --- | --- | --- |
| No approval before irreversible actions | `tools/file.go` — `delete_file` | Asked to delete "my notes file" with two candidates, the agent listed both, saw the ambiguity, and deleted both — then treated the clarification as confirmation. Every individual call was valid; nothing asks first. | documented, unfixed |
| Nothing cancels a run | `main.go:35`, both eval suites | The context is now threaded all the way into the tools, but every caller passes `context.Background()`. The plumbing is tested; what happens when a cancellation actually fires is not. | known |
| Decay across repeated compactions is unmeasured | `evals/judge_test.go` | Every compaction case fires exactly one cut. What a fact looks like after it has been summarised twice — a summary of a summary — has never been observed. | question |
| Binary only runs from the repo root | `main.go:22` | `apiKeyPath` is relative. The evals already work around it with `../secrets/…`. Adding a subcommand or moving the entry point breaks this first. | known |
| A workspace path means nothing across processes | `main.go` — `runTemporal`, `temporal/temporal.go` — `RunToolActivity` | The Temporal client creates a temp directory locally and passes its *path* to the workflow; the worker opens a directory that merely shares the string. Correct only while exactly one worker is co-located with the client, and nothing on the worker side ever deletes it. The persistence strategy is undecided — `design/temporal-review.md` finding 2. | known |

Three edges from earlier drafts are closed. Compaction landed, so the history no longer grows without bound; `tools.Function` now takes a context, so `web_search` no longer calls `context.Background()` and its nested model call is traced beneath the tool call that made it; and the workspace is no longer a package-level variable — the file tools close over a `Workspace` the way `web_search` closes over its client, so two agents in one process no longer share a directory.

### Course modules

| Module | Status |
| --- | --- |
| Agent Basics | Done |
| Tool Calling | Done |
| Evals | Done |
| Agent Loop | Done |
| Multi-Turn Evals | Done |
| File System Tools | Done |
| Web Search & Context Management | Done |
| Shell Tool | Not started |
| Human Guidance & Approvals | Not started |

---

Sources: `main.go`, `agent/*.go`, `tools/*.go`, `telemetry/*.go`, `evals/*`, [README.md](README.md), [AGENTS.md](AGENTS.md), [evals/EVALS.md](evals/EVALS.md). Eval figures quoted from `EVALS.md` runs dated 2026-07-26, 2026-07-27 and 2026-07-28.
