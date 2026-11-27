package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"google.golang.org/protobuf/encoding/protowire"
)

// Provimux: what an attempt sent (.github/provimux/design/answered-by.md 4.2 and 4.3, step W6a, and the lead's
// amendment of 2026-11-27: the entry only when it is certain). Every HTTP client an executor sends a model request with
// is built by a helper that applies upstream's observation seam (sdk/httptransport) after the proxy, the pool and the
// TLS profile are chosen. Provimux puts one decorator on that seam, in one place: the context an attempt gets its
// tracker on (WithUpstreamAttemptTracker), together with a log of the attempt's own and a connection trace. The
// decorator notes each request a client built for the attempt hands its transport: the scheme, host and port of the
// URL, the model read from the request as its bytes are, the names of its credential headers, whether the request is
// one of the attempt's known housekeeping requests, whether anything makes what was read differ from what leaves (a
// Host other than the URL's, a transport below that is not one of the gateway's own, a request on another context
// than the attempt's) and, when the response's headers arrive, the status. The trace counts every connection the
// attempt's context asks for, so one the decorator did not see (an upstream WebSocket, a client no helper built) is
// known. It delegates and changes no byte: the body is read through GetBody, never consumed. With models.answered-by
// off nothing is attached.

// ProvimuxSentRequest is one request an attempt handed to its transport, as Provimux's decorator saw it.
type ProvimuxSentRequest struct {
	Scheme string // of the request's URL, lower case
	Host   string // its host, lower case, without the port
	Port   string // its port; "" = the scheme's default
	Model  string // the model id read from the request (answered-by.md 4.2); "" = none was read
	Signer string // which credential headers the request carries, by name only: "x-api-key", "authorization", "both", ""
	Status int    // the status of its response; 0 = no response arrived

	// unread: the request has a body, or a model in its URL, in a form the reader does not know, or two models that
	// differ.
	unread bool
	// untrusted: what was read may not be what left, to the host it names: a Host other than the URL's, a transport
	// below the reader that is not one of the gateway's own, or a request on a context other than the attempt's.
	untrusted bool
	// housekeeping: a request of the closed list of requests that cannot produce an answer (provimuxHousekeeping).
	housekeeping bool
}

// provimuxSentKept is how many requests an attempt's log keeps. An attempt that makes more names no model.
const provimuxSentKept = 16

// provimuxSentBodyLimit is the largest body the reader reads. A larger one is not read.
const provimuxSentBodyLimit = 64 << 20

// provimuxSentLog is one attempt's observations.
type provimuxSentLog struct {
	parent *provimuxSentLog // the log of the attempt this one runs inside, if any

	mu       sync.Mutex
	requests []ProvimuxSentRequest
	overflow bool

	conns    atomic.Int64 // connections the attempt's context asked for (the trace)
	observed atomic.Int64 // of those, the ones asked for by a request the decorator saw
}

func (l *provimuxSentLog) add(request ProvimuxSentRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.requests) >= provimuxSentKept {
		l.overflow = true
		return
	}
	l.requests = append(l.requests, request)
}

type provimuxSentLogKey struct{}

func provimuxSentLogOf(ctx context.Context) *provimuxSentLog {
	if ctx == nil {
		return nil
	}
	log, _ := ctx.Value(provimuxSentLogKey{}).(*provimuxSentLog)
	return log
}

