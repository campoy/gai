# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

`gai` ("Go Agent") is a single-binary Go CLI that sends a prompt to the OpenAI Chat Completions API and prints the response. The entire program is `main.go`; there are no packages, no tests, and no build tooling beyond the Go toolchain.

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

Commit every large change without being asked. Commit directly to `main` — local-only repo, no remote, no feature branches. **Do not add `Co-Authored-By` or any other attribution trailer** to commit messages.

## SDK notes

Uses `github.com/openai/openai-go` v1.12.0. Two gotchas specific to this SDK version:

- `openai.NewClient(...)` returns a `Client` **value**, not a pointer.
- Messages are built with helpers like `openai.UserMessage(s)` that produce `openai.ChatCompletionMessageParamUnion`, not plain structs.

The model is pinned in the `model` const (`openai.ChatModelGPT4oMini`). `resp.Choices` can legitimately come back empty — the existing code checks for this before indexing.
