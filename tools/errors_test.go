package tools

import (
	"errors"
	"testing"
)

// TestInvalidArgumentIsMarked covers the classification the Temporal path keys
// off: a tool call the model got wrong comes back as ErrInvalidArgument, so
// the activity fails on the first attempt instead of retrying arguments that
// cannot start working.
func TestInvalidArgumentIsMarked(t *testing.T) {
	newTestWorkspace(t)
	ctx := t.Context()

	// web_search parses its arguments before it touches the client, so a nil
	// one is enough for the cases below and none of them makes a call.
	search := NewWebSearch(nil).Func

	cases := []struct {
		name string
		fn   Function
		args string
	}{
		{"read_file truncated JSON", readFile, `{"path":`},
		{"write_file truncated JSON", writeFile, `{"path":`},
		{"list_files truncated JSON", listFiles, `{"path":`},
		{"delete_file truncated JSON", deleteFile, `{"path":`},
		{"current_datetime truncated JSON", dateTime, `{"timezone":`},
		{"web_search truncated JSON", search, `{"query":`},

		{"read_file wrong type", readFile, `{"path":42}`},
		{"read_file no path", readFile, `{}`},
		{"read_file absolute path", readFile, `{"path":"/etc/passwd"}`},
		{"delete_file escaping path", deleteFile, `{"path":"../../outside.txt"}`},
		{"current_datetime unknown timezone", dateTime, `{"timezone":"Mars/Olympus"}`},
		{"web_search empty query", search, `{"query":"  "}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.fn(ctx, c.args)
			if !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("error = %v, want an ErrInvalidArgument", err)
			}
		})
	}
}

// TestValidArgumentIsNotMarked pins the other side of the line: the tool
// understood the arguments and refused the operation. Not everything here is
// worth retrying either — the clobber refusal certainly is not — but that is a
// question of what retry policy the tool activity runs under, not of the
// arguments being malformed, so these stay unclassified.
func TestValidArgumentIsNotMarked(t *testing.T) {
	newTestWorkspace(t)
	ctx := t.Context()

	if _, err := readFile(ctx, `{"path":"absent.md"}`); err == nil {
		t.Fatal("reading a missing file succeeded, want error")
	} else if errors.Is(err, ErrInvalidArgument) {
		t.Errorf("reading a missing file = %v, want a plain error", err)
	}

	if _, err := writeFile(ctx, `{"path":"notes.md","content":"first"}`); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	if _, err := writeFile(ctx, `{"path":"notes.md","content":"second"}`); err == nil {
		t.Fatal("clobbering succeeded, want error")
	} else if errors.Is(err, ErrInvalidArgument) {
		t.Errorf("refusing to clobber = %v, want a plain error", err)
	}
}