// ProvimuxSent returns what the attempt of ctx sent when the answer's entry can be certain (the lead's rule of
// 2026-11-27): among its requests answered with a 2xx and not on the closed housekeeping list there is exactly one,
// the reader read it whole and nothing makes what was read differ from what left; and no connection was made in the
// attempt that the decorator did not see (an upstream WebSocket among them). ok is false in every other case.
func ProvimuxSent(ctx context.Context) (sent ProvimuxSentRequest, ok bool) {
	log := provimuxSentLogOf(ctx)
	if log == nil || log.conns.Load() > log.observed.Load() {
		return ProvimuxSentRequest{}, false
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.overflow {
		return ProvimuxSentRequest{}, false
	}
	candidates := 0
	for _, request := range log.requests {
		if request.Status < 200 || request.Status > 299 || request.housekeeping {
			continue
		}
		candidates++
		sent = request
	}
	if candidates != 1 || sent.unread || sent.untrusted || sent.Model == "" {
		return ProvimuxSentRequest{}, false
	}
	return sent, true
}

// provimuxObserveSent returns ctx with the attempt's log, its connection trace and Provimux's decorator on it, around
// any decorator ctx already carries. Only WithUpstreamAttemptTracker calls it. With models.answered-by off it adds
// nothing.
func provimuxObserveSent(ctx context.Context) context.Context {
	if ctx == nil || !logging.ProvimuxAnsweredBy() {
		return ctx
	}
	log := &provimuxSentLog{parent: provimuxSentLogOf(ctx)}
	outer := ctx
	ctx = context.WithValue(ctx, provimuxSentLogKey{}, log)
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GetConn: func(string) { log.conns.Add(1) }})
	return httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
		// The decorator ctx carried before ours (an embedder's), applied first, inside ours. A decorator of an
		// enclosing attempt is replaced by this one: a client built for this attempt notes on this attempt's log.
		inner := next
		if decorated := httptransport.Decorate(outer, &http.Client{Transport: next}); decorated != nil && decorated.Transport != nil {
			inner = decorated.Transport
		}
		if enclosing, ours := inner.(*provimuxSentObserver); ours {
			inner = enclosing.next
		}
		return &provimuxSentObserver{next: inner, log: log}
	})
}

// provimuxSentObserver is the decorator's transport: it notes each request of the client built for log's attempt,
// and delegates.
type provimuxSentObserver struct {
	next http.RoundTripper
	log  *provimuxSentLog
}

func (o *provimuxSentObserver) RoundTrip(req *http.Request) (*http.Response, error) {
	own := provimuxSentLogOf(req.Context())
	observed := provimuxObserveRequest(req)
	if !provimuxStockTransport(o.next) || !provimuxStockTransport(req.Context().Value("cliproxy.roundtripper")) {
		observed.untrusted = true
	}
	if own != o.log {
		// A request on another context than the attempt's (a detached one): it is not known to be the attempt's
		// own, and no entry is named for either.
		observed.untrusted = true
	}
	if own != nil {
		// The connection this request asks for is a seen one, for its attempt and every attempt it runs inside.
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GetConn: func(string) {
			for log := own; log != nil; log = log.parent {
				log.observed.Add(1)
			}
		}}))
	}
	resp, err := o.next.RoundTrip(req)
	if err == nil && resp != nil {
		observed.Status = resp.StatusCode
	}
	o.log.add(observed)
	if own != nil && own != o.log {
		own.add(observed)
	}
	return resp, err
}

// CloseIdleConnections passes the client's call on, as the transport below would receive it without the decorator.
func (o *provimuxSentObserver) CloseIdleConnections() {
	if closer, ok := o.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// ProvimuxStockTransport is implemented by a transport that hands every request on as it receives it, to the host its
// URL names: a stand-in of a test, or a transport an embedder vouches for. A transport below the reader that is
// neither one of the gateway's own nor this gives no entry.
type ProvimuxStockTransport interface {
	ProvimuxPassesRequestsUnchanged() bool
}

// provimuxStockTransport reports whether rt (nil included) is one of the gateway's own transports: Go's, or one the
// gateway's own packages build, or one that vouches for itself.
func provimuxStockTransport(rt any) bool {
	if rt == nil {
		return true
	}
	if stock, ok := rt.(ProvimuxStockTransport); ok {
		return stock.ProvimuxPassesRequestsUnchanged()
	}
	t := reflect.TypeOf(rt)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	path := t.PkgPath()
	switch {
	case path == "net/http", path == "golang.org/x/net/http2":
		return true
	case strings.HasPrefix(path, "github.com/router-for-me/CLIProxyAPI/v8/internal/"),
		path == "github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil":
		return true
	}
	return false
}

// provimuxObserveRequest notes what req sends, without the status.
func provimuxObserveRequest(req *http.Request) ProvimuxSentRequest {
	var observed ProvimuxSentRequest
	if req == nil || req.URL == nil {
		observed.unread = true
		return observed
	}
	observed.Scheme = strings.ToLower(req.URL.Scheme)
	observed.Host = strings.ToLower(req.URL.Hostname())
	observed.Port = req.URL.Port()
	if (observed.Scheme == "https" && observed.Port == "443") || (observed.Scheme == "http" && observed.Port == "80") {
		observed.Port = ""
	}
	observed.Signer = provimuxSigner(req.Header)
	observed.Model, observed.unread = provimuxReadModel(req)
	if host := strings.TrimSpace(req.Host); host != "" && !provimuxSameAuthority(host, observed) {
		observed.untrusted = true
	}
	observed.housekeeping = provimuxHousekeeping(req, observed)
	return observed
}

// provimuxSameAuthority reports whether the Host a request is sent with is its URL's host and port.
func provimuxSameAuthority(host string, observed ProvimuxSentRequest) bool {
	name, port := host, ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		name, port = h, p
	}
	if (observed.Scheme == "https" && port == "443") || (observed.Scheme == "http" && port == "80") {
		port = ""
	}
	return strings.EqualFold(strings.TrimSuffix(name, "."), observed.Host) && port == observed.Port
}

