# Go Agent

`gai` is a CLI coding agent written from scratch in Go.

It follows [AI Agents Fundamentals, v2](https://frontendmasters.com/courses/ai-agents-v2) by Scott Moss on Frontend Masters — a workshop that builds a CLI agent from first principles. The course is taught in Node.js and TypeScript with the OpenAI SDK; this repository works through the same material in Go, using [`openai-go`](https://github.com/openai/openai-go).

The goal is to build the agent from primitives rather than adopt a framework: a plain loop over the Chat Completions API, tools as ordinary Go functions, and explicit control over context and approvals.

## Status

Early. The CLI loops on each message — asking the model, running whatever tools it requests, feeding the results back — until it answers without calling a tool. With no arguments it reads messages from stdin and keeps the conversation going across them.

Course modules, in order, and where this port stands:

| Module | Topic | Status |
| --- | --- | --- |
| Agent Basics | Single call to the model | Done |
| Tool Calling | Model-invoked Go functions | Done |
| Evals | Scoring non-deterministic output | Done |
| Agent Loop | Multi-step reason/act until done | Done |
| Multi-Turn Evals | System prompts, conversation scoring | Done |
| File System Tools | Read, write, list, delete | Done |
| Web Search & Context | Search plus window compaction | Done |
| Shell Tool | Sandboxed command execution | Not started |
| Human Guidance | Approval flow before risky actions | Not started |

## Tools

The agent can call `current_datetime`, `web_search`, `read_file`, `write_file`, `list_files`, and `delete_file`.

`web_search` uses OpenAI's hosted search: it sends the query to `gpt-4o-mini-search-preview` with `web_search_options` set, and returns the answer with its source URLs. The search models cannot call functions themselves, so they back a tool rather than running the agent.

`write_file` refuses to replace a file that already exists unless the call sets `overwrite: true`, which the model only does after reading it — otherwise "add a line to notes.md" silently discarded the rest of the file. See [evals/EVALS.md](evals/EVALS.md).

The file tools operate on a temporary workspace created at the start of each run and deleted at the end. It starts empty, the agent cannot reach outside it — absolute paths and paths escaping the workspace are refused — and nothing it writes survives the run. That also means the agent cannot read this repository, only files it created itself.

## Context compaction

A long conversation, or one whose tools return a lot of text, eventually costs more to send than it is worth. Once the history passes a token budget the agent summarises the older middle of it and carries on with the summary in its place:

```
system prompt          system prompt
user: one              summary of the earlier turns
assistant …            user: three
user: two        →     assistant …
assistant …            user: four
user: three
…
```

The system prompt is kept byte for byte, the last two user messages and everything that followed them are kept verbatim, and the stretch between is replaced by one summary written by the model itself.

The cut always lands on a user message. The API rejects a history where a tool result's call is missing, or an assistant's `tool_calls` go unanswered, and tool traffic never spans two user messages — so cutting there cannot orphan either half of a pair. When there is no interior user message to cut at, a single message whose tools returned too much, compaction declines and the request goes out over budget rather than malformed.

The budget is `compactAfter` in `agent/compact.go`, deliberately well below the model's real window so the mechanism is visible rather than theoretical. Compaction is best-effort throughout: if the summary call fails, the conversation continues uncompacted. It logs a line to stderr when it fires.

One consequence worth knowing: `Run` appends every turn to the caller's params, but compaction also *removes* from them. The params hold the conversation as the agent currently sees it, which after a compaction is not everything that was said.

## Evals

`evals/` holds two suites. The trajectory evals score which tools the agent reached for — grouped as golden, secondary, negative and ambiguous, each with a pass rate to clear. The judged evals play multi-turn conversations and hand the whole thing, tool calls and results included, to a model that scores it 1-10 against a rubric.

Both drive the same entry point and defaults the CLI uses. They make real API calls, so they only run when asked:

```bash
go test ./evals/ -eval                                 # everything, 5 runs per case
go test ./evals/ -run TestJudgeConversations -eval -v   # judged conversations only
go test ./evals/ -eval -eval.runs=10 -v                 # more runs per case
```

`go test ./...` skips them. [evals/EVALS.md](evals/EVALS.md) records the current scores and what the suite has caught.

## Telemetry

Every run is traced with OpenTelemetry: one span for the agent loop, one per model call (carrying the prompt, the reply, and token usage), and one per tool call. Tools are handed the loop's context, so a tool that calls the model itself — `web_search` does — is traced beneath the tool call that made it, and is cancelled along with the run.

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

With arguments, the agent answers that one prompt and exits. With none, it reads one message per line from stdin until end of input, carrying the conversation and the workspace from message to message:

```
$ ./gai
> What time is it in Tokyo?
It's 3:07 PM in Tokyo.
> And in Madrid? How many hours behind is that?
In Madrid it's 8:07 AM — Tokyo is 7 hours ahead.
> ^D
```

Input can be piped, in which case the `>` prompt is omitted:

```bash
printf 'Create a.txt saying hello\nWhat files do I have?\n' | ./gai
```

## Configuration

The OpenAI API key is read from `secrets/openai-api-key`, a file containing nothing but the key:

```bash
mkdir -p secrets
printf '%s' 'sk-...' > secrets/openai-api-key
```

`secrets/` is gitignored. The path is relative to the working directory, so run `gai` from the repository root.

## Requirements

Go 1.26.5 or later.
