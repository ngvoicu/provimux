package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"google.golang.org/protobuf/encoding/protowire"
)

// Provimux: what an attempt sent (.github/provimux/design/answered-by.md 4.2 and 4.3, step W6a). The entry of an
// answer is read from the request the gateway sent for it, never predicted. Every HTTP client an executor sends a model
// request with is built by a helper that applies upstream's observation seam (sdk/httptransport) after the proxy, the
// pool and the TLS profile are chosen. Provimux puts one decorator on that seam, in one place: the context an attempt
// gets its tracker on (WithUpstreamAttemptTracker). For each request a client hands its transport, the decorator finds
// the attempt's tracker in the request's own context and notes there what was sent: the scheme, host and port of the
// URL, the model read from the request as its bytes are, the names of its credential headers and, when the response's
// headers arrive, the status. It delegates to the transport below it and changes nothing: the body is read through
// GetBody (a second reader over the same bytes), never consumed. With models.answered-by off it is not attached.

// ProvimuxSentRequest is one request an attempt handed to its transport, as Provimux's decorator saw it.
type ProvimuxSentRequest struct {
	Scheme string // of the request's URL, lower case
	Host   string // its host, lower case, without the port
	Port   string // its port; "" = the scheme's default
	Model  string // the model id read from the request (answered-by.md 4.2); "" = none was read
	Signer string // which credential headers the request carries, by name only: "x-api-key", "authorization", "both", ""
	Status int    // the status of its response; 0 = no response arrived

	// unread: the request has a body, or a model in its URL, in a form the reader does not know, or two models that
	// differ. Nothing is known of what it sent: if it was answered, neither is the attempt's model.
	unread bool
}

// provimuxSentKept is how many requests an attempt's tracker keeps. An attempt that makes more names no model.
const provimuxSentKept = 16

// provimuxSentBodyLimit is the largest body the reader reads. A larger one is not read.
const provimuxSentBodyLimit = 64 << 20

// provimuxSentLog is the attempt's observations: a field of its tracker.
type provimuxSentLog struct {
	mu       sync.Mutex
	requests []ProvimuxSentRequest
	overflow bool
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

func provimuxSentLogOf(ctx context.Context) *provimuxSentLog {
	if ctx == nil {
		return nil
	}
	tracker, ok := ctx.Value(upstreamAttemptTrackerContextKey{}).(*upstreamAttemptTracker)
	if !ok || tracker == nil {
		return nil
	}
	return &tracker.provimuxSent
}

// ProvimuxSent returns what the attempt of ctx sent, by step 2 of answered-by.md 4.3: the one host, model and signer
// that its observations with a model and a 2xx response agree on. ok is false when there is no such observation, when
// they differ, when an answered request could not be read, and when more were made than the tracker keeps.
func ProvimuxSent(ctx context.Context) (sent ProvimuxSentRequest, ok bool) {
	log := provimuxSentLogOf(ctx)
	if log == nil {
		return ProvimuxSentRequest{}, false
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.overflow {
		return ProvimuxSentRequest{}, false
	}
	for _, request := range log.requests {
		if request.Status < 200 || request.Status > 299 {
			continue
		}
		if request.unread {
			return ProvimuxSentRequest{}, false
		}
		if request.Model == "" {
			continue
		}
		if !ok {
			sent, ok = request, true
			continue
		}
		if request.Scheme != sent.Scheme || request.Host != sent.Host || request.Port != sent.Port || request.Model != sent.Model || request.Signer != sent.Signer {
			return ProvimuxSentRequest{}, false
		}
	}
	return sent, ok
}

// provimuxObserveSent returns ctx with Provimux's decorator on it, around any decorator ctx already carries. Only
// WithUpstreamAttemptTracker calls it. The decorator notes each request of a context that has a tracker on that
// tracker and delegates; with models.answered-by off it is not attached.
func provimuxObserveSent(ctx context.Context) context.Context {
	if ctx == nil || !logging.ProvimuxAnsweredBy() {
		return ctx
	}
	outer := ctx
	return httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
		// The decorator ctx carried before ours (an embedder's), applied first, inside ours.
		inner := next
		if decorated := httptransport.Decorate(outer, &http.Client{Transport: next}); decorated != nil && decorated.Transport != nil {
			inner = decorated.Transport
		}
		if _, ours := inner.(*provimuxSentObserver); ours {
			return inner
		}
		return &provimuxSentObserver{next: inner}
	})
}

// provimuxSentObserver is the decorator's transport: it notes each request on the tracker of the request's own
// context, and delegates.
type provimuxSentObserver struct{ next http.RoundTripper }

func (o *provimuxSentObserver) RoundTrip(req *http.Request) (*http.Response, error) {
	log := provimuxSentLogOf(req.Context())
	if log == nil {
		return o.next.RoundTrip(req)
	}
	observed := provimuxObserveRequest(req)
	resp, err := o.next.RoundTrip(req)
	if err == nil && resp != nil {
		observed.Status = resp.StatusCode
	}
	log.add(observed)
	return resp, err
}

// CloseIdleConnections passes the client's call on, as the transport below would receive it without the decorator.
func (o *provimuxSentObserver) CloseIdleConnections() {
	if closer, ok := o.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
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
	return observed
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
