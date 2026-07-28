package temporal

import (
	"testing"
	"time"
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
