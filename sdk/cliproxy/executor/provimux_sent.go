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
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
	"unsafe"

	"github.com/MoeForge/moego/ojson"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/origin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"google.golang.org/protobuf/encoding/protowire"
)

// Provimux: what an attempt sent (.github/provimux/design/answered-by.md 4.2 and 4.3, step W6a, and the lead's
// amendments of 2026-11-27: the entry only where Provimux made every part). Every HTTP client a built-in executor sends
// a model request with is built by a helper that applies upstream's observation seam (sdk/httptransport) after the
// proxy, the pool and the TLS profile are chosen. Provimux puts one decorator on that seam, in one place: the context an
// attempt gets its tracker on (WithUpstreamAttemptTracker), together with a log of the attempt's own and a connection
// trace. The decorator notes each request a client built for the attempt hands its transport: the scheme, host and
// port of the URL, the bytes of the body exactly as the transport reads them (a copy taken while it reads; nothing is
// read ahead or twice), the names of its credential headers, and whether anything makes the request not wholly the
// gateway's own (a Host other than the URL's, a per-credential transport the gateway did not build, a Go default
// transport replaced, a decorator of another program on the context, a request on another context than the attempt's)
// and, when the response's headers arrive, the status. The model is read from those bytes when the answer is stated
// (ProvimuxSent), and housekeeping is matched then, against the executor that answered. The trace counts every
// connection the attempt's context asks for, so one the decorator did not see (an upstream WebSocket, a client no
// helper built) is known. It delegates and changes no byte. With models.answered-by off nothing is attached.

// ProvimuxSentRequest is one request an attempt handed to its transport, as Provimux's decorator saw it.
type ProvimuxSentRequest struct {
	Scheme string // of the request's URL, lower case
	Host   string // its host, lower case, without the port
	Port   string // its port; "" = the scheme's default
	Model  string // the model id read from the bytes the transport was handed (answered-by.md 4.2); "" = none
	Signer string // which credential headers the request carries, by name only: "x-api-key", "authorization", "both", ""
	Status int    // the status of its response; 0 = no response arrived

	// unread: the request has a body, or a model in its URL, in a form the reader does not know, two models that
	// differ, or bytes the transport did not read whole.
	unread bool
	// untrusted: the request is not wholly the gateway's own (see the package comment).
	untrusted bool
	// detached: the request was made on another context than the attempt's own. It is not known to be the attempt's:
	// it never names an entry, though it can still be the answering executor's housekeeping.
	detached bool

	method, path, mediaType string   // for the reader and the closed housekeeping list
	encodings               []string // its Content-Encoding, in order, without identity
	pathModel               string   // the model of its URL's path, "" for none
	body                    *provimuxBodyCapture
}

// provimuxSentKept is how many requests an attempt's log keeps. An attempt that makes more names no model.
const provimuxSentKept = 16

// provimuxSentBodyLimit is the largest body the reader keeps. A larger one is not read.
const provimuxSentBodyLimit = 64 << 20

