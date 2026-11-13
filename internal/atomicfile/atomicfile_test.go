package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesWithoutLeavingTemporaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for _, content := range []string{"old", "new"} {
		if err := Write(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test file inside t.TempDir.
	if err != nil || string(data) != "new" {
		t.Fatalf("%q %v", data, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporaries left: %v", entries)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if err := Write(filepath.Join(dir, "missing", "x"), nil); err == nil {
		t.Fatal("missing directory accepted")
	}
}
