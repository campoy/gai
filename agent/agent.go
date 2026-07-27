// Package agent holds the loop that turns a prompt into an answer: ask the
// model, run the tools it asks for, feed the results back, repeat.
//
// It lives apart from the command so the evals can drive the same loop, with
// the same prompt and the same defaults, that the CLI ships.
package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/campoy/gai/telemetry"
	"github.com/campoy/gai/tools"
	"github.com/openai/openai-go"
)

const (
	// Model is the model every request goes to.
	Model = openai.ChatModelGPT4oMini

	// SystemPrompt is exported so the evals score the prompt that actually
	// ships rather than one written for the test.
	SystemPrompt = "You are a sassy twink with a sharp wit. Slay, queen!"

	// maxSteps bounds the loop so a model that keeps calling tools without ever
	// answering can't run forever.
	maxSteps = 10
)

// LoadAPIKey reads an API key from a file containing nothing but the key. The
// path is the caller's business: it is relative to the working directory, which
// differs between the command and a test binary.
func LoadAPIKey(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return key, nil
}

// Params returns the request the agent runs with: the model, the tool
// registry, and the system prompt as the opening message. Callers append user
// messages to Messages, and the evals override Temperature to make runs
// comparable.
func Params() openai.ChatCompletionNewParams {
	return openai.ChatCompletionNewParams{
		Model: Model,
		Tools: tools.All.AsToolParams(),
		// "auto" lets the model decide whether to call a tool. It is already the
		// default whenever Tools is non-empty; "none" and "required" are the
		// other choices, and a named tool can be forced with
		// ChatCompletionToolChoiceOptionParamOfChatCompletionNamedToolChoice.
		ToolChoice: openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: openai.String("auto"),
		},
		Temperature: openai.Float(1),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(SystemPrompt),
		},
	}
}

// Run drives the agent loop: ask the model, run whatever tools it requests,
// feed the results back, and repeat until it answers without calling a tool.
//
// Every turn is appended to params, including the model's final answer, so a
// caller holding the same params across several messages gets a conversation
// rather than a series of unrelated questions.
func Run(ctx context.Context, client *openai.Client, params *openai.ChatCompletionNewParams) (string, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "agent")
	defer span.End()

	for range maxSteps {
		callCtx, callSpan := telemetry.StartLLM(ctx, params.Model, params.Messages)
		resp, err := client.Chat.Completions.New(callCtx, *params)
		telemetry.EndLLM(callSpan, resp, err)
		if err != nil {
			return "", fmt.Errorf("calling API: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no choices returned")
		}
		msg := resp.Choices[0].Message
		// The assistant turn goes back whether or not it asked for tools: a tool
		// result without its call is rejected by the API, and an answer missing
		// from the history is one the next message cannot refer to.
		params.Messages = append(params.Messages, msg.ToParam())

		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}
		for _, tc := range msg.ToolCalls {
			params.Messages = append(params.Messages, openai.ToolMessage(runTool(ctx, tc), tc.ID))
		}
	}
	return "", fmt.Errorf("gave up after %d steps without a final answer", maxSteps)
}

// runTool executes a tool call, returning failures as text so the model can
// recover from them rather than the program exiting. Arguments are
// model-generated JSON, so they may be malformed or contain undeclared fields.
func runTool(ctx context.Context, tc openai.ChatCompletionMessageToolCall) string {
	_, span := telemetry.StartTool(ctx, tc.Function.Name, tc.Function.Arguments)
	defer span.End()

	t, ok := tools.ByName(tc.Function.Name)
	if !ok {
		return "error: unknown tool " + tc.Function.Name
	}
	out, err := t.Func(tc.Function.Arguments)
	if err != nil {
		return "error: " + err.Error()
	}
	log.Printf("tool %q called with args %q, returned %q", t.Name, tc.Function.Arguments, out)
	return out
}
