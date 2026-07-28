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
	gaitemporal "github.com/campoy/gai/temporal"
	"github.com/campoy/gai/tools"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	temporalclient "go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
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

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "worker":
			if err := runWorker(apiKey); err != nil {
				log.Fatal(err)
			}
			return
		case "temporal":
			if len(os.Args) < 3 {
				log.Fatalf("usage: %s temporal <prompt>", os.Args[0])
			}
			answer, err := runTemporal(ctx, strings.Join(os.Args[2:], " "))
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println(answer)
			return
		}
	}

	client := openai.NewClient(option.WithAPIKey(apiKey))
	ag := agent.New(&client)
	params := ag.Params()

	// With a prompt on the command line, answer it and stop. With none, read
	// one message per line until stdin closes, carrying the conversation from
	// message to message.
	if len(os.Args) > 1 {
		params.Messages = append(params.Messages, openai.UserMessage(strings.Join(os.Args[1:], " ")))
		answer, err := ag.Run(ctx, &params)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(answer)
		return
	}

	if err := chat(ctx, ag, os.Stdin, os.Stdout, &params); err != nil {
		log.Fatal(err)
	}
}

func runWorker(apiKey string) error {
	if apiKey != "" {
		if err := gaitemporal.ConfigureAPIKey(apiKey); err != nil {
			return err
		}
	}
	w, err := gaitemporal.NewWorker("", "")
	if err != nil {
		return err
	}
	gaitemporal.Register(w)
	return w.Run(temporalworker.InterruptCh())
}

func runTemporal(ctx context.Context, prompt string) (string, error) {
	workspaceDir, err := os.MkdirTemp("", "gai-workspace-*")
	if err != nil {
		return "", fmt.Errorf("creating workspace: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(workspaceDir)
	}()

	c, err := gaitemporal.NewClient("")
	if err != nil {
		return "", err
	}
	workflowID := fmt.Sprintf("gai-%d", time.Now().UnixNano())
	we, err := c.ExecuteWorkflow(ctx, temporalclient.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: gaitemporal.DefaultTaskQueue,
	}, gaitemporal.AgentWorkflow, prompt, workspaceDir)
	if err != nil {
		return "", err
	}
	var answer string
	if err := we.Get(ctx, &answer); err != nil {
		return "", err
	}
	return answer, nil
}

// chat reads a message per line and replies to each, keeping every turn in the
// conversation so later messages can refer back to earlier ones. It returns
// when stdin closes.
func chat(ctx context.Context, ag *agent.Agent, in io.Reader, out io.Writer, params *openai.ChatCompletionNewParams) error {
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
		answer, err := ag.Run(ctx, params)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, answer)
	}
	return lines.Err()
}
