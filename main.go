package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

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

	client := openai.NewClient(option.WithAPIKey(apiKey))

	resp, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		},
	})
	if err != nil {
		log.Fatalf("calling API: %v", err)
	}
	if len(resp.Choices) == 0 {
		log.Fatal("no choices returned")
	}

	fmt.Println(resp.Choices[0].Message.Content)
}
