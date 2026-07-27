package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/campoy/gai/tools"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const (
	apiKeyPath = "secrets/openai-api-key"
	model      = openai.ChatModelGPT4oMini

	// maxSteps bounds the agent loop so a model that keeps calling tools
	// without ever answering can't run forever.
	maxSteps = 10
)

// loadAPIKey reads the API key from a file containing nothing but the key.
func loadAPIKey(path string) (string, error) {
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

func main() {
	apiKey, err := loadAPIKey(apiKeyPath)
	if err != nil {
		log.Fatalf("loading API key: %v", err)
	}

	prompt := "Hello! Tell me a fun fact about the Go programming language."
	if len(os.Args) > 1 {
		prompt = strings.Join(os.Args[1:], " ")
	}

	ctx := context.Background()
	client := openai.NewClient(option.WithAPIKey(apiKey))

	params := openai.ChatCompletionNewParams{
		Model: model,
		Tools: tools.All.AsToolParams(),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("You are a sassy twink with a sharp wit. Slay, queen!"),
			openai.UserMessage(prompt),
		},
	}

	answer, err := run(ctx, &client, params)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(answer)
}

// run drives the agent loop: ask the model, run whatever tools it requests,
// feed the results back, and repeat until it answers without calling a tool.
func run(ctx context.Context, client *openai.Client, params openai.ChatCompletionNewParams) (string, error) {
	for range maxSteps {
		resp, err := client.Chat.Completions.New(ctx, params)
		if err != nil {
			return "", fmt.Errorf("calling API: %w", err)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no choices returned")
		}
		msg := resp.Choices[0].Message

		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}
		// The assistant turn that requested the tools has to go back too; a tool
		// result without its call is rejected by the API.
		params.Messages = append(params.Messages, msg.ToParam())
		for _, tc := range msg.ToolCalls {
			params.Messages = append(params.Messages, openai.ToolMessage(runTool(tc), tc.ID))
		}
	}
	return "", fmt.Errorf("gave up after %d steps without a final answer", maxSteps)
}

// runTool executes a tool call, returning failures as text so the model can
// recover from them rather than the program exiting. Arguments are
// model-generated JSON, so they may be malformed or contain undeclared fields.
func runTool(tc openai.ChatCompletionMessageToolCall) string {
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
