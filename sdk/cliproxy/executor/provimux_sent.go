package executor

import (
	"context"
	"encoding/binary"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
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

// Provimux: what an attempt sent (.github/provimux/design/answered-by.md, "What was sent"). Every HTTP client a
// built-in executor sends a model request with is built by a helper that applies upstream's observation seam
// (sdk/httptransport) after the proxy, the pool and the TLS profile are chosen. Provimux puts one decorator on that
// seam, in one place: the context an attempt gets its tracker on (WithUpstreamAttemptTracker), together with a log of
// the attempt's own and a connection trace. The decorator notes each request a client built for the attempt hands its
// transport: the scheme, host and port of the URL, the names of its credential headers, whether it is sent with a Host
// other than the URL's or on another context than the attempt's, and, when the response's headers arrive, the status.
// For a 2xx it then reads the model from the request's body as GetBody gives it again, and keeps only the model: no
// copy of a body outlives its RoundTrip. The trace counts every connection the attempt's context asks for, so one the
// decorator did not see (an upstream WebSocket, a client no helper built) is known. It delegates and changes no byte.
// With models.answered-by off nothing is attached.

// ProvimuxSentRequest is one request an attempt handed to its transport, as Provimux's decorator saw it.
type ProvimuxSentRequest struct {
	Scheme string // of the request's URL, lower case
	Host   string // its host, lower case, without the port
	Port   string // its port; "" = the scheme's default
	Model  string // the model id read from the request's body or path (answered-by.md 4.2); "" = none
	Signer string // which credential headers the request carries, by name only: "x-api-key", "authorization", "both", ""
	Status int    // the status of its response; 0 = no response arrived

	// unread: the request has a body, or a model in its URL, in a form the reader does not know, a body it cannot read
	// again (no GetBody, or larger than the reader reads), or two models that differ.
	unread bool
	// untrusted: the request is sent with a Host other than its URL's.
	untrusted bool
	// detached: the request was made on another context than the attempt's own. It is not known to be the attempt's:
	// it never names an entry, though it can still be the answering executor's housekeeping.
	detached bool

	method, path, mediaType string // for the closed housekeeping list
	empty                   bool   // the request has no body
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

// ProvimuxSent returns what the attempt of ctx sent, when the answer's entry can be certain: among its requests
// answered with a 2xx, other than the closed housekeeping list of the executor that answered (executor, a kind of the
// closed executor list), there is exactly one; it was sent to its URL's own authority on the attempt's context; its
// model was read; and no connection was made in the attempt that the decorator did not see (an upstream WebSocket
// among them). ok is false in every other case.
func ProvimuxSent(ctx context.Context, executor string) (sent ProvimuxSentRequest, ok bool) {
	log := provimuxSentLogOf(ctx)
	if log == nil || executor == "" || log.conns.Load() > log.observed.Load() {
		return ProvimuxSentRequest{}, false
	}
	log.mu.Lock()
	requests := append([]ProvimuxSentRequest(nil), log.requests...)
	overflow := log.overflow
	log.mu.Unlock()
	if overflow {
		return ProvimuxSentRequest{}, false
	}
	candidates := 0
	for _, request := range requests {
		if request.Status < 200 || request.Status > 299 || provimuxHousekeeping(executor, request) {
			continue
		}
		candidates++
		sent = request
	}
	if candidates != 1 || sent.unread || sent.untrusted || sent.detached || sent.Model == "" {
		return ProvimuxSentRequest{}, false
	}
	return sent, true
}

// provimuxObserveSent returns ctx with the attempt's log, its connection trace and Provimux's decorator on it. Only
// WithUpstreamAttemptTracker calls it. With models.answered-by off it adds nothing.
func provimuxObserveSent(ctx context.Context) context.Context {
	if ctx == nil || !logging.ProvimuxAnsweredBy() {
		return ctx
	}
	log := &provimuxSentLog{parent: provimuxSentLogOf(ctx)}
	ctx = context.WithValue(ctx, provimuxSentLogKey{}, log)
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GetConn: func(string) { log.conns.Add(1) }})
	return httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
		return &provimuxSentObserver{next: next, log: log}
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
	if own != o.log {
		// A request on another context than the attempt's (a detached one): it is not known to be the attempt's
		// own, and no entry is named for it.
		observed.detached = true
	}
	sent := req
	if own != nil {
		// The connection this request asks for is a seen one, for its attempt and every attempt it runs inside.
		sent = sent.WithContext(httptrace.WithClientTrace(sent.Context(), &httptrace.ClientTrace{GetConn: func(string) {
			for log := own; log != nil; log = log.parent {
				log.observed.Add(1)
			}
		}}))
	}
	resp, err := o.next.RoundTrip(sent)
	if err == nil && resp != nil {
		observed.Status = resp.StatusCode
		if observed.Status >= 200 && observed.Status <= 299 && !observed.unread {
			observed.Model, observed.unread = provimuxModelOf(req, observed.mediaType)
		}
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

// provimuxObserveRequest notes what req sends, without the model and the status.
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
	observed.method, observed.path = req.Method, req.URL.Path
	observed.mediaType, _, _ = mime.ParseMediaType(req.Header.Get("Content-Type"))
	observed.mediaType = strings.ToLower(observed.mediaType)
	if host := strings.TrimSpace(req.Host); host != "" && !provimuxSameAuthority(host, observed) {
		observed.untrusted = true
	}
	observed.empty = req.Body == nil || req.Body == http.NoBody
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

// provimuxModelOf reads the model of req, sent with mediaType: the <id> of a URL path .../models/<id>:<method>; from
// its body as GetBody gives it again (zstd decoded where Content-Encoding says so): the top-level "model" of a JSON
// body (or of a form), field 21 of a Connect message to Devin's chat method. unread is true where the body cannot be
// read again, where a body or a model is in a form the reader does not know, and where two models differ.
func provimuxModelOf(req *http.Request, mediaType string) (model string, unread bool) {
	pathModel, pathUnread := provimuxPathModel(req.URL)
	if pathUnread {
		return "", true
	}
	body, readable := provimuxBodyOf(req)
	if readable {
		body, readable = provimuxDecode(body, provimuxEncodings(req.Header))
	}
	if !readable {
		return "", true
	}
	bodyModel, bodyUnread := provimuxBodyModel(req.URL.Path, mediaType, body)
	switch {
	case bodyUnread, !utf8.ValidString(bodyModel):
		return "", true
	case pathModel != "" && bodyModel != "" && pathModel != bodyModel:
		return "", true
	case pathModel != "":
		return pathModel, false
	}
	return bodyModel, false
}

// provimuxBodyOf reads req's body again, from GetBody, as a string that lives only while the model is read: "" for a
// request without a body. ok is false without GetBody, for a body larger than the reader reads, and where it cannot be
// read.
func provimuxBodyOf(req *http.Request) (body string, ok bool) {
	if req.Body == nil || req.Body == http.NoBody {
		return "", true
	}
	if req.GetBody == nil || req.ContentLength > provimuxSentBodyLimit {
		return "", false
	}
	reader, err := req.GetBody()
	if err != nil {
		return "", false
	}
	defer func() { _ = reader.Close() }()
	var text strings.Builder
	if req.ContentLength > 0 {
		text.Grow(int(req.ContentLength))
	}
	n, err := io.Copy(&text, io.LimitReader(reader, provimuxSentBodyLimit+1))
	if err != nil || n > provimuxSentBodyLimit {
		return "", false
	}
	return text.String(), true
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
	return strings.Clone(id), false
}

// provimuxBodyModel reads the model of a body sent to path with mediaType.
func provimuxBodyModel(path, mediaType, body string) (model string, unread bool) {
	if strings.TrimSpace(body) == "" {
		return "", false
	}
	switch {
	case mediaType == "application/connect+proto":
		return provimuxConnectModel(path, []byte(body))
	case mediaType == "application/x-www-form-urlencoded":
		return provimuxFormModel(body)
	case strings.Contains(mediaType, "json") || (mediaType == "" && strings.HasPrefix(strings.TrimSpace(body), "{")):
		return provimuxJSONModel(body)
	}
	return "", true
}

// provimuxEncodings lists a request's content encodings, in order, without identity.
func provimuxEncodings(header http.Header) []string {
	var encodings []string
	for _, value := range header.Values("Content-Encoding") {
		for _, encoding := range strings.Split(value, ",") {
			if encoding = strings.ToLower(strings.TrimSpace(encoding)); encoding != "" && encoding != "identity" {
				encodings = append(encodings, encoding)
			}
		}
	}
	return encodings
}

// provimuxDecode decodes body for its encodings: none, or zstd alone. Any other is not read.
func provimuxDecode(body string, encodings []string) (string, bool) {
	switch {
	case len(encodings) == 0:
		return body, true
	case len(encodings) == 1 && encodings[0] == "zstd":
		decoder, errDecoder := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(provimuxSentBodyLimit))
		if errDecoder != nil {
			return "", false
		}
		defer decoder.Close()
		decoded, errDecode := decoder.DecodeAll([]byte(body), nil)
		if errDecode != nil {
			return "", false
		}
		return string(decoded), true
	}
	return "", false
}

// provimuxJSONModel reads the top-level "model" of a JSON object: a string, once. A body that is not an object, a
// "model" that is not a non-empty string, and two "model" keys are not read.
func provimuxJSONModel(body string) (model string, unread bool) {
	if !ojson.Valid(body) {
		return "", true
	}
	root := ojson.Parse(body)
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
		seen, model = true, strings.Clone(value.String()) // a string of its own: the body is let go after this
		return true
	})
	if unread {
		return "", true
	}
	return model, false
}

