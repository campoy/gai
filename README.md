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
| File System Tools | Read, write, list, delete | Not started |
| Web Search & Context | Search plus window compaction | Not started |
| Shell Tool | Sandboxed command execution | Not started |
| Human Guidance | Approval flow before risky actions | Not started |

The course covers telemetry with Laminar; the Go equivalent is undecided.

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
