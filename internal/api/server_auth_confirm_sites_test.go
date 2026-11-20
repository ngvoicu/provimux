package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// confirmationSites lists every place allowed to confirm a credential statement: file, enclosing function, number
// of calls. A route of the execution layer confirms the offer immediately before the first byte of its answer
// (handlers.ConfirmAnswer); a relay confirms the credential it holds immediately before writing its upstream's
// response (logging.ConfirmGinCPAAuthStatement); ConfirmAnswer itself confirms the offer
// (logging.ConfirmGinCPAAuthOffer). A new or a missing site fails this test until the list is changed on purpose.
var confirmationSites = map[string]int{
	// handlers.ConfirmAnswer: the routes of the execution layer.
	"sdk/api/handlers/claude/code_handlers.go ClaudeCountTokens ConfirmAnswer":                          1,
	"sdk/api/handlers/claude/code_handlers.go handleNonStreamingResponse ConfirmAnswer":                 1,
	"sdk/api/handlers/claude/code_handlers.go handleStreamingResponse ConfirmAnswer":                    1,
	"sdk/api/handlers/openai/openai_handlers.go handleNonStreamingResponse ConfirmAnswer":               1,
	"sdk/api/handlers/openai/openai_handlers.go handleStreamingResponse ConfirmAnswer":                  1,
	"sdk/api/handlers/openai/openai_handlers.go handleCompletionsNonStreamingResponse ConfirmAnswer":    1,
	"sdk/api/handlers/openai/openai_handlers.go handleCompletionsStreamingResponse ConfirmAnswer":       2,
	"sdk/api/handlers/openai/openai_handlers.go handleStreamResult ConfirmAnswer":                       1,
	"sdk/api/handlers/openai/openai_responses_handlers.go Compact ConfirmAnswer":                        1,
	"sdk/api/handlers/openai/openai_responses_handlers.go handleNonStreamingResponse ConfirmAnswer":     1,
	"sdk/api/handlers/openai/openai_responses_handlers.go handleStreamingResponse ConfirmAnswer":        3,
	"sdk/api/handlers/gemini/gemini_handlers.go handleGenerateContent ConfirmAnswer":                    1,
	"sdk/api/handlers/gemini/gemini_handlers.go handleStreamGenerateContent ConfirmAnswer":              1,
	"sdk/api/handlers/gemini/gemini_handlers.go handleCountTokens ConfirmAnswer":                        1,
	"sdk/api/handlers/gemini/interactions_handlers.go handleInteractionsNonStream ConfirmAnswer":        1,
	"sdk/api/handlers/gemini/interactions_handlers.go forwardInteractionsStream ConfirmAnswer":          1,
	"sdk/api/handlers/openai/openai_images_handlers.go collectRoutedImages ConfirmAnswer":               1,
	"sdk/api/handlers/openai/openai_images_handlers.go streamRoutedImages ConfirmAnswer":                1,
	"sdk/api/handlers/openai/openai_images_handlers.go streamOpenAICompatImages ConfirmAnswer":          1,
	"sdk/api/handlers/openai/openai_images_handlers.go collectImagesWithModelAndOriginal ConfirmAnswer": 1,
	"sdk/api/handlers/openai/openai_images_handlers.go streamImagesWithModel ConfirmAnswer":             1,
	"sdk/api/handlers/openai/openai_images_handlers.go collectImagesFromResponses ConfirmAnswer":        1,
	"sdk/api/handlers/openai/openai_images_handlers.go streamImagesFromResponses ConfirmAnswer":         1,
	"sdk/api/handlers/openai/openai_videos_handlers.go collectXAIVideosNative ConfirmAnswer":            1,
	"sdk/api/handlers/openai/openai_videos_handlers.go collectXAIVideosCreate ConfirmAnswer":            1,
	"sdk/api/handlers/openai/openai_videos_handlers.go VideosRetrieve ConfirmAnswer":                    1,
	"sdk/api/handlers/openai/openai_videos_handlers.go writeVideoContentFromURL ConfirmAnswer":          1,
	"sdk/api/handlers/openai/openai_speech_handlers.go handleXAISpeech ConfirmAnswer":                   1,
	"sdk/api/handlers/handlers_answered.go ConfirmAnswer ConfirmGinCPAAuthOffer":                        1,
	"internal/client/codex/live/live.go Handle ConfirmGinCPAAuthStatement":                              1,
	"internal/client/codex/live/capabilities.go HandleHangup ConfirmGinCPAAuthStatement":                1,
	"internal/client/codex/live/websocket.go HandleDirectWebsocket ConfirmGinCPAAuthStatement":          1,
	"internal/client/codex/live/sideband.go handleSidebandDialError ConfirmGinCPAAuthStatement":         1,
	"internal/api/server_routes.go codexAlphaSearch ConfirmGinCPAAuthStatement":                         1,
	"internal/client/musecode/relay.go writeRelayAttemptResponse ConfirmGinCPAAuthStatement":            1,
}

