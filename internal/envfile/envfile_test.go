package envfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadParsesKeysAndIgnoresComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\nAPI_KEY=abc123\nOPENAI_BASE_URL=\"https://example.com\"\n\nBAD LINE\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	vars, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if vars["API_KEY"] != "abc123" {
		t.Fatalf("API_KEY=%q", vars["API_KEY"])
	}
	if vars["OPENAI_BASE_URL"] != "https://example.com" {
		t.Fatalf("OPENAI_BASE_URL=%q", vars["OPENAI_BASE_URL"])
	}
	if len(vars) != 2 {
		t.Fatalf("expected 2 vars, got %d: %v", len(vars), vars)
	}
}

func TestFindWalksUpward(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".env")
	if err := os.WriteFile(path, []byte("K=v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Find(nested, ".env"); got != path {
		t.Fatalf("got %q want %q", got, path)
	}
	if got := Find(t.TempDir(), ".env"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
