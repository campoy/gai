package temporal

import (
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"
)

func TestDefaultActivityOptions(t *testing.T) {
	opts := defaultActivityOptions()

	if opts.StartToCloseTimeout != activityStartToCloseTimeout {
		t.Fatalf("unexpected start-to-close timeout: got %v want %v", opts.StartToCloseTimeout, activityStartToCloseTimeout)
	}
	if opts.RetryPolicy == nil {
		t.Fatal("expected retry policy to be configured")
	}
	if opts.RetryPolicy.InitialInterval != time.Second {
		t.Fatalf("unexpected initial interval: got %v want %v", opts.RetryPolicy.InitialInterval, time.Second)
	}
	if opts.RetryPolicy.BackoffCoefficient != 2.0 {
		t.Fatalf("unexpected backoff coefficient: got %v want %v", opts.RetryPolicy.BackoffCoefficient, 2.0)
	}
	if opts.RetryPolicy.MaximumInterval != 30*time.Second {
		t.Fatalf("unexpected maximum interval: got %v want %v", opts.RetryPolicy.MaximumInterval, 30*time.Second)
	}
	if opts.RetryPolicy.MaximumAttempts != 3 {
		t.Fatalf("unexpected maximum attempts: got %d want %d", opts.RetryPolicy.MaximumAttempts, 3)
	}
}

// TestPermanentFailuresAreNonRetryable covers the failures the retry policy
// must not touch. Each of these fails identically on every attempt, so being
// retryable buys nothing but the backoff: three attempts of sleeping before
// the model is told what it already could have been told immediately, and a
// missing key burning the whole budget on every activity of a run that cannot
// succeed.
//
// None of these cases reaches the API: the two that need a key fail before the
// client is built, and read_file makes no call at all.
func TestPermanentFailuresAreNonRetryable(t *testing.T) {
	t.Run("chat completion without an API key", func(t *testing.T) {
		t.Setenv(apiKeyEnvName, "")
		_, err := ChatCompletionActivity(t.Context(), CompletionRequest{WorkspaceDir: t.TempDir()})
		assertNonRetryable(t, err, errTypeMissingAPIKey)
	})

	t.Run("tool without an API key", func(t *testing.T) {
		t.Setenv(apiKeyEnvName, "")
		_, err := RunToolActivity(t.Context(), ToolInvocation{Name: "read_file", WorkspaceDir: t.TempDir()})
		assertNonRetryable(t, err, errTypeMissingAPIKey)
	})

	t.Run("tool the model invented", func(t *testing.T) {
		t.Setenv(apiKeyEnvName, "test-key")
		_, err := RunToolActivity(t.Context(), ToolInvocation{Name: "no_such_tool", WorkspaceDir: t.TempDir()})
		assertNonRetryable(t, err, errTypeUnknownTool)
	})

	t.Run("arguments that do not parse", func(t *testing.T) {
		t.Setenv(apiKeyEnvName, "test-key")
		_, err := RunToolActivity(t.Context(), ToolInvocation{
			Name:         "read_file",
			Arguments:    `{"path":`,
			WorkspaceDir: t.TempDir(),
		})
		assertNonRetryable(t, err, errTypeInvalidToolArgs)
	})

	t.Run("a path outside the workspace", func(t *testing.T) {
		t.Setenv(apiKeyEnvName, "test-key")
		_, err := RunToolActivity(t.Context(), ToolInvocation{
			Name:         "read_file",
			Arguments:    `{"path":"../../etc/passwd"}`,
			WorkspaceDir: t.TempDir(),
		})
		assertNonRetryable(t, err, errTypeInvalidToolArgs)
	})
}

// TestToolFailuresStayRetryable is the other half: a tool that failed for a
// reason other than its arguments keeps the retry policy, so a rate-limited
// web_search is not turned into a permanent failure by this classification.
func TestToolFailuresStayRetryable(t *testing.T) {
	t.Setenv(apiKeyEnvName, "test-key")

	_, err := RunToolActivity(t.Context(), ToolInvocation{
		Name:         "read_file",
		Arguments:    `{"path":"absent.md"}`,
		WorkspaceDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("reading a missing file succeeded, want error")
	}
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) && appErr.NonRetryable() {
		t.Errorf("error %v is non-retryable, want the retry policy to apply", err)
	}
}

// assertNonRetryable checks that an activity error is one Temporal will not
// retry, and that it is labelled with the type the history will show.
func assertNonRetryable(t *testing.T, err error, wantType string) {
	t.Helper()

	if err == nil {
		t.Fatal("got no error, want a non-retryable one")
	}
	var appErr *temporal.ApplicationError
	if !errors.As(err, &appErr) {
		t.Fatalf("got %T (%v), want a *temporal.ApplicationError", err, err)
	}
	if !appErr.NonRetryable() {
		t.Errorf("error %v is retryable, want non-retryable", err)
	}
	if appErr.Type() != wantType {
		t.Errorf("error type = %q, want %q", appErr.Type(), wantType)
	}
}
