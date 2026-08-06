package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAllAdvertisesEveryTool pins the set the model is offered. Only the name
// links a schema to its implementation, so a tool dropped or renamed here is
// invisible until the model asks for something that no longer exists.
func TestAllAdvertisesEveryTool(t *testing.T) {
	// A nil client is enough: web_search parses its arguments before it reaches
	// one, and nothing here calls a tool that would.
	set := All(nil, newTestWorkspace(t))

	want := []string{"current_datetime", "read_file", "write_file", "list_files", "delete_file", "web_search"}
	if len(set) != len(want) {
		t.Errorf("All returned %d tools, want %d", len(set), len(want))
	}
	for _, name := range want {
		if _, ok := set.ByName(name); !ok {
			t.Errorf("All does not include %q", name)
		}
	}
}

// TestAllBuildsEveryFileToolAroundItsWorkspace covers the seam the refactor
// itself could break. TestWorkspacesAreIndependent proves the methods keep to
// their own directory; this proves All actually hands that directory to all
// four of them. A tool built around the wrong workspace — or around none —
// still type-checks and still answers ByName, so nothing but this notices.
func TestAllBuildsEveryFileToolAroundItsWorkspace(t *testing.T) {
	a, b := newTestWorkspace(t), newTestWorkspace(t)
	setA, setB := All(nil, a), All(nil, b)
	ctx := t.Context()

	call := func(set Tools, name, args string) (string, error) {
		t.Helper()
		tool, ok := set.ByName(name)
		if !ok {
			t.Fatalf("%s missing from the set", name)
		}
		return tool.Func(ctx, args)
	}

	// write_file through A must land in A and nowhere else.
	if _, err := call(setA, "write_file", `{"path":"notes.md","content":"a's notes"}`); err != nil {
		t.Fatalf("write_file through set A: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.Dir(), "notes.md")); err != nil {
		t.Fatalf("write_file through set A did not write into A: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.Dir(), "notes.md")); !os.IsNotExist(err) {
		t.Fatalf("write_file through set A wrote into B")
	}

	// read_file and list_files through B must not see A's file.
	if got, err := call(setB, "read_file", `{"path":"notes.md"}`); err == nil {
		t.Errorf("read_file through set B returned %q, want a not-found error", got)
	}
	if got, err := call(setB, "list_files", `{}`); err != nil {
		t.Fatalf("list_files through set B: %v", err)
	} else if got != ". is empty" {
		t.Errorf("list_files through set B = %q, want %q", got, ". is empty")
	}
	if got, err := call(setA, "list_files", `{}`); err != nil {
		t.Fatalf("list_files through set A: %v", err)
	} else if got != "notes.md" {
		t.Errorf("list_files through set A = %q, want %q", got, "notes.md")
	}

	// delete_file through B must not reach A's file either.
	if _, err := call(setB, "delete_file", `{"path":"notes.md"}`); err == nil {
		t.Error("delete_file through set B succeeded, want a not-found error")
	}
	if _, err := os.Stat(filepath.Join(a.Dir(), "notes.md")); err != nil {
		t.Fatalf("delete_file through set B removed A's file: %v", err)
	}

	// And through A it works, which is what proves the case above was a
	// workspace boundary rather than a broken tool.
	if _, err := call(setA, "delete_file", `{"path":"notes.md"}`); err != nil {
		t.Fatalf("delete_file through set A: %v", err)
	}
}
