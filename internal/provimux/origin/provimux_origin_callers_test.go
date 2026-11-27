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
		"origin.NoteGatewayTransport(":      {"sdk/cliproxy/provimux_origin.go"},
		"origin.InstallLoadedMark(":         {"sdk/cliproxy/auth/provimux_loaded.go"},
		"origin.MarkLoaded(":                {"internal/store/provimux_loaded.go", "sdk/auth/provimux_loaded.go", "internal/watcher/synthesizer/provimux_loaded.go"},
		"provimuxMarkLoaded(":               {"internal/store/provimux_loaded.go", "sdk/auth/provimux_loaded.go", "internal/watcher/synthesizer/provimux_loaded.go", "internal/store/gitstore.go", "internal/store/objectstore.go", "internal/store/postgresstore.go", "sdk/auth/filestore.go", "internal/watcher/synthesizer/file.go", "internal/watcher/synthesizer/config.go"},
		"provimuxGatewayTransports(":        {"sdk/cliproxy/builder.go", "sdk/cliproxy/provimux_origin.go"},
		"provimuxNoteExecutor(":             {"sdk/cliproxy/service_executors.go", "sdk/cliproxy/provimux_builtin_executors.go"},
		"logging.ProvimuxStockServer(":      {"internal/api/server.go"},
		"StandInForTests(":                  {"internal/provimux/origin/origin.go"},
		"MarkLoadedForTests(":               {"internal/provimux/origin/origin.go"},
		"ProvimuxStockServerForTests(":      {"internal/api/provimux_stock_testseam.go"},
		"coreauth.SetProvimuxAnswerEntry(":  {"sdk/cliproxy/provimux_answer_entry.go"},
		"provimuxWithManager(":              {"sdk/cliproxy/auth/conductor_execution.go", "sdk/cliproxy/auth/provimux_answered.go"},
		"origin.DefaultTransportUnchanged(": {"sdk/cliproxy/executor/provimux_sent.go"},
		"origin.GatewayTransport(":          {"sdk/cliproxy/executor/provimux_sent.go"},
		"provimuxStockOptions(":             {"internal/api/server.go", "internal/api/provimux_stock.go"},
		"origin.MarkShippedGateway(":        {"internal/logging/provimux_shipped.go"},
		"logging.ProvimuxShippedGateway(":   {"cmd/server/main.go"},
		"ShippedGatewayForTests(":           {"internal/provimux/origin/origin.go"},
		"WithProvimuxStatementAsked(":       {"sdk/api/handlers/handlers_execution.go", "sdk/api/handlers/handlers_stream.go", "sdk/cliproxy/executor/provimux_sent.go"},
		"ProvimuxPassesRequestsUnchanged":   {}, // the declaration the earlier build read: in no product file
		"ProvimuxStockTransport":            {}, // the same
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
