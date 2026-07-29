package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxFileSize caps how much of a file is handed to the model. Anything larger
// is truncated rather than refused, since a partial read is usually enough to
// answer a question and a whole file can swamp the context window.
const maxFileSize = 64 << 10

// A Workspace is the directory the file tools are confined to, so the agent
// never touches the real file system and nothing it writes survives the run.
//
// It is handed to the tools when they are built rather than kept in package
// state, the way web_search closes over its client. Two agents running at once
// — two Temporal activities on one worker, say — then each resolve their paths
// against their own directory, instead of racing on one shared string and
// reading and writing each other's files.
type Workspace struct{ dir string }

// NewWorkspace creates an empty temporary directory for the file tools to work
// in and returns it along with a function that deletes it and everything in it.
func NewWorkspace() (ws *Workspace, cleanup func() error, err error) {
	dir, err := os.MkdirTemp("", "gai-*")
	if err != nil {
		return nil, nil, fmt.Errorf("creating workspace: %w", err)
	}
	return &Workspace{dir: dir}, func() error { return os.RemoveAll(dir) }, nil
}

// OpenWorkspace returns a workspace rooted at a directory chosen by the caller,
// creating it if it is not there yet. Unlike NewWorkspace it hands back no
// cleanup function: whoever named the directory owns its lifetime.
func OpenWorkspace(dir string) (*Workspace, error) {
	if dir == "" {
		return nil, fmt.Errorf("workspace path is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating workspace %q: %w", dir, err)
	}
	return &Workspace{dir: dir}, nil
}

// Dir reports the directory the workspace is rooted at, or "" for a nil
// workspace, whose file tools refuse every path they are given.
func (w *Workspace) Dir() string {
	if w == nil {
		return ""
	}
	return w.dir
}

// NewReadFile returns a tool that reads a file in the workspace.
func NewReadFile(w *Workspace) Tool {
	return New(
		"read_file",
		"Read a text file and return its contents. Paths are relative to a temporary workspace that only exists for this conversation.",
		pathSchema("Path of the file to read, relative to the workspace.", nil),
		w.readFile,
	)
}

// NewWriteFile returns a tool that creates or overwrites a file in the workspace.
func NewWriteFile(w *Workspace) Tool {
	return New(
		"write_file",
		"Write text to a file, creating it if needed. This REPLACES the whole file: anything already in it is lost. To add to, edit, or append to a file that already exists, call read_file first and write back the old contents together with the new. Paths are relative to a temporary workspace that only exists for this conversation, so nothing written here is permanent.",
		pathSchema("Path of the file to write, relative to the workspace.", map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "Full contents of the file after the write. Not a fragment to append — whatever is not included here is deleted.",
			},
			"overwrite": map[string]any{
				"type":        "boolean",
				"description": "Set true only after reading an existing file, to confirm you are replacing its contents on purpose. Required when the file already exists.",
			},
		}, "content"),
		w.writeFile,
	)
}

// NewListFiles returns a tool that lists the entries of a directory in the workspace.
func NewListFiles(w *Workspace) Tool {
	return New(
		"list_files",
		"List the files and directories in a directory. Paths are relative to a temporary workspace that only exists for this conversation; omit the path to list the workspace itself, which starts out empty.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory to list, relative to the workspace. Defaults to the workspace root.",
				},
			},
		},
		w.listFiles,
	)
}

// NewDeleteFile returns a tool that removes a file from the workspace.
func NewDeleteFile(w *Workspace) Tool {
	return New(
		"delete_file",
		"Delete a file. This cannot be undone. Paths are relative to a temporary workspace that only exists for this conversation.",
		pathSchema("Path of the file to delete, relative to the workspace.", nil),
		w.deleteFile,
	)
}

func (w *Workspace) readFile(_ context.Context, args string) (string, error) {
	p, err := w.pathArg(args)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, use list_files", filepath.Base(p))
	}

	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if len(b) > maxFileSize {
		return string(b[:maxFileSize]) + fmt.Sprintf("\n\n[truncated, %d bytes total]", len(b)), nil
	}
	return string(b), nil
}

func (w *Workspace) writeFile(_ context.Context, args string) (string, error) {
	var p struct {
		Path      string `json:"path"`
		Content   string `json:"content"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "", err
	}
	path, err := w.resolve(p.Path)
	if err != nil {
		return "", err
	}

	// Refuse to clobber a file the model has not acknowledged. Describing the
	// danger in the tool description was not enough on its own: the model wrote
	// straight over an existing file every time, reporting success while its
	// contents were lost. An error it has to handle is not so easy to ignore.
	if !p.Overwrite {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return "", fmt.Errorf("%s already exists (%d bytes); read it first, then call again with overwrite=true and the full contents you want it to end up with",
				p.Path, info.Size())
		}
	}
	// The workspace starts empty, so any subdirectory the model asks for has to
	// be created here or every nested write fails.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(p.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path), nil
}

func (w *Workspace) listFiles(_ context.Context, args string) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if args != "" {
		if err := json.Unmarshal([]byte(args), &p); err != nil {
			return "", err
		}
	}
	if p.Path == "" {
		p.Path = "."
	}
	dir, err := w.resolve(p.Path)
	if err != nil {
		return "", err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return fmt.Sprintf("%s is empty", p.Path), nil
	}
	sort.Strings(names)
	return strings.Join(names, "\n"), nil
}

func (w *Workspace) deleteFile(_ context.Context, args string) (string, error) {
	p, err := w.pathArg(args)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory; this tool only deletes files", filepath.Base(p))
	}
	if err := os.Remove(p); err != nil {
		return "", err
	}
	return "deleted " + filepath.Base(p), nil
}

// pathSchema builds the JSON Schema for a tool whose only required argument is
// a path, plus any extra properties it also requires.
func pathSchema(description string, extra map[string]any, alsoRequired ...string) map[string]any {
	props := map[string]any{
		"path": map[string]any{
			"type":        "string",
			"description": description,
		},
	}
	maps.Copy(props, extra)
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   append([]string{"path"}, alsoRequired...),
	}
}

// pathArg unmarshals the path argument shared by most of these tools and
// resolves it against the workspace.
func (w *Workspace) pathArg(args string) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "", err
	}
	return w.resolve(p.Path)
}

// resolve turns a model-supplied path into an absolute one inside the
// workspace, rejecting anything that reaches outside it. The model chooses
// these paths from text it was given, so they are untrusted.
func (w *Workspace) resolve(path string) (string, error) {
	dir := w.Dir()
	if dir == "" {
		return "", fmt.Errorf("no workspace: the file tools were built without one")
	}
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be relative to the workspace, got %q", path)
	}

	abs := filepath.Join(dir, path)
	rel, err := filepath.Rel(dir, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the workspace: %q", path)
	}
	return abs, nil
}
