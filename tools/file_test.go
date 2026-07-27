package tools

import "testing"

// TestResolve covers the sandbox the file tools rely on. The model picks these
// paths out of untrusted text, so every case here is one it could be talked
// into trying.
func TestResolve(t *testing.T) {
	rejected := []string{
		"",                                    // no path at all
		"/etc/passwd",                         // absolute
		"../../../../etc/passwd",              // traversal
		"tools/../../outside.txt",             // traversal via a real subdirectory
		"secrets/openai-api-key",              // denied directory
		"./secrets/../secrets/openai-api-key", // denied directory, obscured
		".git/config",                         // denied directory
	}
	for _, path := range rejected {
		if got, err := resolve(path); err == nil {
			t.Errorf("resolve(%q) = %q, want error", path, got)
		}
	}

	accepted := []string{"main.go", "tools/file.go", "./README.md", "a/b/c.txt"}
	for _, path := range accepted {
		if _, err := resolve(path); err != nil {
			t.Errorf("resolve(%q) = error %v, want success", path, err)
		}
	}
}