// provimuxSigner names the credential headers of a request: x-api-key, Authorization, both or neither. The names are
// compared without regard to case, as a vendor reads them.
func provimuxSigner(header http.Header) string {
	var apiKey, authorization bool
	for name, values := range header {
		if len(values) == 0 {
			continue
		}
		switch {
		case strings.EqualFold(name, "x-api-key"):
			apiKey = true
		case strings.EqualFold(name, "authorization"):
			authorization = true
		}
	}
	switch {
	case apiKey && authorization:
		return "both"
	case apiKey:
		return "x-api-key"
	case authorization:
		return "authorization"
	}
	return ""
}

// provimuxSentModel reads the model id of an outgoing request without consuming its body (GetBody): the top-level
// "model" of a JSON body (or of a form), zstd decoded where Content-Encoding says so; the <id> of a URL path
// .../models/<id>:<method>; field 21 of a Connect message to Devin's chat method. "" where the request has none, has
// two that differ, or is in a form it does not know.
func provimuxSentModel(req *http.Request) string {
	model, unread := provimuxReadModel(req)
	if unread {
		return ""
	}
	return model
}

// provimuxReadModel reads the model of req; unread is true where the request carries a body or a model it cannot read
// as one of the known forms, or two models that differ.
func provimuxReadModel(req *http.Request) (model string, unread bool) {
	pathModel, pathUnread := provimuxPathModel(req.URL)
	if pathUnread {
		return "", true
	}
	bodyModel, bodyUnread := provimuxBodyModel(req)
	if bodyUnread {
		return "", true
	}
	switch {
	case pathModel != "" && bodyModel != "" && pathModel != bodyModel:
		return "", true
	case pathModel != "":
		return pathModel, false
	}
	return bodyModel, false
}

// provimuxPathModel reads the <id> of a path .../models/<id>:<method> (Gemini, Vertex's native route). A path whose
// models are a publisher's other than Google's (.../publishers/<p>/models/<id>:<method>) is not read: the id alone
// would not name the model.
func provimuxPathModel(u *url.URL) (model string, unread bool) {
	if u == nil {
		return "", false
	}
	path := u.Path
	i := strings.LastIndex(path, "/models/")
	if i < 0 {
		return "", false
	}
	rest := path[i+len("/models/"):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return "", false
	}
	id, method := rest[:colon], rest[colon+1:]
	if method == "" || strings.Contains(method, "/") {
		return "", false
	}
	if id == "" || strings.Contains(id, "/") || !utf8.ValidString(id) {
		return "", true
	}
	if before := path[:i]; strings.Contains(before, "/publishers/") {
		publisher := before[strings.LastIndex(before, "/publishers/")+len("/publishers/"):]
		if publisher != "google" {
			return "", true
		}
	}
	return id, false
}

// provimuxBodyModel reads the model of req's body.
func provimuxBodyModel(req *http.Request) (model string, unread bool) {
	body, readable := provimuxRequestBody(req)
	if !readable {
		return "", true
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return "", false
	}
	mediaType, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	mediaType = strings.ToLower(mediaType)
	switch {
	case mediaType == "application/connect+proto":
		return provimuxConnectModel(req.URL, body)
	case mediaType == "application/x-www-form-urlencoded":
		return provimuxFormModel(body)
	case strings.Contains(mediaType, "json") || (mediaType == "" && bytes.HasPrefix(bytes.TrimSpace(body), []byte("{"))):
		return provimuxJSONModel(body)
	}
	return "", true
}

