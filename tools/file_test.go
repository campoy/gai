package tools

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolve covers the sandbox the file tools rely on. The model picks these
// paths out of untrusted text, so every case here is one it could be talked
// into trying.
func TestResolve(t *testing.T) {
	if _, err := resolve("anything"); err == nil {
		t.Error("resolve succeeded with no workspace, want error")
	}

	newTestWorkspace(t)

	rejected := []string{
		"",                       // no path at all
		"/etc/passwd",            // absolute
		"../../../../etc/passwd", // traversal
		"sub/../../outside.txt",  // traversal via a subdirectory
	}
	for _, path := range rejected {
		got, err := resolve(path)
		if err == nil {
			t.Errorf("resolve(%q) = %q, want error", path, got)
			continue
		}
		// A rejected path is the model's argument, not a passing condition, so
		// it is classified for the callers that must not retry it.
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("resolve(%q) = %v, want an ErrInvalidArgument", path, err)
		}
	}

	accepted := []string{"notes.md", "a/b/c.txt", "./README.md"}
	for _, path := range accepted {
		got, err := resolve(path)
		if err != nil {
			t.Errorf("resolve(%q) = error %v, want success", path, err)
			continue
		}
		if !strings.HasPrefix(got, workspace+string(filepath.Separator)) {
			t.Errorf("resolve(%q) = %q, want a path inside %q", path, got, workspace)
		}
	}
}

// TestWorkspaceLifecycle checks that the workspace is real while a run is in
// progress and gone afterwards.
func TestWorkspaceLifecycle(t *testing.T) {
	newTestWorkspace(t)
	ctx := t.Context()

	if _, err := writeFile(ctx, `{"path":"notes/todo.md","content":"buy milk"}`); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	got, err := readFile(ctx, `{"path":"notes/todo.md"}`)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if got != "buy milk" {
		t.Errorf("readFile = %q, want %q", got, "buy milk")
	}

	listing, err := listFiles(ctx, `{}`)
	if err != nil {
		t.Fatalf("listFiles: %v", err)
	}
	if listing != "notes/" {
		t.Errorf("listFiles = %q, want %q", listing, "notes/")
	}

	if _, err := deleteFile(ctx, `{"path":"notes/todo.md"}`); err != nil {
		t.Fatalf("deleteFile: %v", err)
	}
	if _, err := readFile(ctx, `{"path":"notes/todo.md"}`); err == nil {
		t.Error("readFile succeeded after delete, want error")
	}
}

// TestWriteFileRefusesToClobber covers the guard that stops the model
// replacing a file it has not read.
func TestWriteFileRefusesToClobber(t *testing.T) {
	newTestWorkspace(t)
	ctx := t.Context()

	if _, err := writeFile(ctx, `{"path":"notes.md","content":"first"}`); err != nil {
		t.Fatalf("writing a new file: %v", err)
	}

	if _, err := writeFile(ctx, `{"path":"notes.md","content":"second"}`); err == nil {
		t.Error("overwrote an existing file without overwrite=true, want error")
	}
	got, err := readFile(ctx, `{"path":"notes.md"}`)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if got != "first" {
		t.Errorf("contents = %q after a refused write, want %q", got, "first")
	}

	if _, err := writeFile(ctx, `{"path":"notes.md","content":"second","overwrite":true}`); err != nil {
		t.Fatalf("overwriting with overwrite=true: %v", err)
	}
	if got, _ := readFile(ctx, `{"path":"notes.md"}`); got != "second" {
		t.Errorf("contents = %q after an acknowledged write, want %q", got, "second")
	}
}

// newTestWorkspace creates a workspace and removes it when the test ends.
func newTestWorkspace(t *testing.T) {
	t.Helper()
	cleanup, err := NewWorkspace()
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
}
