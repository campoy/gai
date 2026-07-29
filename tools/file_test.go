package tools

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestResolve covers the sandbox the file tools rely on. The model picks these
// paths out of untrusted text, so every case here is one it could be talked
// into trying.
func TestResolve(t *testing.T) {
	// A tool built without a workspace has nothing to resolve against and must
	// say so rather than reaching into the process's working directory.
	var none *Workspace
	if _, err := none.resolve("anything"); err == nil {
		t.Error("resolve succeeded with no workspace, want error")
	}

	w := newTestWorkspace(t)

	rejected := []string{
		"",                       // no path at all
		"/etc/passwd",            // absolute
		"../../../../etc/passwd", // traversal
		"sub/../../outside.txt",  // traversal via a subdirectory
	}
	for _, path := range rejected {
		if got, err := w.resolve(path); err == nil {
			t.Errorf("resolve(%q) = %q, want error", path, got)
		}
	}

	accepted := []string{"notes.md", "a/b/c.txt", "./README.md"}
	for _, path := range accepted {
		got, err := w.resolve(path)
		if err != nil {
			t.Errorf("resolve(%q) = error %v, want success", path, err)
			continue
		}
		if !strings.HasPrefix(got, w.Dir()+string(filepath.Separator)) {
			t.Errorf("resolve(%q) = %q, want a path inside %q", path, got, w.Dir())
		}
	}
}

// TestWorkspacesAreIndependent pins down what the workspace being a value
// rather than package state buys: two of them live at once, each tool set
// confined to its own directory. Two Temporal activities on one worker did
// share a directory, and read and wrote each other's files.
func TestWorkspacesAreIndependent(t *testing.T) {
	a, b := newTestWorkspace(t), newTestWorkspace(t)
	ctx := t.Context()

	if _, err := a.writeFile(ctx, `{"path":"notes.md","content":"a's notes"}`); err != nil {
		t.Fatalf("writeFile in a: %v", err)
	}
	if _, err := b.writeFile(ctx, `{"path":"notes.md","content":"b's notes"}`); err != nil {
		t.Fatalf("writeFile in b: %v", err)
	}

	for _, tc := range []struct {
		name string
		w    *Workspace
		want string
	}{
		{"a", a, "a's notes"},
		{"b", b, "b's notes"},
	} {
		got, err := tc.w.readFile(ctx, `{"path":"notes.md"}`)
		if err != nil {
			t.Fatalf("readFile in %s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s read %q, want %q — the two workspaces are sharing a directory", tc.name, got, tc.want)
		}
	}

	// And deleting through one leaves the other's copy alone.
	if _, err := a.deleteFile(ctx, `{"path":"notes.md"}`); err != nil {
		t.Fatalf("deleteFile in a: %v", err)
	}
	if _, err := b.readFile(ctx, `{"path":"notes.md"}`); err != nil {
		t.Errorf("deleting a's file reached into b: %v", err)
	}
}

// TestWorkspaceLifecycle checks that the workspace is real while a run is in
// progress and gone afterwards.
func TestWorkspaceLifecycle(t *testing.T) {
	w := newTestWorkspace(t)
	ctx := t.Context()

	if _, err := w.writeFile(ctx, `{"path":"notes/todo.md","content":"buy milk"}`); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	got, err := w.readFile(ctx, `{"path":"notes/todo.md"}`)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if got != "buy milk" {
		t.Errorf("readFile = %q, want %q", got, "buy milk")
	}

	listing, err := w.listFiles(ctx, `{}`)
	if err != nil {
		t.Fatalf("listFiles: %v", err)
	}
	if listing != "notes/" {
		t.Errorf("listFiles = %q, want %q", listing, "notes/")
	}

	if _, err := w.deleteFile(ctx, `{"path":"notes/todo.md"}`); err != nil {
		t.Fatalf("deleteFile: %v", err)
	}
	if _, err := w.readFile(ctx, `{"path":"notes/todo.md"}`); err == nil {
		t.Error("readFile succeeded after delete, want error")
	}
}

// TestWriteFileRefusesToClobber covers the guard that stops the model
// replacing a file it has not read.
func TestWriteFileRefusesToClobber(t *testing.T) {
	w := newTestWorkspace(t)
	ctx := t.Context()

	if _, err := w.writeFile(ctx, `{"path":"notes.md","content":"first"}`); err != nil {
		t.Fatalf("writing a new file: %v", err)
	}

	if _, err := w.writeFile(ctx, `{"path":"notes.md","content":"second"}`); err == nil {
		t.Error("overwrote an existing file without overwrite=true, want error")
	}
	got, err := w.readFile(ctx, `{"path":"notes.md"}`)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if got != "first" {
		t.Errorf("contents = %q after a refused write, want %q", got, "first")
	}

	if _, err := w.writeFile(ctx, `{"path":"notes.md","content":"second","overwrite":true}`); err != nil {
		t.Fatalf("overwriting with overwrite=true: %v", err)
	}
	if got, _ := w.readFile(ctx, `{"path":"notes.md"}`); got != "second" {
		t.Errorf("contents = %q after an acknowledged write, want %q", got, "second")
	}
}

// newTestWorkspace creates a workspace and removes it when the test ends.
func newTestWorkspace(t *testing.T) *Workspace {
	t.Helper()
	w, cleanup, err := NewWorkspace()
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return w
}
