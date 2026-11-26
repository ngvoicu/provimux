package provimux

// A snapshot crosses builds only when the session cache means the same in both
// (sdk/cliproxy/auth/provimux_affinity_snapshot.go, the pin). This test writes this build's pin,
// then builds the auth package again with one edit to upstream's code (go test -overlay: a
// scratch copy of one file, the tree is not touched) and lets that build judge the pin. An edit to
// a rule every entry follows (key syntax, recency, lifetimes, expiry) voids the snapshot; an edit to
// how one kind of conversation is named leaves out that kind's entries; an edit that changes
// neither is shown harmless: the pin passes and the restore tests pass on that build.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvimuxAffinityPinAcrossBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the auth package once per edit")
	}
	goTool, errLook := exec.LookPath("go")
	if errLook != nil {
		t.Skip("no go tool")
	}
	// The overlay names files by the path the go command sees: no symbolic link in it.
	root, errRoot := filepath.Abs(filepath.Join("..", ".."))
	if errRoot == nil {
		root, errRoot = filepath.EvalSymlinks(root)
	}
	if errRoot != nil {
		t.Fatal(errRoot)
	}
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.json")
	run := func(overlay string, env []string, pattern string) (string, error) {
		args := []string{"test", "-count=1", "-run", pattern}
		if overlay != "" {
			args = append(args, "-overlay", overlay)
		}
		args = append(args, "./sdk/cliproxy/auth")
		cmd := exec.Command(goTool, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("", []string{"PROVIMUX_TEST_PIN_WRITE=" + pinPath}, "^TestProvimuxAffinityPinFixture$"); err != nil {
		t.Fatalf("writing the pin: %v\n%s", err, out)
	}

	selector := filepath.Join(root, "sdk", "cliproxy", "auth", "selector.go")
	cache := filepath.Join(root, "sdk", "cliproxy", "auth", "session_cache.go")
	info := filepath.Join(root, "sdk", "cliproxy", "session", "info.go")
	for _, edit := range []struct {
		name, file, from, to string
		want                 string // "whole", "kinds: <at least these kinds>" or "same"
	}{
		{"the explicit session key's syntax (the review's edit)", selector,
			`cacheKey := provider + "::" + primaryID + "::" + modelKey`, `cacheKey := provider + ":" + primaryID + "::" + modelKey`, "whole"},
		{"the fallback (parent, conversation) key's syntax", selector,
			`fallbackKey = provider + "::" + fallbackID + "::" + modelKey`, `fallbackKey = provider + "::fb:" + fallbackID + "::" + modelKey`, "kinds: conv,pck"},
		{"the key a result refreshes", selector,
			`cacheKey := ns + "::" + primaryID + "::" + nsModel`, `cacheKey := ns + ":" + primaryID + "::" + nsModel`, "whole"},
		{"how a Claude Code session is named", info,
			"info.AgentName = \"main\"\n\t\t\tinfo.SessionID = \"claude:\" + sid\n", "info.AgentName = \"main\"\n\t\t\tinfo.SessionID = \"cc:\" + sid\n", "kinds: claude"},
		{"recency: a refreshed group goes to the front of the eviction order", cache,
			`c.evictionElements[primaryKey] = c.evictionOrder.PushBack(primaryKey)`, `c.evictionElements[primaryKey] = c.evictionOrder.PushFront(primaryKey)`, "whole"},
		{"recency: the selection sequence counts by two", cache,
			`c.bindingSequence++`, `c.bindingSequence += 2`, "whole"},
		{"lifetime: a new or reused binding lives two TTLs", cache,
			`c.replaceAliasGroupsLocked(authID, now.Add(c.ttl), aliases, binding, previousGroups...)`, `c.replaceAliasGroupsLocked(authID, now.Add(2*c.ttl), aliases, binding, previousGroups...)`, "whole"},
		{"lifetime: a successful result renews for two TTLs", cache,
			`c.replaceAliasGroupsLocked(expectedAuthID, now.Add(c.ttl), aliases, entry.binding, entry)`, `c.replaceAliasGroupsLocked(expectedAuthID, now.Add(2*c.ttl), aliases, entry.binding, entry)`, "whole"},
		{"expiry: an expired entry is still used", cache,
			`	if !now.Before(entry.expiresAt) {
		c.removeAliasGroupLocked(entry)
		return "", false
	}

	aliases := compactSessionAliases(mergeSessionAliases([]string{sessionID}, entry.aliases...))`, `	if false {
		c.removeAliasGroupLocked(entry)
		return "", false
	}

	aliases := compactSessionAliases(mergeSessionAliases([]string{sessionID}, entry.aliases...))`, "whole"},
		{"the third review's edit: the generic session header folded to lower case", info,
			"if sid := sessionHeaderValue(headers, \"X-Session-ID\"); sid != \"\" {\n", "if sid := sessionHeaderValue(headers, \"X-Session-ID\"); sid != \"\" {\n\t\tsid = strings.ToLower(sid)\n", "kinds: header"},
		{"the third review's edit: an expired entry still used for two seconds", cache,
			`	if !now.Before(entry.expiresAt) {
		c.removeAliasGroupLocked(entry)
		return "", false
	}

	aliases := compactSessionAliases(mergeSessionAliases([]string{sessionID}, entry.aliases...))`, `	if !now.Before(entry.expiresAt.Add(2 * time.Second)) {
		c.removeAliasGroupLocked(entry)
		return "", false
	}

	aliases := compactSessionAliases(mergeSessionAliases([]string{sessionID}, entry.aliases...))`, "whole"},
		{"the third review's edit: a smaller default capacity", cache,
			"defaultMaxSessionEntries = 65536", "defaultMaxSessionEntries = 4", "whole"},
		{"harmless: a hit's own renewal (Pick binds again at once, with one TTL)", cache,
			`c.replaceAliasGroupsLocked(entry.authID, now.Add(c.ttl), aliases, entry.binding, entry)
	return entry.authID, true`, `c.replaceAliasGroupsLocked(entry.authID, now.Add(2*c.ttl), aliases, entry.binding, entry)
	return entry.authID, true`, "same"},
		{"harmless: a log line", selector,
			`"session-affinity: cache hit | session=%s`, `"session-affinity: cache hit (edited) | session=%s`, "same"},
	} {
		t.Run(edit.name, func(t *testing.T) {
			original, err := os.ReadFile(edit.file)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(original), edit.from) != 1 {
				t.Fatalf("the edit does not apply once to %s: upstream changed; update the edit", filepath.Base(edit.file))
			}
			edited := filepath.Join(t.TempDir(), filepath.Base(edit.file))
			if errWrite := os.WriteFile(edited, []byte(strings.Replace(string(original), edit.from, edit.to, 1)), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			overlay := filepath.Join(t.TempDir(), "overlay.json")
			data, _ := json.Marshal(map[string]map[string]string{"Replace": {edit.file: edited}})
			if errWrite := os.WriteFile(overlay, data, 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			resultPath := filepath.Join(t.TempDir(), "result")
			out, errRun := run(overlay, []string{"PROVIMUX_TEST_PIN_READ=" + pinPath, "PROVIMUX_TEST_PIN_RESULT=" + resultPath}, "^TestProvimuxAffinityPinFixture$")
			if errRun != nil {
				t.Fatalf("the edited build: %v\n%s", errRun, out)
			}
			result, _ := os.ReadFile(resultPath)
			t.Logf("edited build says: %s", result)
			got := string(result)
			switch {
			case edit.want == "whole" || edit.want == "same":
				if !strings.HasPrefix(got, edit.want) {
					t.Fatalf("the edited build says %q, want %s", got, edit.want)
				}
			default: // "kinds: a,b": at least those kinds are left out, the rest crosses
				left, ok := strings.CutPrefix(got, "kinds: ")
				for _, kind := range strings.Split(strings.TrimPrefix(edit.want, "kinds: "), ",") {
					if !ok || !strings.Contains(","+left+",", ","+kind+",") {
						t.Fatalf("the edited build says %q, want at least %s", got, edit.want)
					}
				}
			}
			if edit.want != "same" {
				return
			}
			if out, errRestore := run(overlay, nil, "^TestProvimuxAffinityRestore"); errRestore != nil {
				t.Fatalf("the restore tests fail on the edited build: %v\n%s", errRestore, out)
			}
		})
	}
}
