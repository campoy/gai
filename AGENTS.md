# AGENTS.md

This file provides guidance to coding agents (Claude Code and others) when working with code in this repository.

Claude Code does not read `AGENTS.md` directly, so the repo's `CLAUDE.md` is a one-line `@AGENTS.md` import. Edit this file, not that one.

## Overview

`gai` ("Go Agent") is a CLI coding agent built from scratch in Go. It is a port of [AI Agents Fundamentals, v2](https://frontendmasters.com/courses/ai-agents-v2) by Scott Moss — a workshop taught in Node.js/TypeScript with the OpenAI SDK — reimplemented in Go with `openai-go`.

That framing drives most decisions here: **build from primitives, don't adopt an agent framework.** The point is to write the agent loop, the tool dispatch, and the context management by hand, the way the course does. Prefer the standard library and the OpenAI SDK over pulling in a dependency that hides the mechanism.

The course order is Agent Basics → Tool Calling → Evals → Agent Loop → Multi-Turn Evals → File System Tools → Web Search & Context Management → Shell Tool → Human Guidance & Approvals. The README tracks which modules are done; check it before assuming a capability exists.

`main.go` holds the CLI and the OpenAI wiring; `tools/` holds the tool registry. No tests yet, no build tooling beyond the Go toolchain.

**Check for existing code before writing new code.** Look through the packages and files already in the repo for a type, function, or registry that covers what you need, and extend or reuse it. Do not add a second implementation alongside one that already exists — this happened once with the datetime tool, which was written into `main.go` while `tools/datetime.go` already defined it.

## Tools

A tool is a `tools.Tool`: a name, a description, a JSON Schema for its arguments (`Parameters`, nil for none), and a `Func` taking the call's raw JSON arguments. Register new ones in `tools.All` (a `tools.Tools`); `tools.ByName` does the lookup.

`Tool.AsToolParam` and `Tools.AsToolParams` convert the registry into the schema the SDK sends, so `main.go` passes `tools.All.AsToolParams()` straight to `Tools` on the request params.

The only thing linking a schema to its implementation is the name string. Tool failures are returned to the model as text (`runTool`) rather than exiting, so it can recover.

## Commands

```bash
go build -o gai .            # build
./gai "your prompt here"     # run; args are joined into the prompt, default prompt if none
go run . "your prompt here"  # build and run in one step

gofmt -l .                   # list unformatted files (should print nothing)
go vet ./...                 # vet
```

There is no test suite yet. Once `*_test.go` files exist: `go test ./...` for all, `go test -run TestName ./...` for one.

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
