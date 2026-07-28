// Package temporal contains the Temporal workflow and activity scaffold used by the CLI.
package temporal

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/campoy/gai/agent"
	"github.com/campoy/gai/tools"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	// DefaultHostPort is the default Temporal endpoint used by the local CLI worker and client.
	DefaultHostPort = "127.0.0.1:7233"
	// DefaultTaskQueue is the task queue used when the CLI does not override it.
	DefaultTaskQueue = "gai"
)

const (
	maxWorkflowSteps = 10

	activityStartToCloseTimeout = 5 * time.Minute
)

const apiKeyEnvName = "GAI_API_KEY"

var defaultActivityRetryPolicy = &temporal.RetryPolicy{
	InitialInterval:    time.Second,
	BackoffCoefficient: 2.0,
	MaximumInterval:    30 * time.Second,
	MaximumAttempts:    3,
}

func defaultActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: activityStartToCloseTimeout,
		RetryPolicy:         defaultActivityRetryPolicy,
	}
}

type CompletionRequest struct {
	Messages     []openai.ChatCompletionMessageParamUnion `json:"messages"`
	WorkspaceDir string                                   `json:"workspace_dir"`
}

type CompletionResult struct {
	Message *openai.ChatCompletionMessageParamUnion `json:"message"`
	Usage   int64                                   `json:"usage"`
}

type ToolInvocation struct {
	Name         string `json:"name"`
	Arguments    string `json:"arguments"`
	WorkspaceDir string `json:"workspace_dir"`
}

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

// Register wires the workflow and activities into a Temporal worker.
func Register(w worker.Worker) {
	w.RegisterWorkflow(AgentWorkflow)
	w.RegisterActivity(ChatCompletionActivity)
	w.RegisterActivity(RunToolActivity)
}

// ConfigureAPIKey stores the API key used by the Temporal worker activities.
func ConfigureAPIKey(key string) error {
	if key == "" {
		return fmt.Errorf("api key is required")
	}
	return os.Setenv(apiKeyEnvName, key)
}

// APIKey returns the API key currently configured for the worker activities.
func APIKey() string {
	return os.Getenv(apiKeyEnvName)
}

// AgentWorkflow drives the agent loop through Temporal activities.
func AgentWorkflow(ctx workflow.Context, prompt, workspaceDir string) (string, error) {
	activityCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions())

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(agent.SystemPrompt),
		openai.UserMessage(prompt),
	}

	for range maxWorkflowSteps {
		var result *CompletionResult
		err := workflow.ExecuteActivity(activityCtx, ChatCompletionActivity, CompletionRequest{Messages: messages, WorkspaceDir: workspaceDir}).Get(ctx, &result)
		if err != nil {
			return "", err
		}
		if result == nil || result.Message == nil {
			return "", fmt.Errorf("chat completion activity returned no message")
		}
		messages = append(messages, *result.Message)

		assistant := result.Message.OfAssistant
		if assistant == nil {
			return "", fmt.Errorf("expected assistant message")
		}
		if len(assistant.ToolCalls) == 0 {
			return assistant.Content.OfString.Or(""), nil
		}

		for _, tc := range assistant.ToolCalls {
			var output string
			err := workflow.ExecuteActivity(activityCtx, RunToolActivity, ToolInvocation{Name: tc.Function.Name, Arguments: tc.Function.Arguments, WorkspaceDir: workspaceDir}).Get(ctx, &output)
			if err != nil {
				messages = append(messages, openai.ToolMessage("error: "+err.Error(), tc.ID))
				continue
			}
			messages = append(messages, openai.ToolMessage(output, tc.ID))
		}
	}

	return "", fmt.Errorf("gave up after %d steps without a final answer", maxWorkflowSteps)
}

// ChatCompletionActivity calls the OpenAI chat completions API from a Temporal activity.
func ChatCompletionActivity(ctx context.Context, req CompletionRequest) (*CompletionResult, error) {
	if err := tools.SetWorkspace(req.WorkspaceDir); err != nil {
		return nil, err
	}
	key := APIKey()
	if key == "" {
		return nil, fmt.Errorf("missing API key")
	}

	client := openai.NewClient(option.WithAPIKey(key))
	ag := agent.New(&client)
	params := ag.Params()
	params.Messages = req.Messages

	resp, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned")
	}

	msg := resp.Choices[0].Message.ToParam()
	return &CompletionResult{Message: &msg, Usage: resp.Usage.TotalTokens}, nil
}

// RunToolActivity invokes a registered tool from a Temporal activity.
func RunToolActivity(ctx context.Context, req ToolInvocation) (string, error) {
	if err := tools.SetWorkspace(req.WorkspaceDir); err != nil {
		return "", err
	}
	key := APIKey()
	if key == "" {
		return "", fmt.Errorf("missing API key")
	}

	client := openai.NewClient(option.WithAPIKey(key))
	toolsSet := tools.All(&client)
	tool, ok := toolsSet.ByName(req.Name)
	if !ok {
		return "", fmt.Errorf("unknown tool %s", req.Name)
	}
	return tool.Func(ctx, req.Arguments)
}
