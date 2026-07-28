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
	DefaultHostPort  = "127.0.0.1:7233"
	DefaultTaskQueue = "gai"
)

func NewClient(hostPort string) (client.Client, error) {
	if hostPort == "" {
		hostPort = os.Getenv("TEMPORAL_HOST_PORT")
	}
	if hostPort == "" {
		hostPort = DefaultHostPort
	}
	return client.NewClient(client.Options{HostPort: hostPort})
}

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

func Register(w worker.Worker) {
	w.RegisterWorkflow(ConversationWorkflow)
	w.RegisterActivity(EchoActivity)
}

func ConversationWorkflow(ctx workflow.Context, prompt string) (string, error) {
	var answer string
	err := workflow.ExecuteActivity(ctx, EchoActivity, prompt).Get(ctx, &answer)
	if err != nil {
		return "", err
	}
	return answer, nil
}

func EchoActivity(ctx context.Context, prompt string) (string, error) {
	return fmt.Sprintf("temporal echo: %s", prompt), nil
}
