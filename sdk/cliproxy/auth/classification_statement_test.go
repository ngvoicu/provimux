package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

// The host of an API key's base URL is given in ASCII, lower case, an internationalised host in its IDNA form; a key
// whose base URL has no host that can be given (no scheme, a URL that does not parse, a host IDNA rejects) is not
// stated at all, never as a key without a base URL.
func TestAuthStatementHostIsASCIIOrTheKeyIsNotStated(t *testing.T) {
	key := func(baseURL string) *Auth {
		return &Auth{Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "made-up-key", "base_url": baseURL}}
	}
	for _, test := range []struct {
		name      string
		auth      *Auth
		host      string
		statement logging.AuthStatement
	}{
		{name: "internationalised host", auth: key("https://bücher.example/v1"), host: "xn--bcher-kva.example", statement: logging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "xn--bcher-kva.example"}},
		{name: "internationalised host in upper case, with a port", auth: key("https://BÜCHER.example:8443/v1"), host: "xn--bcher-kva.example", statement: logging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "xn--bcher-kva.example"}},
		{name: "internationalised label among ASCII labels", auth: key("https://api.例え.jp/v1"), host: "api.xn--r8jz45g.jp", statement: logging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "api.xn--r8jz45g.jp"}},
		{name: "ASCII host IDNA would reject", auth: key("https://my_gateway.internal/v1"), host: "my_gateway.internal", statement: logging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "my_gateway.internal"}},
		{name: "IPv6 literal", auth: key("https://[::1]:8080/v1"), host: "::1", statement: logging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "::1"}},
		{name: "credentials, port, path and query dropped", auth: key("https://user:pass@API.Example.test:8443/acct-1/v1?token=t"), host: "api.example.test", statement: logging.AuthStatement{Kind: "apikey", Provider: "claude", Host: "api.example.test"}},
		{name: "base URL without a scheme", auth: key("localhost:8080/v1")},
		{name: "base URL that does not parse", auth: key("https://ex ample.com/v1")},
		{name: "host IDNA rejects", auth: key("https://bad\uFFFD.example/v1")},
		{name: "key without a base URL", auth: key(""), statement: logging.AuthStatement{Kind: "apikey", Provider: "claude"}},
		{name: "login with a base URL", auth: &Auth{Provider: "devin", Attributes: map[string]string{AttributeAuthKind: "oauth", "base_url": "https://login.example.test/acct"}}, statement: logging.AuthStatement{Kind: "oauth", Provider: "devin"}},
		{name: "no credential"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.auth.BaseURLHost(); got != test.host {
				t.Fatalf("BaseURLHost() = %q, want %q", got, test.host)
			}
			if got := test.auth.AuthStatement(); got != test.statement {
				t.Fatalf("AuthStatement() = %+q, want %+q", got, test.statement)
			}
			if got := test.auth.AuthStatement().OnTheWire(); got != test.statement {
				t.Fatalf("AuthStatement().OnTheWire() = %+q, want %+q", got, test.statement)
			}
		})
	}
}
