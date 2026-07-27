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
| Evals | Scoring non-deterministic output | Not started |
| Agent Loop | Multi-step reason/act until done | Done |
| Multi-Turn Evals | System prompts, conversation scoring | Not started |
| File System Tools | Read, write, list, delete | Done |
| Web Search & Context | Search plus window compaction | Not started |
| Shell Tool | Sandboxed command execution | Not started |
| Human Guidance | Approval flow before risky actions | Not started |

## Tools

The agent can call `current_datetime`, `read_file`, `write_file`, `list_files`, and `delete_file`.

The file tools are confined to the working directory: absolute paths, paths escaping it, and anything under `secrets/` or `.git/` are refused. There is no approval prompt yet, so `write_file` and `delete_file` act immediately — run the agent somewhere you don't mind it editing.

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
