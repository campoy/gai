package agent

import (
	"context"
	"fmt"
	"log"

	"github.com/campoy/gai/telemetry"
	"github.com/openai/openai-go"
	"go.opentelemetry.io/otel/attribute"
)

const (
	// compactAfter is the token budget that triggers compaction. It is far below
	// the model's 128k window on purpose: at the real limit this mechanism would
	// never run outside a synthetic test, and the point of writing it by hand is
	// to be able to watch it work.
	compactAfter = 8000

	// keepTurns is how many of the most recent user messages survive a
	// compaction, along with every assistant reply and tool result that followed
	// them. Two is enough for "and the one before that" to still resolve.
	keepTurns = 2
)

// summaryPreamble labels the summary in the history, so the model treats it as
// a record of what happened rather than as something it said itself.
const summaryPreamble = "Summary of the earlier part of this conversation, which was removed to save space:\n\n"

// summaryPrompt asks for the parts of a conversation that later turns refer
// back to. Prose about what was discussed is useless here; the facts, the file
// contents and the unfinished business are what the next turn needs.
const summaryPrompt = `You are compressing the earlier part of a conversation between a user and an
assistant that has tools for telling the time, searching the web, and reading, writing, listing and
deleting files. What you write replaces those messages entirely — anything you leave out is forgotten.

Record, as compactly as you can while staying specific:

  - facts the user stated about themselves, their work, or what they want
  - files created, changed or deleted, and what they now contain
  - questions the user asked and the answers they were given
  - decisions already made, and anything left unfinished

Use concrete names, values and dates rather than descriptions of them: "the file budget.md contains
'Total: 100'" is useful, "the assistant created a file with a total" is not. Do not add advice,
commentary or anything that did not happen. Write it as notes, not as a story.`

// compact replaces the middle of the conversation with a summary of it, once
// the history has grown past compactAfter tokens.
//
// It is best-effort: every failure path leaves params exactly as it found them,
// because losing a conversation to a failed optimisation is worse than paying
// for a long one. The same reasoning makes runTool return tool failures as text
// rather than exiting.
func (a *Agent) compact(ctx context.Context, params *openai.ChatCompletionNewParams) {
	if a.usedTokens < compactAfter {
		return
	}
	// The system prompt has to be message 0 for the split below to hold. Nothing
	// in this package builds params any other way, but a caller could.
	if len(params.Messages) == 0 || params.Messages[0].OfSystem == nil {
		return
	}
	cut := cutPoint(params.Messages)
	if cut == 0 {
		return
	}

	ctx, span := telemetry.Tracer().Start(ctx, "compact")
	defer span.End()

	dropped := params.Messages[1:cut]
	summary, err := a.summarize(ctx, dropped)
	if err != nil {
		// Recorded on the span and logged, but not returned: the loop carries on
		// with the full history, over budget but correct.
		span.RecordError(err)
		log.Printf("compaction failed, continuing uncompacted: %v", err)
		return
	}

	kept := make([]openai.ChatCompletionMessageParamUnion, 0, len(params.Messages)-len(dropped)+1)
	kept = append(kept, params.Messages[0], openai.SystemMessage(summaryPreamble+summary))
	kept = append(kept, params.Messages[cut:]...)

	span.SetAttributes(
		attribute.Int("gai.compact.messages_before", len(params.Messages)),
		attribute.Int("gai.compact.messages_after", len(kept)),
		attribute.Int("gai.compact.messages_dropped", len(dropped)),
		attribute.Int64("gai.compact.trigger_tokens", a.usedTokens),
	)
	log.Printf("compacted %d messages into a summary, %d remain", len(dropped), len(kept))

	params.Messages = kept
	// The next response reports what the compacted history really costs. Until
	// then assume it is clear, so the next step does not compact all over again
	// on a figure that describes messages no longer present.
	a.usedTokens = 0
}

// summarize asks the model to compress a stretch of conversation into notes.
//
// Tools are left off the request: this call has no business reading a file, and
// a tool call here would be a step the agent never gets to run.
func (a *Agent) summarize(ctx context.Context, dropped []openai.ChatCompletionMessageParamUnion) (string, error) {
	transcript := Transcribe(dropped)
	if transcript == "" {
		return "", fmt.Errorf("nothing to summarise")
	}

	req := openai.ChatCompletionNewParams{
		Model: Model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(summaryPrompt),
			openai.UserMessage(transcript),
		},
	}
	callCtx, span := telemetry.StartLLM(ctx, req.Model, req.Messages)
	resp, err := a.client.Chat.Completions.New(callCtx, req)
	telemetry.EndLLM(span, resp, err)
	if err != nil {
		return "", fmt.Errorf("summarising: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("summarising: no choices returned")
	}
	if resp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("summarising: empty summary")
	}
	return resp.Choices[0].Message.Content, nil
}

// cutPoint returns the index where the surviving tail of the conversation
// begins, or 0 when there is nothing safe to drop.
//
// The cut always lands on a user message. The API rejects a history in which a
// tool message's call is missing, or an assistant message's tool_calls go
// unanswered, and tool traffic always sits between two user messages, never
// across one — so a cut at a user message cannot separate a call from its
// result without tracking a single call id.
//
// It is not the only safe place to cut, just the simplest one to be sure of,
// and it has the better semantics anyway: what survives is whole exchanges
// rather than the tail end of one.
func cutPoint(msgs []openai.ChatCompletionMessageParamUnion) int {
	var users []int
	for i, m := range msgs {
		if m.OfUser != nil {
			users = append(users, i)
		}
	}
	// There is only something to gain if a user message would be dropped: with
	// keepTurns or fewer, the cut would land at the start and drop nothing.
	if len(users) <= keepTurns {
		return 0
	}
	return users[len(users)-keepTurns]
}