// provimuxRequestBody returns the bytes req sends, through GetBody, decoded where its Content-Encoding is zstd.
// readable is false where they cannot be had without consuming the body, are too large, or are in another encoding.
func provimuxRequestBody(req *http.Request) (body []byte, readable bool) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	reader, errBody := req.GetBody()
	if errBody != nil || reader == nil {
		return nil, false
	}
	defer func() { _ = reader.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(reader, provimuxSentBodyLimit+1))
	if errRead != nil || len(body) > provimuxSentBodyLimit {
		return nil, false
	}
	var encodings []string
	for _, value := range req.Header.Values("Content-Encoding") {
		for _, encoding := range strings.Split(value, ",") {
			if encoding = strings.ToLower(strings.TrimSpace(encoding)); encoding != "" && encoding != "identity" {
				encodings = append(encodings, encoding)
			}
		}
	}
	switch {
	case len(encodings) == 0:
		return body, true
	case len(encodings) == 1 && encodings[0] == "zstd":
		decoder, errDecoder := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(provimuxSentBodyLimit))
		if errDecoder != nil {
			return nil, false
		}
		defer decoder.Close()
		decoded, errDecode := decoder.DecodeAll(body, nil)
		if errDecode != nil {
			return nil, false
		}
		return decoded, true
	}
	return nil, false
}

// provimuxJSONModel reads the top-level "model" of a JSON object: a string, once. A body that is not an object, a
// "model" that is not a non-empty string, and two "model" keys are not read.
func provimuxJSONModel(body []byte) (model string, unread bool) {
	if !ojson.ValidBytes(body) {
		return "", true
	}
	root := ojson.ParseBytes(body)
	if !root.IsObject() {
		return "", true
	}
	seen := false
	root.ForEach(func(key, value ojson.Result) bool {
		if key.String() != "model" {
			return true
		}
		if seen || value.Type != ojson.String || value.String() == "" {
			unread = true
			return false
		}
		seen, model = true, value.String()
		return true
	})
	if unread {
		return "", true
	}
	return model, false
}

// provimuxFormModel reads the "model" of a form: once, or the same each time.
func provimuxFormModel(body []byte) (model string, unread bool) {
	values, errParse := url.ParseQuery(string(body))
	if errParse != nil {
		return "", true
	}
	models := values["model"]
	if len(models) == 0 {
		return "", false
	}
	for _, value := range models {
		if value == "" || value != models[0] {
			return "", true
		}
	}
	return models[0], false
}

// provimuxDevinChatMethod is the path of Devin's chat method (helps.DevinChatPath), whose message carries the model
// in field 21 (helps.BuildDevinGetChatMessageRequest).
const provimuxDevinChatMethod = "/exa.api_server_pb.ApiServerService/GetChatMessage"

// provimuxConnectModel reads field 21 of the one uncompressed Connect frame of a request to Devin's chat method. Any
// other method carries no model; a chat request in another shape is not read.
func provimuxConnectModel(u *url.URL, body []byte) (model string, unread bool) {
	if u == nil || !strings.HasSuffix(u.Path, provimuxDevinChatMethod) {
		return "", false
	}
	if len(body) < 5 || body[0] != 0 || int(binary.BigEndian.Uint32(body[1:5])) != len(body)-5 {
		return "", true
	}
	message := body[5:]
	seen := false
	for len(message) > 0 {
		number, kind, n := protowire.ConsumeTag(message)
		if n < 0 {
			return "", true
		}
		message = message[n:]
		if number == 21 {
			if kind != protowire.BytesType {
				return "", true
			}
			value, m := protowire.ConsumeBytes(message)
			if m < 0 || len(value) == 0 || !utf8.Valid(value) || (seen && string(value) != model) {
				return "", true
			}
			seen, model = true, string(value)
			message = message[m:]
			continue
		}
		m := protowire.ConsumeFieldValue(number, kind, message)
		if m < 0 {
			return "", true
		}
		message = message[m:]
	}
	if !seen {
		return "", true
	}
	return model, false
}