const modulePath = "github.com/router-for-me/CLIProxyAPI/v8"

// confirmFunctions are the functions that put a statement on a response, by the package that defines them.
var confirmFunctions = map[string][]string{
	modulePath + "/sdk/api/handlers": {"ConfirmAnswer"},
	modulePath + "/internal/logging": {"ConfirmGinCPAAuthOffer", "ConfirmGinCPAAuthStatement"},
}

func TestConfirmationSitesAreTheListedOnes(t *testing.T) {
	root, errRoot := filepath.Abs(filepath.Join("..", ".."))
	if errRoot != nil {
		t.Fatal(errRoot)
	}
	found := map[string]int{}
	fset := token.NewFileSet()
	errWalk := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, errParse := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if errParse != nil {
			return errParse
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// The names a call can use for each confirming function in this file: through an import, or unqualified
		// inside the package that defines it.
		names := map[string]map[string]bool{}
		for _, spec := range file.Imports {
			importPath, _ := strconv.Unquote(spec.Path.Value)
			functions, ok := confirmFunctions[importPath]
			if !ok {
				continue
			}
			local := importPath[strings.LastIndex(importPath, "/")+1:]
			if spec.Name != nil {
				local = spec.Name.Name
			}
			names[local] = map[string]bool{}
			for _, function := range functions {
				names[local][function] = true
			}
		}
		own := map[string]bool{}
		if functions, ok := confirmFunctions[modulePath+"/"+filepath.ToSlash(filepath.Dir(rel))]; ok {
			for _, function := range functions {
				own[function] = true
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch callee := call.Fun.(type) {
				case *ast.SelectorExpr:
					if pkg, ok := callee.X.(*ast.Ident); ok && names[pkg.Name][callee.Sel.Name] {
						found[rel+" "+fn.Name.Name+" "+callee.Sel.Name]++
					}
				case *ast.Ident:
					if own[callee.Name] {
						found[rel+" "+fn.Name.Name+" "+callee.Name]++
					}
				}
				return true
			})
		}
		return nil
	})
	if errWalk != nil {
		t.Fatal(errWalk)
	}

	var problems []string
	total := 0
	for site, want := range confirmationSites {
		total += want
		if got := found[site]; got != want {
			problems = append(problems, site+": "+strconv.Itoa(got)+" calls, listed "+strconv.Itoa(want))
		}
	}
	for site, got := range found {
		if _, listed := confirmationSites[site]; !listed {
			problems = append(problems, site+": "+strconv.Itoa(got)+" calls, not listed")
		}
	}
	sort.Strings(problems)
	for _, problem := range problems {
		t.Error(problem)
	}
	if total != 38 {
		t.Errorf("the list holds %d calls; section 3 gives 31 route lines, 6 relays and the one inside ConfirmAnswer", total)
	}
}
