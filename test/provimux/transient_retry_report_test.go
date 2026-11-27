package provimux

import (
	"testing"
)

// scriptReport puts the pool in the state of the owner's report: the login answers one "Overloaded"
// (status 502, Anthropic's overloaded_error body, as his log shows it), the Devin login's weekly quota is
// spent, both Claude keys have no credit, OpenRouter answers.
func scriptReport(p *pool) {
	p.vendors["login"].setScript(fault{status: 502, body: bodyOverloaded})
	p.vendors["devin"].setScript(repeat(fault{status: 429, body: bodyWeeklyQuota}, 20)...)
	p.vendors["key1"].setScript(repeat(fault{status: 400, body: bodyCreditTooLow}, 20)...)
	p.vendors["key2"].setScript(repeat(fault{status: 400, body: bodyCreditTooLow}, 20)...)
}

func repeat(f fault, n int) []fault {
	out := make([]fault, n)
	for i := range out {
		out[i] = f
	}
	return out
}

// Today (main, and the rule off): one passing "Overloaded" of the login sends the conversation down the
// whole list, to OpenRouter, and it stays there after the login answers again.
func TestTransientRetryReportToday(t *testing.T) {
	for _, stream := range []bool{true, false} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			p := newPool(t, poolOptions{affinity: true, rebindOn: "any"})
			if code, body := p.send("conversation", stream); code != 200 || answeredBy(body) != "login" {
				t.Fatalf("first turn: %d answered by %q, want 200 from the login", code, answeredBy(body))
			}
			scriptReport(p)
			before := p.calls()
			code, body := p.send("conversation", stream)
			after := p.calls()
			if code != 200 || answeredBy(body) != "openrouter" {
				t.Fatalf("second turn: %d answered by %q, want 200 from openrouter (today's behaviour); calls %v -> %v; body %s", code, answeredBy(body), before, after, body)
			}
			for _, name := range p.order {
				if got := after[name] - before[name]; got != 1 {
					t.Errorf("%s was called %d times in the second turn, want 1", name, got)
				}
			}
			if cd := p.cooldown("login"); cd <= 0 {
				t.Errorf("login cooldown after the Overloaded = %v, want upstream's transient cooldown", cd)
			}
			// The login answers again, and the next requests of the conversation stay on OpenRouter.
			for i := 0; i < 3; i++ {
				if code, body := p.send("conversation", stream); code != 200 || answeredBy(body) != "openrouter" {
					t.Fatalf("turn %d after recovery: %d answered by %q, want openrouter (the binding moved)", i+3, code, answeredBy(body))
				}
			}
			if code, body := p.send("another-conversation", stream); code == 200 && answeredBy(body) == "login" {
				t.Fatalf("a new conversation went to the login while it cools down")
			}
		})
	}
}
