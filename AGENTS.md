# AGENTS.md

This file provides guidance to coding agents (Claude Code and others) when working with code in this repository.

Claude Code does not read `AGENTS.md` directly, so the repo's `CLAUDE.md` is a one-line `@AGENTS.md` import. Edit this file, not that one.

## Overview

`gai` ("Go Agent") is a CLI coding agent built from scratch in Go. It is a port of [AI Agents Fundamentals, v2](https://frontendmasters.com/courses/ai-agents-v2) by Scott Moss — a workshop taught in Node.js/TypeScript with the OpenAI SDK — reimplemented in Go with `openai-go`.

That framing drives most decisions here: **build from primitives, don't adopt an agent framework.** The point is to write the agent loop, the tool dispatch, and the context management by hand, the way the course does. Prefer the standard library and the OpenAI SDK over pulling in a dependency that hides the mechanism.

The course order is Agent Basics → Tool Calling → Evals → Agent Loop → Multi-Turn Evals → File System Tools → Web Search & Context Management → Shell Tool → Human Guidance & Approvals. The README tracks which modules are done; check it before assuming a capability exists.

`main.go` is the CLI: key loading, the stdin loop, process setup. `agent/` holds the loop itself, the compaction that keeps it affordable, the transcript renderer and the defaults it runs with; `tools/` holds the tool registry; `telemetry/` holds the OpenTelemetry exporter and span helpers; `evals/` holds the eval suite and its notes. No build tooling beyond the Go toolchain.

Four documents, each with a job. This file is the working guide — conventions, invariants, and the reasoning behind them. [README.md](README.md) is for someone running `gai` for the first time. [ARCHITECTURE.md](ARCHITECTURE.md) explains how the pieces fit, with diagrams and line-cited claims; it carries figures (line counts, eval scores, a commit hash) that go stale unless a change that moves them updates them. [evals/EVALS.md](evals/EVALS.md) records eval results and what the suite has caught. A change that alters behaviour belongs in whichever of these describe it, in the same commit.

**Check for existing code before writing new code.** Look through the packages and files already in the repo for a type, function, or registry that covers what you need, and extend or reuse it. Do not add a second implementation alongside one that already exists — this happened once with the datetime tool, which was written into `main.go` while `tools/datetime.go` already defined it.

## Tools

A tool is a `tools.Tool`: a name, a description, a JSON Schema for its arguments (`Parameters`, nil for none), and a `Func` taking a context and the call's raw JSON arguments. Register new ones in `tools.All`, which builds the set; `Tools.ByName` does the lookup. Names are snake_case (`read_file`, `current_datetime`).

The context is the agent loop's, and `runTool` hands over the one `telemetry.StartTool` returned rather than the one it was given. A tool that makes a request of its own must pass it on — `web_search` does, so its nested model call is cancelled with the run and appears in the trace under the tool call that made it. Never substitute `context.Background()`: it was there before the signature had a context, and it made the search uncancellable and its span a second root. The file and datetime tools take the parameter as `_`, which is fine; a tool that does I/O and drops it is not. `agent/tool_test.go` pins both halves down.

`tools.All` takes an `*openai.Client` because `web_search` makes a model call of its own. A tool needing outside state closes over it at construction — `NewWebSearch(client)` — rather than reading a package-level variable set by an initializer. `agent.New` builds the set once and holds it.

The file tools work in a temporary workspace, not the repository. `tools.NewWorkspace` creates it and returns a cleanup function; `main` calls it once per run, and each eval case calls it per run, deferring the cleanup, so the directory and everything in it is deleted on exit. The file tools fail until it has been called.

Every file tool routes its path through `resolve` in `tools/file.go`, which rejects absolute paths and anything escaping the workspace. The model picks these paths out of untrusted text — never add a file tool that bypasses `resolve`, and keep the tests in `tools/file_test.go` passing.

`Tool.AsToolParam` and `Tools.AsToolParams` convert the registry into the schema the SDK sends, so `Agent.Params` passes `Tools.AsToolParams()` straight to `Tools` on the request params.

The only thing linking a schema to its implementation is the name string. Tool failures are returned to the model as text (`runTool`) rather than exiting, so it can recover.

## The agent loop

`agent.New(client)` returns an `*agent.Agent` holding the client and its tools; `Params` and `Run` are methods on it. `Run` takes `*openai.ChatCompletionNewParams` and appends every turn to it — the assistant message, any tool results, and the final answer. That is what makes the stdin mode a conversation rather than a series of unrelated questions, and what lets the evals transcribe the tool traffic, so don't switch it back to a value receiver.

The loop lives in `agent/` rather than `main` so the evals can drive the code that ships. `agent.Params()` returns the model, tool set, tool choice and system prompt in one place; both the CLI and the evals start from it, the evals overriding only `Temperature`. Anything the agent runs with belongs there, not in `main`.

`agent/compact.go` keeps the conversation affordable. At the top of every step `compact` checks `usedTokens` — the total the last response reported, which is why the count is exact and costs nothing — and once it passes `compactAfter`, replaces the older middle of the history with a summary the model writes itself. It runs inside the loop rather than between user messages, so one message whose tools return a lot of text can be compacted mid-turn.

`cutPoint` decides where. **The cut always lands on a user message**, because the API rejects a history whose tool result has no call, or whose `tool_calls` go unanswered, and tool traffic never spans two user messages. That rule is the load-bearing part of this file — `agent/compact_test.go` pins it down, along with `validateToolPairing`, which rejects a history the API would. Don't relax it to drop "just a bit more"; a bad cut doesn't degrade the agent, it breaks the request outright.

Compaction is best-effort everywhere: no safe cut, a failed summary call, an empty one, or a history that doesn't start with a system message all leave `params` untouched and the loop continuing. It also *removes* messages from the caller's params — the one place anything does — so those params hold the conversation as the agent currently sees it, not a full record. `agent/run_test.go` drives the whole loop against an `httptest` stub, so the wiring is covered without an API call.

`agent.Transcribe` renders a conversation as text. It lives in `agent/` because both the evals and the summariser need it and two renderings would drift; it skips the first system message (the persona) and labels later ones as summaries.

With command line arguments `main` answers one prompt and exits; with none, `chat` reads a message per line until stdin closes. The `>` prompt is only printed when stdin is a character device, so piped input produces clean output. The workspace is created once per process, so files written in one message are still there in the next.

## Evals

`evals/trajectory_test.go` scores the agent's *trajectory* — which tools it called with which arguments — not the prose it produced, which varies too much to assert on. Cases come in three kinds with different pass-rate thresholds: `golden` (the prompt names what it wants, 80%), `secondary` (the tool is implied or several are chained, 60%), and `negative` (answering unaided is correct, 80%). Each case runs N times and is scored as a rate, because the model is non-deterministic even at temperature 0.

There is a fourth kind, `ambiguous` (40%), for prompts where another agent could justify the other choice but one option loses data or guesses. When a case fails, fix the agent or change the expectation deliberately — never lower a threshold to make it green.

`evals/judge_test.go` is the multi-turn half: several messages through one conversation, then the whole conversation handed to a model that must reply with a strict JSON schema of `{score 1-10, reason}`. `agent.Transcribe` renders every tool call and result alongside the prose, plus the final workspace contents, so the judge can check what the agent claimed against what it actually did. That works only because `agent.Run` appends every turn to the caller's params — keep it that way. Where compaction has fired the judge sees the summary that replaced those turns, which is what the agent saw too; `remembers across a compaction` is the case that exercises it. It grades decisions and truthfulness only — tone and persona are explicitly out of scope, since the system prompt requires the agent to be flamboyant. The judge is `o4-mini` at high reasoning effort; reasoning models reject `temperature`, so the bar is a mean across runs. `declines_to_guess_when_the_request_is_unclear` fails at 1/10 by design: the agent deletes both candidate files on an ambiguous request.

`evals/EVALS.md` records the current scores and what the suite has caught, including why `write_file` refuses to clobber a file rather than merely warning against it. Update its results table when scores move.

Tool calls are read back from the run's own OpenTelemetry spans via `telemetry.WithExporter` and an in-memory exporter, so the evals and the traces agree by construction. `InMemoryExporter.Shutdown` discards its spans, so read them before shutting the provider down — that is why a custom exporter is wired as a syncer rather than a batcher.

Evals go through `agent.New`, `Agent.Params` and `agent.SystemPrompt`. Keep it that way: an eval against a prompt or a tool set that doesn't ship measures nothing.

## Telemetry

`telemetry.Init` installs a global tracer provider exporting OTLP/gRPC to `localhost:4317`; `telemetry.WithEndpoint` retargets it. The connection is always plaintext, so a hosted backend would need TLS and an auth header added first. It returns a shutdown function that must run before exit, since spans are batched — `main` bounds it with `flushTimeout` so a missing collector can't stall the program.

Span attributes follow the OpenTelemetry GenAI conventions (`gen_ai.prompt.{i}.role`, `gen_ai.usage.input_tokens`, …). Keep new spans consistent with those names.

To see traces locally, run Jaeger: `docker run --rm -p 16686:16686 -p 4317:4317 cr.jaegertracing.io/jaegertracing/jaeger:2.20.0`, UI at localhost:16686.

## Commands

```bash
go build -o gai .            # build
./gai "your prompt here"     # run; args are joined into the prompt, stdin if none
go run . "your prompt here"  # build and run in one step

gofmt -l .                   # list unformatted files (should print nothing)
go vet ./...                 # vet
```

`go test ./...` runs the tests; `go test -run TestName ./...` runs one. What is covered without spending an API call: the file-tool path sandbox (`tools/file_test.go`), the compaction cut rule and its tool pairing (`agent/compact_test.go`), the loop end to end against an `httptest` stub (`agent/run_test.go`), and the context a tool is handed (`agent/tool_test.go`). Everything about how the model behaves is in `evals/`, and is billed.

The evals in `evals/` make real, billed API calls and are skipped unless `-eval` is passed: `go test ./evals/ -eval`, plus `-eval.runs=N` to change how many times each case runs (default 5). Never remove that gate; `go test ./...` must stay free. They read the key from `../secrets/openai-api-key`, since a test binary runs in its own package directory.

Verification loop after a change: `gofmt -l . && go vet ./... && go build -o gai . && ./gai "Reply with exactly: pong"`. The last step makes a real, billed API call — it is the only way to confirm the client wiring works, but skip it for changes that can't affect the request path.

## API key handling

`agent.LoadAPIKey` reads the file named by `apiKeyPath` in `main.go` — `secrets/openai-api-key`, containing **nothing but the raw key**, no `KEY=value` wrapper, trailing whitespace trimmed. This replaced an earlier `.env`-style format; don't reintroduce env-file parsing. It takes the path as an argument because the evals run from their own package directory and pass `../secrets/openai-api-key`.

`apiKeyPath` is a relative path, so **the binary only works when run from the repo root**. If you add subcommands or move the entry point, this is the first thing that breaks.

The whole `secrets/` directory is gitignored. A live key was once committed and later purged from history via a root-commit rewrite, so prefer explicit paths when staging (`git add main.go`) over `git add -A`.

## Git conventions

- **Commit every large change.** Don't wait to be asked and don't ask for permission each time. A substantial unit of work — a feature, a refactor, a bug fix spanning files — gets its own commit with a descriptive message. Small tweaks and in-progress edits don't.
- **Write a body, not just a subject.** Any commit worth making on its own gets a short paragraph explaining what changed and why — the reasoning that isn't visible in the diff. Trivial one-liners can stay subject-only.
- **No self-attribution anywhere.** Not in commit messages — no `Co-Authored-By: Claude` or any other trailer — and not in pull request titles, bodies or comments: no "Generated with", no tool name, no badge, no emoji sign-off. This holds however the text was produced. The history and the PR queue read as the author's own work.
- **Work on a feature branch, not `main`.** The repo has a remote — `origin`, `github.com/campoy/gai` — and changes land through a pull request. Branch before the first commit; if you notice you're already on `main` with work in progress, branch and carry it over rather than committing there.
- **Open the PR when the work is done, not before.** Done means `gofmt -l .` silent, `go vet ./...` and `go test ./...` clean, and the docs in this file, the README and `evals/EVALS.md` updated in the same change. `gh pr create` with a body that explains the reasoning, not just the diff — and a section for what the reviewer should weigh: the trade-offs taken, what wasn't measured, and anything that contradicts a convention written down here.
- **Push and open PRs only when asked.** Committing on a branch is local and cheap to undo; publishing to GitHub is neither. Ask first, every time — approval to open one PR is not approval for the next.
- **Stage explicit paths** (`git add main.go`), not `git add -A` or `git add .`, so nothing under `secrets/` can slip in.

## SDK notes

Uses `github.com/openai/openai-go` v1.12.0. Two gotchas specific to this SDK version:

- `openai.NewClient(...)` returns a `Client` **value**, not a pointer.
- Messages are built with helpers like `openai.UserMessage(s)` that produce `openai.ChatCompletionMessageParamUnion`, not plain structs.

The model is pinned in `agent.Model` (`openai.ChatModelGPT4oMini`), exported so the evals score the model that ships. `resp.Choices` can legitimately come back empty — the existing code checks for this before indexing.
