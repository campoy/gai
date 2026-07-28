// Package temporal contains the Temporal workflow and activity scaffold used by the CLI.
package temporal

import (
	"context"
	"fmt"
	"os"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	// DefaultHostPort is the default Temporal endpoint used by the local CLI worker and client.
	DefaultHostPort = "127.0.0.1:7233"
	// DefaultTaskQueue is the task queue used when the CLI does not override it.
	DefaultTaskQueue = "gai"
)

// NewClient creates a Temporal client for the configured host and port.
func NewClient(hostPort string) (client.Client, error) {
	if hostPort == "" {
		hostPort = os.Getenv("TEMPORAL_HOST_PORT")
	}
	if hostPort == "" {
		hostPort = DefaultHostPort
	}
	return client.Dial(client.Options{HostPort: hostPort})
}

// NewWorker creates a Temporal worker for the configured task queue.
func NewWorker(hostPort, taskQueue string) (worker.Worker, error) {
	c, err := NewClient(hostPort)
	if err != nil {
		return nil, err
	}
	if taskQueue == "" {
		taskQueue = DefaultTaskQueue
	}
	return worker.New(c, taskQueue, worker.Options{}), nil
}

// Register wires the workflow and activity into a Temporal worker.
func Register(w worker.Worker) {
	w.RegisterWorkflow(ConversationWorkflow)
	w.RegisterActivity(EchoActivity)
}

// ConversationWorkflow executes the simple echo activity.
func ConversationWorkflow(ctx workflow.Context, prompt string) (string, error) {
	var answer string
	err := workflow.ExecuteActivity(ctx, EchoActivity, prompt).Get(ctx, &answer)
	if err != nil {
		return "", err
	}
	return answer, nil
}

// EchoActivity returns a Temporal echo response for the supplied prompt.
func EchoActivity(ctx context.Context, prompt string) (string, error) {
	return fmt.Sprintf("temporal echo: %s", prompt), nil
}
