package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestResolve covers the sandbox the file tools rely on. The model picks these
// paths out of untrusted text, so every case here is one it could be talked
// into trying.
func TestResolve(t *testing.T) {
	w := newTestWorkspace(t)

	rejected := []string{
		"",                       // no path at all
		"/etc/passwd",            // absolute
		"../../../../etc/passwd", // traversal
		"sub/../../outside.txt",  // traversal via a subdirectory
	}
	for _, path := range rejected {
		got, err := w.resolve(path)
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

// TestZeroWorkspaceReachesNothing is the sandbox's floor. A Workspace only ever
// comes from NewWorkspace or OpenWorkspace, so the zero value means the tools
// were built wrong — but if it ever reached filepath.Join it would resolve
// against the process's own working directory, which is the repository gai is
// running in. Every file tool has to refuse it, and refuse it without writing
// anything.
func TestZeroWorkspaceReachesNothing(t *testing.T) {
	var zero Workspace
	ctx := t.Context()

	if got, err := zero.resolve("notes.md"); err == nil {
		t.Errorf("resolve with no workspace = %q, want error", got)
	}

	// A name nothing else would create, so finding it afterwards means a tool
	// wrote into the working directory rather than into a workspace.
	const probe = "gai-zero-workspace-probe.md"
	t.Cleanup(func() { _ = os.Remove(probe) })

	for _, c := range []struct {
		name string
		fn   Function
		args string
	}{
		{"read_file", zero.readFile, fmt.Sprintf(`{"path":%q}`, probe)},
		{"write_file", zero.writeFile, fmt.Sprintf(`{"path":%q,"content":"escaped"}`, probe)},
		{"list_files", zero.listFiles, `{}`},
		{"delete_file", zero.deleteFile, fmt.Sprintf(`{"path":%q}`, probe)},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got, err := c.fn(ctx, c.args); err == nil {
				t.Errorf("%s with no workspace = %q, want error", c.name, got)
			}
		})
	}

	if _, err := os.Stat(probe); !os.IsNotExist(err) {
		t.Fatalf("%s exists in the working directory: a file tool with no workspace wrote outside the sandbox", probe)
	}
}

// TestWorkspacesAreIndependent pins down what the workspace being a value
// rather than package state buys: two of them live at once, each tool confined
// to its own directory. Two Temporal activities on one worker used to share a
// directory, and read and wrote each other's files.
func TestWorkspacesAreIndependent(t *testing.T) {
	a, b := newTestWorkspace(t), newTestWorkspace(t)
	ctx := t.Context()

	if _, err := a.writeFile(ctx, `{"path":"notes.md","content":"a's notes"}`); err != nil {
		t.Fatalf("writeFile in a: %v", err)
	}
	if _, err := b.writeFile(ctx, `{"path":"notes.md","content":"b's notes"}`); err != nil {
		t.Fatalf("writeFile in b: %v", err)
	}

	for _, c := range []struct {
		name string
		w    Workspace
		want string
	}{
		{"a", a, "a's notes"},
		{"b", b, "b's notes"},
	} {
		got, err := c.w.readFile(ctx, `{"path":"notes.md"}`)
		if err != nil {
			t.Fatalf("readFile in %s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s read %q, want %q — the two workspaces are sharing a directory", c.name, got, c.want)
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

// TestWorkspacesAreConcurrencySafe is the regression test for the bug this all
// exists to fix: the workspace used to be one package-level string that every
// tool call read, so two runs sharing a worker overwrote each other's directory
// and then resolved cleanly into it. Under -race this also fails outright if
// mutable package state is ever reintroduced.
func TestWorkspacesAreConcurrencySafe(t *testing.T) {
	ctx := t.Context()

	const (
		workers = 4
		rounds  = 25
	)
	workspaces := make([]Workspace, workers)
	for i := range workspaces {
		workspaces[i] = newTestWorkspace(t)
	}

	var wg sync.WaitGroup
	for i, w := range workspaces {
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := fmt.Sprintf("worker %d", i)
			args := fmt.Sprintf(`{"path":"notes.md","content":%q,"overwrite":true}`, want)
			for range rounds {
				if _, err := w.writeFile(ctx, args); err != nil {
					t.Errorf("worker %d: writeFile: %v", i, err)
					return
				}
				got, err := w.readFile(ctx, `{"path":"notes.md"}`)
				if err != nil {
					t.Errorf("worker %d: readFile: %v", i, err)
					return
				}
				if got != want {
					t.Errorf("worker %d read %q, want %q — workspaces are being shared", i, got, want)
					return
				}
				if _, err := w.listFiles(ctx, `{}`); err != nil {
					t.Errorf("worker %d: listFiles: %v", i, err)
					return
				}
			}
		}()
	}
	wg.Wait()
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
func newTestWorkspace(t *testing.T) Workspace {
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
