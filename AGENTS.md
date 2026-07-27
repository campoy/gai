# AGENTS.md

This file provides guidance to coding agents (Claude Code and others) when working with code in this repository.

Claude Code does not read `AGENTS.md` directly, so the repo's `CLAUDE.md` is a one-line `@AGENTS.md` import. Edit this file, not that one.

## Overview

`gai` ("Go Agent") is a CLI coding agent built from scratch in Go. It is a port of [AI Agents Fundamentals, v2](https://frontendmasters.com/courses/ai-agents-v2) by Scott Moss — a workshop taught in Node.js/TypeScript with the OpenAI SDK — reimplemented in Go with `openai-go`.

That framing drives most decisions here: **build from primitives, don't adopt an agent framework.** The point is to write the agent loop, the tool dispatch, and the context management by hand, the way the course does. Prefer the standard library and the OpenAI SDK over pulling in a dependency that hides the mechanism.

The course order is Agent Basics → Tool Calling → Evals → Agent Loop → Multi-Turn Evals → File System Tools → Web Search & Context Management → Shell Tool → Human Guidance & Approvals. The README tracks which modules are done; check it before assuming a capability exists.

`main.go` holds the CLI and the OpenAI wiring; `tools/` holds the tool registry; `telemetry/` holds the OpenTelemetry exporter and span helpers. No tests yet, no build tooling beyond the Go toolchain.

**Check for existing code before writing new code.** Look through the packages and files already in the repo for a type, function, or registry that covers what you need, and extend or reuse it. Do not add a second implementation alongside one that already exists — this happened once with the datetime tool, which was written into `main.go` while `tools/datetime.go` already defined it.

## Tools

A tool is a `tools.Tool`: a name, a description, a JSON Schema for its arguments (`Parameters`, nil for none), and a `Func` taking the call's raw JSON arguments. Register new ones in `tools.All` (a `tools.Tools`); `tools.ByName` does the lookup. Names are snake_case (`read_file`, `current_datetime`).

The file tools work in a temporary workspace, not the repository. `tools.NewWorkspace` creates it and returns a cleanup function; `main` calls it once per run and defers the cleanup, so the directory and everything in it is deleted on exit. The file tools fail until it has been called.

Every file tool routes its path through `resolve` in `tools/file.go`, which rejects absolute paths and anything escaping the workspace. The model picks these paths out of untrusted text — never add a file tool that bypasses `resolve`, and keep the tests in `tools/file_test.go` passing.

`Tool.AsToolParam` and `Tools.AsToolParams` convert the registry into the schema the SDK sends, so `main.go` passes `tools.All.AsToolParams()` straight to `Tools` on the request params.

The only thing linking a schema to its implementation is the name string. Tool failures are returned to the model as text (`runTool`) rather than exiting, so it can recover.

## The agent loop

`run` takes `*openai.ChatCompletionNewParams` and appends every turn to it — the assistant message, any tool results, and the final answer. That is what makes the stdin mode a conversation rather than a series of unrelated questions, so don't switch it back to a value receiver.

With command line arguments `main` answers one prompt and exits; with none, `chat` reads a message per line until stdin closes. The `>` prompt is only printed when stdin is a character device, so piped input produces clean output. The workspace is created once per process, so files written in one message are still there in the next.

## Evals

`eval_test.go` scores the agent's *trajectory* — which tools it called with which arguments — not the prose it produced, which varies too much to assert on. Cases come in three kinds with different pass-rate thresholds: `golden` (the prompt names what it wants, 80%), `secondary` (the tool is implied or several are chained, 60%), and `negative` (answering unaided is correct, 80%). Each case runs N times and is scored as a rate, because the model is non-deterministic even at temperature 0.

There is a fourth kind, `ambiguous` (40%), for prompts where another agent could justify the other choice but one option loses data or guesses. When a case fails, fix the agent or change the expectation deliberately — never lower a threshold to make it green.

`judge_test.go` is the multi-turn half: several messages through one conversation, then the transcript and the final workspace contents handed to a model that must reply with a strict JSON schema of `{score 1-10, reason}`. It grades decisions and truthfulness only — tone and persona are explicitly out of scope, since the system prompt requires the agent to be flamboyant. The judge is `o4-mini` at high reasoning effort; reasoning models reject `temperature`, so the bar is a mean across runs. `declines_to_guess_when_the_request_is_unclear` fails at 1/10 by design: the agent deletes both candidate files on an ambiguous request.

`EVALS.md` records the current scores and what the suite has caught, including why `write_file` refuses to clobber a file rather than merely warning against it. Update its results table when scores move.

Tool calls are read back from the run's own OpenTelemetry spans via `telemetry.WithExporter` and an in-memory exporter, so the evals and the traces agree by construction. `InMemoryExporter.Shutdown` discards its spans, so read them before shutting the provider down — that is why a custom exporter is wired as a syncer rather than a batcher.

Evals share `systemPrompt` with `main`. Keep it that way: an eval against a prompt that doesn't ship measures nothing.

## Telemetry

`telemetry.Init` installs a global tracer provider exporting OTLP/gRPC to `localhost:4317`; `telemetry.WithEndpoint` retargets it. The connection is always plaintext, so a hosted backend would need TLS and an auth header added first. It returns a shutdown function that must run before exit, since spans are batched — `main` bounds it with `flushTimeout` so a missing collector can't stall the program.

Span attributes follow the OpenTelemetry GenAI conventions (`gen_ai.prompt.{i}.role`, `gen_ai.usage.input_tokens`, …). Keep new spans consistent with those names.

To see traces locally, run Jaeger: `docker run --rm -p 16686:16686 -p 4317:4317 cr.jaegertracing.io/jaegertracing/jaeger:2.20.0`, UI at localhost:16686.

## Commands

```bash
go build -o gai .            # build
./gai "your prompt here"     # run; args are joined into the prompt, default prompt if none
go run . "your prompt here"  # build and run in one step

gofmt -l .                   # list unformatted files (should print nothing)
go vet ./...                 # vet
```

`go test ./...` runs the tests; `go test -run TestName ./...` runs one. Coverage is thin — the file-tool path sandbox and the evals.

The evals in `eval_test.go` make real, billed API calls and are skipped unless `-eval` is passed: `go test -run TestEval -eval .`, or `-eval.runs=N` to change how many times each case runs (default 5). Never remove that gate; `go test ./...` must stay free.

Verification loop after a change: `gofmt -l . && go vet ./... && go build -o gai . && ./gai "Reply with exactly: pong"`. The last step makes a real, billed API call — it is the only way to confirm the client wiring works, but skip it for changes that can't affect the request path.

## API key handling

`loadAPIKey` reads `secrets/openai-api-key` — a file containing **nothing but the raw key**, no `KEY=value` wrapper, trailing whitespace trimmed. This replaced an earlier `.env`-style format; don't reintroduce env-file parsing.

`apiKeyPath` is a relative path, so **the binary only works when run from the repo root**. If you add subcommands or move the entry point, this is the first thing that breaks.

The whole `secrets/` directory is gitignored. A live key was once committed and later purged from history via a root-commit rewrite, so prefer explicit paths when staging (`git add main.go`) over `git add -A`.

## Git conventions

- **Commit every large change.** Don't wait to be asked and don't ask for permission each time. A substantial unit of work — a feature, a refactor, a bug fix spanning files — gets its own commit with a descriptive message. Small tweaks and in-progress edits don't.
- **Write a body, not just a subject.** Any commit worth making on its own gets a short paragraph explaining what changed and why — the reasoning that isn't visible in the diff. Trivial one-liners can stay subject-only.
- **No attribution trailers.** Do not add `Co-Authored-By: Claude` or any other self-attribution. The history reads as the author's own.
- **Commit directly to `main`.** Local-only repo, no remote, no feature branches.
- **Stage explicit paths** (`git add main.go`), not `git add -A` or `git add .`, so nothing under `secrets/` can slip in.

## SDK notes

Uses `github.com/openai/openai-go` v1.12.0. Two gotchas specific to this SDK version:

- `openai.NewClient(...)` returns a `Client` **value**, not a pointer.
- Messages are built with helpers like `openai.UserMessage(s)` that produce `openai.ChatCompletionMessageParamUnion`, not plain structs.

The model is pinned in the `model` const (`openai.ChatModelGPT4oMini`). `resp.Choices` can legitimately come back empty — the existing code checks for this before indexing.
