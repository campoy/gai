# Go Agent

`gai` is a CLI coding agent written from scratch in Go.

It follows [AI Agents Fundamentals, v2](https://frontendmasters.com/courses/ai-agents-v2) by Scott Moss on Frontend Masters — a workshop that builds a CLI agent from first principles. The course is taught in Node.js and TypeScript with the OpenAI SDK; this repository works through the same material in Go, using [`openai-go`](https://github.com/openai/openai-go).

The goal is to build the agent from primitives rather than adopt a framework: a plain loop over the Chat Completions API, tools as ordinary Go functions, and explicit control over context and approvals.

## Status

Early. The CLI takes one prompt and loops — asking the model, running whatever tools it requests, feeding the results back — until it answers without calling a tool. There is no conversation across prompts yet.

Course modules, in order, and where this port stands:

| Module | Topic | Status |
| --- | --- | --- |
| Agent Basics | Single call to the model | Done |
| Tool Calling | Model-invoked Go functions | Done |
| Evals | Scoring non-deterministic output | Done |
| Agent Loop | Multi-step reason/act until done | Done |
| Multi-Turn Evals | System prompts, conversation scoring | Not started |
| File System Tools | Read, write, list, delete | Done |
| Web Search & Context | Search plus window compaction | Not started |
| Shell Tool | Sandboxed command execution | Not started |
| Human Guidance | Approval flow before risky actions | Not started |

## Tools

The agent can call `current_datetime`, `read_file`, `write_file`, `list_files`, and `delete_file`.

`write_file` refuses to replace a file that already exists unless the call sets `overwrite: true`, which the model only does after reading it — otherwise "add a line to notes.md" silently discarded the rest of the file. See [EVALS.md](EVALS.md).

The file tools operate on a temporary workspace created at the start of each run and deleted at the end. It starts empty, the agent cannot reach outside it — absolute paths and paths escaping the workspace are refused — and nothing it writes survives the run. That also means the agent cannot read this repository, only files it created itself.

## Evals

The evals score which tools the agent reached for, not the wording of its answers. Cases are grouped as golden (the prompt names what it wants), secondary (the tool is implied, or several are needed), and negative (no tool should be called at all). Each runs several times and has to clear a pass rate, since the model is non-deterministic even at temperature 0.

They make real API calls, so they only run when asked:

```bash
go test -run TestEval -eval .              # 5 runs per case
go test -run TestEval -eval -eval.runs=10 -v .
```

`go test ./...` skips them. [EVALS.md](EVALS.md) records the current scores and what the suite has caught.

## Telemetry

Every run is traced with OpenTelemetry: one span for the agent loop, one per model call (carrying the prompt, the reply, and token usage), and one per tool call.

Spans are exported over OTLP/gRPC to `localhost:4317` by default. Start a collector before running:

```bash
docker run --rm --name jaeger \
  -p 16686:16686 -p 4317:4317 -p 4318:4318 \
  cr.jaegertracing.io/jaegertracing/jaeger:2.20.0
```

Traces then show up at [localhost:16686](http://localhost:16686). Without a collector the program still works — spans are dropped and a flush error is logged on exit.

Span attributes follow the OpenTelemetry GenAI conventions, so any OTLP backend will do; `telemetry.WithEndpoint` points the exporter at a different one.

## Usage

```bash
go build -o gai .
./gai "Tell me a fun fact about the Go programming language."
```

With no arguments, a default prompt is used.

## Configuration

The OpenAI API key is read from `secrets/openai-api-key`, a file containing nothing but the key:

```bash
mkdir -p secrets
printf '%s' 'sk-...' > secrets/openai-api-key
```

`secrets/` is gitignored. The path is relative to the working directory, so run `gai` from the repository root.

## Requirements

Go 1.26.5 or later.
