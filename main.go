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
		Tools: toolParams(tools.All),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("You are a sassy twink with a sharp wit. Slay, queen!"),
			openai.UserMessage(prompt),
		},
	}

	msg, err := complete(ctx, &client, params)
	if err != nil {
		log.Fatal(err)
	}

	if len(msg.ToolCalls) > 0 {
		// The assistant turn that requested the tools has to go back too; a tool
		// result without its call is rejected by the API.
		params.Messages = append(params.Messages, msg.ToParam())
		for _, tc := range msg.ToolCalls {
			params.Messages = append(params.Messages, openai.ToolMessage(runTool(tc), tc.ID))
		}
		if msg, err = complete(ctx, &client, params); err != nil {
			log.Fatal(err)
		}
	}

	fmt.Println(msg.Content)
}

// toolParams converts the tool registry into the schema the API advertises to
// the model. Only the name links a schema back to its implementation.
func toolParams(ts []tools.Tool) []openai.ChatCompletionToolParam {
	params := make([]openai.ChatCompletionToolParam, len(ts))
	for i, t := range ts {
		params[i] = openai.ChatCompletionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  t.Parameters,
			},
		}
	}
	return params
}

// complete makes one request and returns the assistant message.
func complete(ctx context.Context, client *openai.Client, params openai.ChatCompletionNewParams) (openai.ChatCompletionMessage, error) {
	resp, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		return openai.ChatCompletionMessage{}, fmt.Errorf("calling API: %w", err)
	}
	if len(resp.Choices) == 0 {
		return openai.ChatCompletionMessage{}, fmt.Errorf("no choices returned")
	}
	return resp.Choices[0].Message, nil
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
	return out
}