// provimuxHousekeeping reports whether req is on the closed list of requests that cannot produce an answer, known by
// destination and shape (the lead's rule of 2026-11-27): a token exchange or refresh, a model catalog, a project
// discovery. Every other request counts as one that could have produced the answer. The list is closed: a request
// upstream adds is a candidate until it is named here.
func provimuxHousekeeping(req *http.Request, observed ProvimuxSentRequest) bool {
	if observed.Model != "" || observed.unread || observed.Scheme != "https" || observed.Port != "" {
		return false
	}
	method, host, path := req.Method, observed.Host, req.URL.Path
	mediaType, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	form := method == http.MethodPost && strings.EqualFold(mediaType, "application/x-www-form-urlencoded")
	for _, known := range provimuxHousekeepingList {
		if known.method != method || (known.form && !form) {
			continue
		}
		if known.host != host && !(known.hostSuffix != "" && strings.HasSuffix(host, known.hostSuffix)) {
			continue
		}
		if known.path == path || (known.pathSuffix != "" && strings.HasSuffix(path, known.pathSuffix)) {
			return true
		}
	}
	return false
}

// provimuxHousekeepingRequest is one entry of the closed list: a method, a host (or a host suffix), a path (or a path
// suffix) and, for a token endpoint, a form body.
type provimuxHousekeepingRequest struct {
	executor         string // who sends it (documentation)
	method           string
	host, hostSuffix string
	path, pathSuffix string
	form             bool
}

// provimuxHousekeepingList is the closed list, by executor. Claude, Codex, xAI, Meta, Devin, Kimi, Gemini keys and
// OpenAI-compatible keys make no such request on an attempt's clients: their token refreshes and profile reads run on
// clients of their own (a connection one of them makes on the attempt's context is unseen, and cancels), or in an
// attempt of their own.
var provimuxHousekeepingList = []provimuxHousekeepingRequest{
	// Google's OAuth token endpoint: a Vertex service account's token exchange, an Antigravity or Gemini login's
	// refresh. A form POST.
	{executor: "vertex, antigravity, gemini", method: http.MethodPost, host: "oauth2.googleapis.com", path: "/token", form: true},
	{executor: "vertex, antigravity, gemini", method: http.MethodPost, host: "www.googleapis.com", path: "/oauth2/v4/token", form: true},
	// Antigravity: project discovery, onboarding and the user's information, before a first answer of a login. JSON
	// without a model, to its three hosts.
	{executor: "antigravity", method: http.MethodPost, host: "cloudcode-pa.googleapis.com", pathSuffix: "/v1internal:loadCodeAssist"},
	{executor: "antigravity", method: http.MethodPost, host: "cloudcode-pa.googleapis.com", pathSuffix: "/v1internal:onboardUser"},
	{executor: "antigravity", method: http.MethodPost, host: "cloudcode-pa.googleapis.com", pathSuffix: "/v1internal:fetchUserInfo"},
	{executor: "antigravity", method: http.MethodPost, host: "daily-cloudcode-pa.googleapis.com", pathSuffix: "/v1internal:loadCodeAssist"},
	{executor: "antigravity", method: http.MethodPost, host: "daily-cloudcode-pa.googleapis.com", pathSuffix: "/v1internal:onboardUser"},
	{executor: "antigravity", method: http.MethodPost, host: "daily-cloudcode-pa.googleapis.com", pathSuffix: "/v1internal:fetchUserInfo"},
	{executor: "antigravity", method: http.MethodPost, host: "daily-cloudcode-pa.sandbox.googleapis.com", pathSuffix: "/v1internal:loadCodeAssist"},
	{executor: "antigravity", method: http.MethodPost, host: "daily-cloudcode-pa.sandbox.googleapis.com", pathSuffix: "/v1internal:onboardUser"},
	{executor: "antigravity", method: http.MethodPost, host: "daily-cloudcode-pa.sandbox.googleapis.com", pathSuffix: "/v1internal:fetchUserInfo"},
	// Copilot: the exchange of the GitHub token for Copilot's, and Copilot's model catalog.
	{executor: "copilot", method: http.MethodGet, host: "api.github.com", path: "/copilot_internal/v2/token"},
	{executor: "copilot", method: http.MethodGet, host: "api.githubcopilot.com", path: "/models"},
	{executor: "copilot", method: http.MethodGet, hostSuffix: ".githubcopilot.com", path: "/models"},
}
