package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const (
	apiKeyPath = "secrets/openai-api-key"
	model      = openai.ChatModelGPT4oMini
)

// tools are the function schemas advertised to the model. Each Name must have a
// matching entry in toolFuncs; that string is the only thing linking the two.
var tools = []openai.ChatCompletionToolParam{{
	Function: openai.FunctionDefinitionParam{
		Name:        "current_datetime",
		Description: openai.String("Current date and time. Use for anything about today, now, or the current year."),
		Parameters: openai.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"timezone": map[string]any{
					"type":        "string",
					"description": "IANA timezone name, e.g. Europe/Madrid. Omit for server local time.",
				},
			},
		},
	},
}}

// toolFuncs dispatches a tool call by name. Arguments are model-generated JSON,
// so they may be malformed or contain undeclared fields.
var toolFuncs = map[string]func(json.RawMessage) (string, error){
	"current_datetime": currentDateTime,
}

func currentDateTime(args json.RawMessage) (string, error) {
	var p struct {
		Timezone string `json:"timezone"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", err
		}
	}
	loc := time.Local
	if p.Timezone != "" {
		l, err := time.LoadLocation(p.Timezone)
		if err != nil {
			return "", fmt.Errorf("unknown timezone %q", p.Timezone)
		}
		loc = l
	}
	return time.Now().In(loc).Format(time.RFC1123), nil
}

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
		Tools: tools,
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
// recover from them rather than the program exiting.
func runTool(tc openai.ChatCompletionMessageToolCall) string {
	fn, ok := toolFuncs[tc.Function.Name]
	if !ok {
		return "error: unknown tool " + tc.Function.Name
	}
	out, err := fn(json.RawMessage(tc.Function.Arguments))
	if err != nil {
		return "error: " + err.Error()
	}
	return out
}
