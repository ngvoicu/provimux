package savings

import "strings"

// Step 4 of 02-prices.md section 4: from a recorded model id to the OpenRouter ids it may be listed
// under. Plain string scans; the repository forbids the standard regexp.

// makers maps the start of a model name to its maker's OpenRouter prefix, in the order tried.
var makers = []struct{ prefix, maker string }{
	{"claude-", "anthropic"},
	{"gpt-", "openai"},
	{"o1", "openai"},
	{"o3", "openai"},
	{"o4", "openai"},
	{"codex-", "openai"},
	{"chatgpt-", "openai"},
	{"gemini-", "google"},
	{"gemma-", "google"},
	{"grok-", "x-ai"},
	{"kimi-", "moonshotai"},
	{"glm-", "z-ai"},
	{"deepseek-", "deepseek"},
	{"qwen", "qwen"},
	{"muse-", "meta"},
}

// effortSuffixes are the only suffixes removed. -fast, -highspeed, -preview, -latest and -1m name
// other products or other prices and stay.
var effortSuffixes = []string{"-thinking", "-xhigh", "-high", "-medium", "-low", "-minimal", "-none"}

// normalizedCandidates lists, in order, the ids step 4 tries for model; none when no maker is known.
func normalizedCandidates(model string) []string {
	n := model
	if i := strings.LastIndexByte(n, ':'); i >= 0 {
		n = n[:i]
	}
	if i := strings.LastIndexByte(n, '/'); i >= 0 {
		n = n[i+1:]
	}
	maker := makerOf(n)
	if maker == "" {
		return nil
	}
	// A dated snapshot is a product of its own, often at another price (gpt-4o-2024-05-13 is not
	// gpt-4o): the maker-qualified id with its date is tried first, the forms without it after.
	var names []string
	if dateless := withoutDate(n); dateless != n {
		names = append(names, n)
		n = dateless
	}
	names = append(names, n)
	for current, removed := n, 0; removed < 2; removed++ { // effort suffixes of the form without a date
		next, ok := withoutEffort(current)
		if !ok {
			break
		}
		names = append(names, next)
		current = next
	}
	out := make([]string, 0, 2*len(names))
	for _, name := range names {
		out = append(out, maker+"/"+name)
		if d := dotted(name); d != name {
			out = append(out, maker+"/"+d)
		}
	}
	return out
}

func makerOf(n string) string {
	for _, m := range makers {
		if strings.HasPrefix(n, m.prefix) {
			return m.maker
		}
	}
	return ""
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

// withoutDate removes a date at the end: "-" and 8 digits, or -YYYY-MM-DD.
func withoutDate(n string) string {
	if len(n) > 9 && n[len(n)-9] == '-' && allDigits(n[len(n)-8:]) {
		return n[:len(n)-9]
	}
	if len(n) > 11 {
		tail := n[len(n)-11:]
		if tail[0] == '-' && allDigits(tail[1:5]) && tail[5] == '-' && allDigits(tail[6:8]) && tail[8] == '-' && allDigits(tail[9:11]) {
			return n[:len(n)-11]
		}
	}
	return n
}

func withoutEffort(n string) (string, bool) {
	for _, suffix := range effortSuffixes {
		if len(n) > len(suffix) && strings.HasSuffix(n, suffix) {
			return n[:len(n)-len(suffix)], true
		}
	}
	return n, false
}

// dotted turns a "-" into a "." where a digit stands before it and one or two digits follow that end
// the name or are followed by "-": claude-opus-4-5 becomes claude-opus-4.5, claude-3-7-sonnet
// claude-3.7-sonnet; grok-4.20-0309-reasoning is unchanged.
func dotted(n string) string {
	b := []byte(n)
	for i := 1; i < len(b)-1; i++ {
		if b[i] != '-' || !isDigit(b[i-1]) {
			continue
		}
		j := i + 1
		for j < len(b) && isDigit(b[j]) {
			j++
		}
		digits := j - (i + 1)
		if digits >= 1 && digits <= 2 && (j == len(b) || b[j] == '-') {
			b[i] = '.'
		}
	}
	return string(b)
}