// provimuxSentLog is one attempt's observations.
type provimuxSentLog struct {
	parent *provimuxSentLog // the log of the attempt this one runs inside, if any

	mu       sync.Mutex
	requests []ProvimuxSentRequest
	overflow bool
	// foreign: when the attempt began, its context carried an HTTP decorator that is not Provimux's own.
	foreign bool
	// asked: the attempt's caller asked for the statement; only then are request bodies copied.
	asked bool

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

// ProvimuxSent returns what the attempt of ctx sent, when the answer's entry can be certain (the lead's amendments of
// 2026-11-27): among its requests answered with a 2xx, other than the closed housekeeping list of the executor that
// answered (executor, a kind of the closed executor list), there is exactly one; it is wholly the gateway's own; its
// model was read from the bytes its transport read, whole; and no connection was made in the attempt that the
// decorator did not see (an upstream WebSocket among them). ok is false in every other case.
func ProvimuxSent(ctx context.Context, executor string) (sent ProvimuxSentRequest, ok bool) {
	log := provimuxSentLogOf(ctx)
	if log == nil || executor == "" || log.conns.Load() > log.observed.Load() {
		return ProvimuxSentRequest{}, false
	}
	log.mu.Lock()
	requests := append([]ProvimuxSentRequest(nil), log.requests...)
	overflow, foreign := log.overflow, log.foreign
	log.mu.Unlock()
	if overflow || foreign {
		return ProvimuxSentRequest{}, false
	}
	defer func() { // the copies go as soon as the models are read
		for _, request := range requests {
			request.body.release()
		}
	}()
	candidates := 0
	for _, request := range requests {
		if request.Status < 200 || request.Status > 299 {
			continue
		}
		request.Model, request.unread = provimuxModelOf(request)
		if provimuxHousekeeping(executor, request) {
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

// provimuxObserveSent returns ctx with the attempt's log, its connection trace and Provimux's decorator on it. A
// decorator ctx already carried (another program's) still runs, inside ours, and the attempt names no entry. Only
// WithUpstreamAttemptTracker calls it. With models.answered-by off it adds nothing.
func provimuxObserveSent(ctx context.Context) context.Context {
	if ctx == nil || !logging.ProvimuxAnsweredBy() {
		return ctx
	}
	log := &provimuxSentLog{parent: provimuxSentLogOf(ctx), foreign: provimuxForeignDecorator(ctx), asked: ProvimuxStatementAsked(ctx)}
	outer := ctx
	ctx = context.WithValue(ctx, provimuxSentLogKey{}, log)
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GetConn: func(string) { log.conns.Add(1) }})
	return httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
		// The decorator ctx carried before ours, applied first, inside ours. A decorator of an enclosing attempt is
		// replaced by this one: a client built for this attempt notes on this attempt's log.
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

// provimuxForeignDecorator reports whether ctx carries an HTTP decorator other than an enclosing attempt's Provimux
// decorator, applied as it is.
func provimuxForeignDecorator(ctx context.Context) bool {
	probe := provimuxProbeTransport{}
	decorated := httptransport.Decorate(ctx, &http.Client{Transport: probe})
	if decorated == nil || decorated.Transport == nil || decorated.Transport == http.RoundTripper(probe) {
		return false
	}
	ours, ok := decorated.Transport.(*provimuxSentObserver)
	return !ok || ours.next != http.RoundTripper(probe)
}

// provimuxProbeTransport is a transport no request is sent on: it only shows what a decorator does to a transport.
type provimuxProbeTransport struct{}

func (provimuxProbeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrNotSupported
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
	if !provimuxGatewayParts(req.Context()) {
		observed.untrusted = true
	}
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
	if o.log.asked {
		// The body is copied only for a call whose caller asked for the statement: no other call's answer names an
		// entry or is recorded (the amendment of 2026-11-27 (2), part 3).
		sent, observed.body = provimuxCaptureBody(sent)
	}
	resp, err := o.next.RoundTrip(sent)
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

// provimuxGatewayParts reports whether what the gateway's client helpers build a transport from is the gateway's own:
// the per-credential transport on ctx is none or one the gateway's own provider built (recognised by identity), and Go's
// default transport has not been replaced. A transport of another program, or one it replaced, gives no entry.
func provimuxGatewayParts(ctx context.Context) bool {
	if rt := ctx.Value("cliproxy.roundtripper"); rt != nil && !origin.GatewayTransport(rt) {
		return false
	}
	return origin.DefaultTransportUnchanged()
}

// provimuxObserveRequest notes what req sends, without the body and the status.
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
	var pathUnread bool
	observed.pathModel, pathUnread = provimuxPathModel(req.URL)
	observed.unread = pathUnread
	observed.encodings = provimuxEncodings(req.Header)
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

// provimuxModelOf reads the model of an observed request from what its transport was handed: the <id> of a URL path
// .../models/<id>:<method>; from the body's bytes as the transport read them (zstd decoded where Content-Encoding says
// so): the top-level "model" of a JSON body (or of a form), field 21 of a Connect message to Devin's chat method.
// unread is true where the request carries a body the transport did not read whole, a body or a model in a form the
// reader does not know, or two models that differ.
func provimuxModelOf(request ProvimuxSentRequest) (model string, unread bool) {
	if request.unread {
		return "", true
	}
	body, readable := request.body.bytes()
	if !readable {
		return "", true
	}
	body, readable = provimuxDecode(body, request.encodings)
	if !readable {
		return "", true
	}
	bodyModel, bodyUnread := provimuxBodyModel(request.path, request.mediaType, body)
	switch {
	case bodyUnread:
		return "", true
	case request.pathModel != "" && bodyModel != "" && request.pathModel != bodyModel:
		return "", true
	case request.pathModel != "":
		return request.pathModel, false
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

// provimuxBodyModel reads the model of a body sent to path with mediaType.
func provimuxBodyModel(path, mediaType string, body []byte) (model string, unread bool) {
	if len(bytes.TrimSpace(body)) == 0 {
		return "", false
	}
	switch {
	case mediaType == "application/connect+proto":
		return provimuxConnectModel(path, body)
	case mediaType == "application/x-www-form-urlencoded":
		return provimuxFormModel(body)
	case strings.Contains(mediaType, "json") || (mediaType == "" && bytes.HasPrefix(bytes.TrimSpace(body), []byte("{"))):
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
func provimuxDecode(body []byte, encodings []string) ([]byte, bool) {
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

// provimuxBodyCapture is a copy of the bytes a request's transport read from its body, taken while it read them: from
// the body itself and from each body a retry asked GetBody for. Nothing is read that the transport did not read.
type provimuxBodyCapture struct {
	mu       sync.Mutex
	none     bool // the request had no body
	released bool // the copies were let go
	streams  []*provimuxBodyStream
}

// provimuxBodyStream is what the transport read from one body.
type provimuxBodyStream struct {
	data   bytes.Buffer
	whole  bool // read to its end without an error
	failed bool // a read failed, or it outgrew the limit
}

// provimuxCaptureBody returns req, as its transport is to be handed it, with a body (and a GetBody) that copy what the
// transport reads, and the capture. The bytes, the length and the headers are those of req: nothing else changes.
func provimuxCaptureBody(req *http.Request) (*http.Request, *provimuxBodyCapture) {
	capture := &provimuxBodyCapture{}
	if req.Body == nil || req.Body == http.NoBody {
		capture.none = true
		return req, capture
	}
	sent := new(http.Request)
	*sent = *req
	size := req.ContentLength
	sent.Body = capture.tee(req.Body, size)
	if getBody := req.GetBody; getBody != nil {
		sent.GetBody = func() (io.ReadCloser, error) {
			body, err := getBody()
			if err != nil || body == nil || body == http.NoBody {
				return body, err
			}
			return capture.tee(body, size), nil
		}
	}
	return sent, capture
}

// tee returns body copying what is read from it into a stream of c, sized for length where it is known.
func (c *provimuxBodyCapture) tee(body io.ReadCloser, length int64) io.ReadCloser {
	stream := &provimuxBodyStream{}
	switch {
	case length > provimuxSentBodyLimit:
		stream.failed = true // larger than the reader reads: nothing is copied, and the model is not read
	case length > 0:
		stream.data.Grow(int(length))
	}
	c.mu.Lock()
	c.streams = append(c.streams, stream)
	c.mu.Unlock()
	return &provimuxTeeBody{body: body, stream: stream, capture: c}
}

// release lets the copies go: nothing is read from them after.
func (c *provimuxBodyCapture) release() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, stream := range c.streams {
		stream.data = bytes.Buffer{}
		stream.whole, stream.failed = false, true
	}
	c.released = true
}

// bytes returns what the transport read, when it read one body whole, or every body it read whole the same; ok is
// false where it read none whole, or two that differ.
func (c *provimuxBodyCapture) bytes() (body []byte, ok bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.released {
		return nil, false
	}
	if c.none {
		return nil, true
	}
	seen := false
	for _, stream := range c.streams {
		if stream.failed {
			return nil, false
		}
		if !stream.whole {
			continue
		}
		if seen && !bytes.Equal(body, stream.data.Bytes()) {
			return nil, false
		}
		body, seen = stream.data.Bytes(), true
	}
	return body, seen
}

// provimuxTeeBody hands the transport the body's bytes and keeps a copy of each.
type provimuxTeeBody struct {
	body    io.ReadCloser
	stream  *provimuxBodyStream
	capture *provimuxBodyCapture
}

func (t *provimuxTeeBody) Read(p []byte) (int, error) {
	n, err := t.body.Read(p)
	t.capture.mu.Lock()
	if n > 0 && !t.stream.failed {
		if t.stream.data.Len()+n > provimuxSentBodyLimit {
			t.stream.failed = true
			t.stream.data = bytes.Buffer{}
		} else {
			t.stream.data.Write(p[:n])
		}
	}
	switch {
	case err == io.EOF:
		t.stream.whole = !t.stream.failed
	case err != nil:
		t.stream.failed = true
	}
	t.capture.mu.Unlock()
	return n, err
}

func (t *provimuxTeeBody) Close() error { return t.body.Close() }

// provimuxJSONModel reads the top-level "model" of a JSON object: a string, once. A body that is not an object, a
// "model" that is not a non-empty string, and two "model" keys are not read.
func provimuxJSONModel(body []byte) (model string, unread bool) {
	if !ojson.ValidBytes(body) {
		return "", true
	}
	// A view of the bytes, not a copy of them (ParseBytes copies the whole body): the bytes do not change while it is
	// read, and the model is copied out of it below, before the copy of the body is let go.
	root := ojson.Parse(unsafe.String(unsafe.SliceData(body), len(body)))
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
		seen, model = true, strings.Clone(value.String())
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
	body, _ := request.body.bytes()
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
			if len(body) == 0 {
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

type provimuxStatementAskedKey struct{}

// WithProvimuxStatementAsked says, on a call's context, that its caller asked for the statement of who answered (the
// handlers do, where they install the statement's callback). Only such a call's request bodies are copied to read the
// model: no other call's answer names an entry or is recorded.
func WithProvimuxStatementAsked(ctx context.Context) context.Context {
	if ctx == nil || !logging.ProvimuxAnsweredBy() {
		return ctx
	}
	return context.WithValue(ctx, provimuxStatementAskedKey{}, true)
}

// ProvimuxStatementAsked reports whether the caller of ctx's call asked for the statement.
func ProvimuxStatementAsked(ctx context.Context) bool {
	asked, _ := ctx.Value(provimuxStatementAskedKey{}).(bool)
	return asked
}
