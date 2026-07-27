package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/campoy/gai/agent"
	"github.com/campoy/gai/telemetry"
	"github.com/campoy/gai/tools"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

const (
	// apiKeyPath is relative, so the binary only works from the repo root.
	apiKeyPath = "secrets/openai-api-key"

	// flushTimeout caps how long the program waits for pending spans to reach
	// the collector before exiting.
	flushTimeout = 2 * time.Second
)

func main() {
	apiKey, err := agent.LoadAPIKey(apiKeyPath)
	if err != nil {
		log.Fatalf("loading API key: %v", err)
	}

	ctx := context.Background()

	shutdown, err := telemetry.Init(ctx)
	if err != nil {
		log.Fatalf("setting up telemetry: %v", err)
	}
	// Spans are batched, so they only reach the collector on shutdown. Bound the
	// flush: with no collector listening the exporter otherwise retries until
	// its own timeout, holding up an exit that has nothing left to do.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		defer cancel()
		if err := shutdown(ctx); err != nil {
			log.Printf("flushing telemetry: %v", err)
		}
	}()

	// The file tools work in a throwaway directory, so nothing the agent writes
	// outlives the run or touches the real file system.
	cleanup, err := tools.NewWorkspace()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			log.Printf("removing workspace: %v", err)
		}
	}()

	client := openai.NewClient(option.WithAPIKey(apiKey))
	params := agent.Params()

	// With a prompt on the command line, answer it and stop. With none, read
	// one message per line until stdin closes, carrying the conversation from
	// message to message.
	if len(os.Args) > 1 {
		params.Messages = append(params.Messages, openai.UserMessage(strings.Join(os.Args[1:], " ")))
		answer, err := agent.Run(ctx, &client, &params)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(answer)
		return
	}

	if err := chat(ctx, &client, os.Stdin, os.Stdout, &params); err != nil {
		log.Fatal(err)
	}
}

// chat reads a message per line and replies to each, keeping every turn in the
// conversation so later messages can refer back to earlier ones. It returns
// when stdin closes.
func chat(ctx context.Context, client *openai.Client, in io.Reader, out io.Writer, params *openai.ChatCompletionNewParams) error {
	// Only prompt when someone is there to read it, so piping input in leaves
	// the output clean.
	prompt := func() {}
	if f, ok := in.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			prompt = func() { fmt.Fprint(out, "> ") }
		}
	}

	lines := bufio.NewScanner(in)
	for prompt(); lines.Scan(); prompt() {
		message := strings.TrimSpace(lines.Text())
		if message == "" {
			continue
		}

		params.Messages = append(params.Messages, openai.UserMessage(message))
		answer, err := agent.Run(ctx, client, params)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, answer)
	}
	return lines.Err()
}
