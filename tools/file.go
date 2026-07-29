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

// workspace is the temporary directory the file tools are confined to. It is
// created by NewWorkspace at the start of a run and deleted at the end, so the
// agent never touches the real file system and nothing it writes survives.
var workspace string

// NewWorkspace creates an empty temporary directory for the file tools to work
// in and returns a function that deletes it along with everything in it. The
// file tools fail until it has been called.
func NewWorkspace() (cleanup func() error, err error) {
	dir, err := os.MkdirTemp("", "gai-*")
	if err != nil {
		return nil, fmt.Errorf("creating workspace: %w", err)
	}
	if err := SetWorkspace(dir); err != nil {
		return nil, err
	}
	return func() error {
		workspace = ""
		return os.RemoveAll(dir)
	}, nil
}

// SetWorkspace configures the directory the file tools operate in. The
// directory is created if it does not already exist.
func SetWorkspace(dir string) error {
	if dir == "" {
		return fmt.Errorf("workspace path is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating workspace %q: %w", dir, err)
	}
	workspace = dir
	return nil
}

// Workspace reports the directory the file tools operate in, or "" before
// NewWorkspace has been called.
func Workspace() string { return workspace }

// ReadFile returns the contents of a file in the workspace.
var ReadFile = New(
	"read_file",
	"Read a text file and return its contents. Paths are relative to a temporary workspace that only exists for this conversation.",
	pathSchema("Path of the file to read, relative to the workspace.", nil),
	readFile,
)

// WriteFile creates or overwrites a file in the workspace.
var WriteFile = New(
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
	writeFile,
)

// ListFiles lists the entries of a directory in the workspace.
var ListFiles = New(
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
	listFiles,
)

// DeleteFile removes a file from the workspace.
var DeleteFile = New(
	"delete_file",
	"Delete a file. This cannot be undone. Paths are relative to a temporary workspace that only exists for this conversation.",
	pathSchema("Path of the file to delete, relative to the workspace.", nil),
	deleteFile,
)

func readFile(_ context.Context, args string) (string, error) {
	p, err := pathArg(args)
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

func writeFile(_ context.Context, args string) (string, error) {
	var p struct {
		Path      string `json:"path"`
		Content   string `json:"content"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}
	path, err := resolve(p.Path)
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

func listFiles(_ context.Context, args string) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if args != "" {
		if err := json.Unmarshal([]byte(args), &p); err != nil {
			return "", fmt.Errorf("%w: %w", ErrInvalidArgument, err)
		}
	}
	if p.Path == "" {
		p.Path = "."
	}
	dir, err := resolve(p.Path)
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

func deleteFile(_ context.Context, args string) (string, error) {
	p, err := pathArg(args)
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
// resolves it against the working directory.
func pathArg(args string) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &p); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}
	return resolve(p.Path)
}

// resolve turns a model-supplied path into an absolute one inside the
// workspace, rejecting anything that reaches outside it. The model chooses
// these paths from text it was given, so they are untrusted.
//
// Every rejection here is an ErrInvalidArgument: the path is the argument, and
// no amount of running the call again makes a bad one good. The missing
// workspace is the exception — that is the run being set up wrong, not the
// model calling wrong.
func resolve(path string) (string, error) {
	if workspace == "" {
		return "", fmt.Errorf("no workspace: call NewWorkspace before using the file tools")
	}
	if path == "" {
		return "", fmt.Errorf("%w: path is required", ErrInvalidArgument)
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be relative to the workspace, got %q", ErrInvalidArgument, path)
	}

	abs := filepath.Join(workspace, path)
	rel, err := filepath.Rel(workspace, abs)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: path escapes the workspace: %q", ErrInvalidArgument, path)
	}
	return abs, nil
}