// provimuxFormModel reads the "model" of a form: once, or the same each time.
func provimuxFormModel(body string) (model string, unread bool) {
	values, errParse := url.ParseQuery(body)
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
	return strings.Clone(models[0]), false // a string of its own: the body is let go after this
}

// provimuxDevinChatMethod is the path of Devin's chat method (helps.DevinChatPath), whose message carries the model
// in field 21 (helps.BuildDevinGetChatMessageRequest).
const provimuxDevinChatMethod = "/exa.api_server_pb.ApiServerService/GetChatMessage"

// provimuxConnectModel reads field 21 of the one uncompressed Connect frame of a request to Devin's chat method. Any
// other method carries no model; a chat request in another shape is not read.
func provimuxConnectModel(path string, body []byte) (model string, unread bool) {
	if !strings.HasSuffix(path, provimuxDevinChatMethod) {
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

// provimuxHousekeeping reports whether request is on the closed list of requests of executor (a kind of the closed
// executor list) that cannot produce an answer (the lead's amendments of 2026-11-27): a token exchange or refresh, a
// model catalog, a project discovery. A request is housekeeping only when the executor that answered is the one that
// sends it, and its method, exact authority (https on the default port, the Host its URL's), exact path and shape are
// those of the list; it carries no model and nothing else makes it doubtful. Every other request counts as one that
// could have produced the answer. The list is closed: a request upstream adds is a candidate until it is named here.
func provimuxHousekeeping(executor string, request ProvimuxSentRequest) bool {
	if request.Model != "" || request.unread || request.untrusted || request.Scheme != "https" || request.Port != "" {
		return false
	}
	for _, known := range provimuxHousekeepingList {
		if known.executor != executor || known.method != request.method || known.host != request.Host || known.path != request.path {
			continue
		}
		switch known.shape {
		case provimuxShapeForm:
			if request.mediaType == "application/x-www-form-urlencoded" {
				return true
			}
		case provimuxShapeJSON:
			if request.mediaType == "application/json" {
				return true
			}
		case provimuxShapeEmpty:
			if request.empty {
				return true
			}
		}
	}
	return false
}

// The shapes of a housekeeping request's body.
const (
	provimuxShapeEmpty = iota // no body (a GET)
	provimuxShapeForm         // a form (a token endpoint)
	provimuxShapeJSON         // JSON without a model
)

// provimuxHousekeepingRequest is one entry of the closed list: the executor that sends it, a method, an exact host, an
// exact path, and the shape of its body.
type provimuxHousekeepingRequest struct {
	executor   string
	method     string
	host, path string
	shape      int
}

// provimuxHousekeepingList is the closed list. Claude, Codex, xAI, Meta, Devin, Kimi and OpenAI-compatible keys make
// no such request on an attempt's clients: their token refreshes and profile reads run on clients of their own (a
// connection one of them makes on the attempt's context is unseen, and cancels), or in an attempt of their own.
var provimuxHousekeepingList = provimuxHousekeepingEntries()

func provimuxHousekeepingEntries() []provimuxHousekeepingRequest {
	var list []provimuxHousekeepingRequest
	// Google's OAuth token endpoint: a Vertex service account's token exchange, an Antigravity or Gemini login's
	// refresh. A form POST.
	for _, executor := range []string{"vertex", "antigravity", "gemini"} {
		list = append(list,
			provimuxHousekeepingRequest{executor: executor, method: http.MethodPost, host: "oauth2.googleapis.com", path: "/token", shape: provimuxShapeForm},
			provimuxHousekeepingRequest{executor: executor, method: http.MethodPost, host: "www.googleapis.com", path: "/oauth2/v4/token", shape: provimuxShapeForm})
	}
	// Antigravity: project discovery, onboarding and the user's information, before a first answer of a login. JSON
	// without a model, to its three hosts.
	for _, host := range []string{"cloudcode-pa.googleapis.com", "daily-cloudcode-pa.googleapis.com", "daily-cloudcode-pa.sandbox.googleapis.com"} {
		for _, path := range []string{"/v1internal:loadCodeAssist", "/v1internal:onboardUser", "/v1internal:fetchUserInfo"} {
			list = append(list, provimuxHousekeepingRequest{executor: "antigravity", method: http.MethodPost, host: host, path: path, shape: provimuxShapeJSON})
		}
	}
	// Copilot: the exchange of the GitHub token for Copilot's, and Copilot's model catalog on its four API hosts.
	list = append(list, provimuxHousekeepingRequest{executor: "copilot", method: http.MethodGet, host: "api.github.com", path: "/copilot_internal/v2/token", shape: provimuxShapeEmpty})
	for _, host := range []string{"api.githubcopilot.com", "api.individual.githubcopilot.com", "api.business.githubcopilot.com", "api.enterprise.githubcopilot.com"} {
		list = append(list, provimuxHousekeepingRequest{executor: "copilot", method: http.MethodGet, host: host, path: "/models", shape: provimuxShapeEmpty})
	}
	return list
}
