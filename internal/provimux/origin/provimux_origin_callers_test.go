package origin

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The positive marks of the lead's amendment of 2026-11-27 (2) are set in a few places of the gateway's own code, and
// the test seams nowhere in product code. This reads every product (non-test) Go file of the module and fails when a
// call is anywhere else. A program outside the module cannot import this package at all (Go's internal rule).
func TestProvimuxOriginCallers(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("the module root: %v", err)
	}
	allowed := map[string][]string{
		"provimuxRegistering(":             {"sdk/cliproxy/auth/conductor_lifecycle.go", "sdk/cliproxy/auth/provimux_answered.go"},
		"provimuxNoteExecutor(":            {}, // the per-call record removed in part (5): in no product file
		"provimuxExecutorSequence(":        {}, // the same
		"coreauth.SetProvimuxAnswerEntry(": {"sdk/cliproxy/provimux_answer_entry.go"},
		"provimuxWithManager(":             {"sdk/cliproxy/auth/conductor_execution.go", "sdk/cliproxy/auth/provimux_answered.go"},
		"origin.MarkShippedGateway(":       {"internal/logging/provimux_shipped.go"},
		"logging.ProvimuxShippedGateway(":  {"cmd/server/main.go"},
		"ShippedGatewayForTests(":          {"internal/provimux/origin/origin.go"},
		"WithProvimuxStatementAsked(":      {"sdk/api/handlers/handlers_execution.go", "sdk/api/handlers/handlers_stream.go", "sdk/cliproxy/executor/provimux_sent.go"},
		"ProvimuxPassesRequestsUnchanged":  {}, // the declaration the earlier build read: in no product file
		"ProvimuxStockTransport":           {}, // the same
	}
	found := map[string][]string{}
	errWalk := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for call := range allowed {
			if strings.Contains(string(raw), call) {
				found[call] = append(found[call], rel)
			}
		}
		return nil
	})
	if errWalk != nil {
		t.Fatal(errWalk)
	}
	for call, files := range found {
		permitted := allowed[call]
		for _, file := range files {
			ok := false
			for _, allowedFile := range permitted {
				ok = ok || file == allowedFile
			}
			if !ok {
				sort.Strings(files)
				t.Errorf("%s appears in %s; only in %v", call, file, permitted)
			}
		}
	}
}
