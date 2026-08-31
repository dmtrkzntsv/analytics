package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Docker build context excludes docs/ wholesale, so every file this
// package embeds needs an explicit exception in .dockerignore. That gap is
// invisible locally — go build sees the working tree, the image build sees
// the context — and it has broken the release image twice. This test binds
// the two files together: each //go:embed pattern must have its exception,
// and each docs/ exception must still point at a file that exists.
func TestDockerignoreCoversEmbeds(t *testing.T) {
	embedSrc, err := os.ReadFile("embed.go")
	if err != nil {
		t.Fatal(err)
	}
	ignore, err := os.ReadFile(filepath.Join("..", ".dockerignore"))
	if err != nil {
		t.Fatal(err)
	}

	exceptions := map[string]bool{}
	for _, line := range strings.Split(string(ignore), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "!docs/"); ok {
			exceptions[rest] = true
		}
	}

	var patterns []string
	for _, line := range strings.Split(string(embedSrc), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "//go:embed "); ok {
			patterns = append(patterns, strings.Fields(rest)...)
		}
	}
	if len(patterns) == 0 {
		t.Fatal("no //go:embed directives found in embed.go")
	}

	for _, p := range patterns {
		if !exceptions[p] {
			t.Errorf("embed.go embeds %q but .dockerignore has no !docs/%s exception; the image build will fail with \"no matching files found\"", p, p)
		}
	}
	for e := range exceptions {
		if e == "embed.go" {
			continue
		}
		if _, err := os.Stat(e); err != nil {
			t.Errorf(".dockerignore excepts !docs/%s but that file does not exist: %v", e, err)
		}
	}
}
