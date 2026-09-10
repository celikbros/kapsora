package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// S3 talks to an S3-compatible object store (MinIO natively, per ADR-021 — there is no
// container anywhere in this project). Signature Version 4 is implemented here rather
// than pulled in as a dependency: the six operations this package needs are a small,
// stable corner of the protocol, and the alternative is an SDK an order of magnitude
// larger than the code that uses it.
//
// Every request is path-style (`/bucket/key`). A native MinIO on an IP address has no
// virtual-host DNS to resolve a bucket name against, and path-style is what the local
// runbook, the deployment and the tests all use.
type S3 struct {
	endpoint  *url.URL
	region    string
	accessKey string
	secretKey string
	http      *http.Client
	now       func() time.Time
}

// S3Options configures the client.
type S3Options struct {
	// Endpoint is the base URL of the store, for example http://127.0.0.1:9000. A bare
	// host:port is accepted and read as http.
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
	// HTTPClient defaults to a client with a 60 second timeout: the worker streams whole
	// files through it, and the default zero-timeout client would hang forever on a store
	// that stopped answering mid-body.
	HTTPClient *http.Client
	// Now defaults to time.Now; tests pin it so a signature is reproducible.
	Now func() time.Time
}

const (
	signAlgorithm = "AWS4-HMAC-SHA256"
	signService   = "s3"
	// unsignedPayload is what a presigned URL declares instead of a body hash: the client
	// that will send the body is not the party that signs the URL.
	unsignedPayload = "UNSIGNED-PAYLOAD"
	// emptyPayloadHash is sha256 of the empty string, the body of every request this
	// process makes itself.
	emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	isoLayout        = "20060102T150405Z"
	dateLayout       = "20060102"
	// maxPresignTTL is the ceiling S3 itself imposes on a presigned URL (seven days).
	maxPresignTTL = 7 * 24 * time.Hour
)

// NewS3 validates the options and returns the client.
func NewS3(o S3Options) (*S3, error) {
	if strings.TrimSpace(o.Endpoint) == "" {
		return nil, errors.New("objectstore: endpoint is required")
	}
	raw := strings.TrimSpace(o.Endpoint)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	endpoint, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("objectstore: parse endpoint: %w", err)
	}
	if endpoint.Host == "" {
		return nil, fmt.Errorf("objectstore: endpoint %q has no host", o.Endpoint)
	}
	if o.AccessKey == "" || o.SecretKey == "" {
		return nil, errors.New("objectstore: access key and secret key are required")
	}
	if o.Region == "" {
		o.Region = "us-east-1"
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	return &S3{
		endpoint: &url.URL{Scheme: endpoint.Scheme, Host: endpoint.Host},
		region:   o.Region, accessKey: o.AccessKey, secretKey: o.SecretKey,
		http: o.HTTPClient, now: o.Now,
	}, nil
}

var _ Store = (*S3)(nil)

// PresignPut implements Store. The content type and the content length are signed
// headers, which is what turns them into a limit rather than a suggestion: the store
// rejects a body of any other size, so the size cap holds without the API ever seeing a
// byte of it.
func (s *S3) PresignPut(_ context.Context, bucket, key string, c PutConstraint) (PresignedURL, error) {
	if c.ByteSize < 0 {
		return PresignedURL{}, fmt.Errorf("objectstore: byte size %d is negative", c.ByteSize)
	}
	headers := map[string]string{
		"content-length": strconv.FormatInt(c.ByteSize, 10),
	}
	if c.ContentType != "" {
		headers["content-type"] = c.ContentType
	}
	return s.presign(http.MethodPut, bucket, key, headers, c.TTL)
}

// PresignGet implements Store.
func (s *S3) PresignGet(_ context.Context, bucket, key string, ttl time.Duration) (PresignedURL, error) {
	return s.presign(http.MethodGet, bucket, key, nil, ttl)
}

// Open implements Store.
func (s *S3) Open(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	resp, err := s.do(ctx, http.MethodGet, bucket, key, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Write implements Store: one signed PUT carrying a body.
//
// It does not go through `do`, and the reason is the whole of SigV4: every other request this
// client makes has an empty body and signs the hash of the empty string, while this one has to
// sign the hash of what it is sending. A request that signed the wrong payload hash would be
// refused by the store with a message about credentials, which is the least useful place to
// spend an afternoon.
func (s *S3) Write(ctx context.Context, bucket, key string, body []byte, contentType string) error {
	if bucket == "" || key == "" {
		return errors.New("objectstore: bucket and key are required")
	}
	digest := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(digest[:])

	path := "/" + bucket + "/" + escapePath(key)
	target := *s.endpoint
	target.Path = "/" + bucket + "/" + key
	target.RawPath = path

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(),
		bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("objectstore: build request: %w", err)
	}
	req.ContentLength = int64(len(body))

	now := s.now().UTC()
	amzDate := now.Format(isoLayout)
	scope := now.Format(dateLayout) + "/" + s.region + "/" + signService + "/aws4_request"
	signable := map[string]string{
		"x-amz-date": amzDate, "x-amz-content-sha256": payloadHash,
		"content-length": strconv.Itoa(len(body)),
	}
	if contentType != "" {
		signable["content-type"] = contentType
	}
	canonicalHeaders, signedHeaders := canonicalizeHeaders(s.endpoint.Host, signable)
	canonical := strings.Join([]string{
		http.MethodPut, path, "", canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")
	signature := s.sign(now, canonical, amzDate, scope)

	for name, value := range signable {
		req.Header.Set(name, value)
	}
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		signAlgorithm, s.accessKey, scope, signedHeaders, signature))

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("objectstore: PUT %s/%s: %w", bucket, key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("objectstore: PUT %s/%s: status %d: %s",
			bucket, key, resp.StatusCode, summarize(detail))
	}
	return nil
}

// Copy implements Store. It is a server-side copy: the promotion of a clean file from
// quarantine to secure never pulls the body through this process, which is both faster
// and the only way the promotion can be safe on a file the API is not allowed to hold.
func (s *S3) Copy(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) error {
	source := "/" + srcBucket + "/" + escapePath(srcKey)
	resp, err := s.do(ctx, http.MethodPut, dstBucket, dstKey,
		map[string]string{"x-amz-copy-source": source})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// A copy answers 200 with an XML body that can still carry an error. Reading it is
	// how a failed copy stops looking like a successful one.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return fmt.Errorf("objectstore: copy %s/%s: read response: %w", srcBucket, srcKey, err)
	}
	if strings.Contains(string(body), "<Error>") {
		return fmt.Errorf("objectstore: copy %s/%s to %s/%s failed: %s",
			srcBucket, srcKey, dstBucket, dstKey, summarize(body))
	}
	return nil
}

// Remove implements Store. Deleting a key that is not there succeeds, because the worker
// may be handed the same scan twice and the second run must not fail on the tidy-up the
// first one already did.
func (s *S3) Remove(ctx context.Context, bucket, key string) error {
	resp, err := s.do(ctx, http.MethodDelete, bucket, key, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// Exists implements Store.
func (s *S3) Exists(ctx context.Context, bucket, key string) (bool, error) {
	resp, err := s.do(ctx, http.MethodHead, bucket, key, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, resp.Body.Close()
}

// presign builds a query-signed URL. The signed headers travel back to the caller in
// PresignedURL.Headers, because a header that was signed and then not sent produces a
// signature mismatch the client cannot diagnose.
func (s *S3) presign(method, bucket, key string, headers map[string]string, ttl time.Duration) (PresignedURL, error) {
	if bucket == "" || key == "" {
		return PresignedURL{}, errors.New("objectstore: bucket and key are required")
	}
	if ttl <= 0 {
		return PresignedURL{}, fmt.Errorf("objectstore: presign ttl %s must be positive", ttl)
	}
	if ttl > maxPresignTTL {
		return PresignedURL{}, fmt.Errorf("objectstore: presign ttl %s exceeds the %s maximum", ttl, maxPresignTTL)
	}
	now := s.now().UTC()
	amzDate := now.Format(isoLayout)
	scope := now.Format(dateLayout) + "/" + s.region + "/" + signService + "/aws4_request"

	canonicalHeaders, signedHeaders := canonicalizeHeaders(s.endpoint.Host, headers)
	query := url.Values{
		"X-Amz-Algorithm":     {signAlgorithm},
		"X-Amz-Credential":    {s.accessKey + "/" + scope},
		"X-Amz-Date":          {amzDate},
		"X-Amz-Expires":       {strconv.Itoa(int(ttl / time.Second))},
		"X-Amz-SignedHeaders": {signedHeaders},
	}
	path := "/" + bucket + "/" + escapePath(key)
	canonical := strings.Join([]string{
		method, path, encodeQuery(query), canonicalHeaders, signedHeaders, unsignedPayload,
	}, "\n")
	signature := s.sign(now, canonical, amzDate, scope)

	signedURL := *s.endpoint
	signedURL.Path = "/" + bucket + "/" + key
	signedURL.RawPath = path
	signedURL.RawQuery = encodeQuery(query) + "&X-Amz-Signature=" + signature

	out := PresignedURL{
		URL: signedURL.String(), Method: method, ExpiresAt: now.Add(ttl),
		Headers: map[string]string{},
	}
	for name, value := range headers {
		out.Headers[http.CanonicalHeaderKey(name)] = value
	}
	return out, nil
}

// do signs and performs one request with the Authorization header. It never sends a body:
// the six operations this client performs are all header-only, and the one that does carry
// bytes — the upload — is the client's, through a presigned URL.
func (s *S3) do(ctx context.Context, method, bucket, key string, headers map[string]string) (*http.Response, error) {
	if bucket == "" || key == "" {
		return nil, errors.New("objectstore: bucket and key are required")
	}
	path := "/" + bucket + "/" + escapePath(key)
	target := *s.endpoint
	target.Path = "/" + bucket + "/" + key
	target.RawPath = path

	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("objectstore: build request: %w", err)
	}
	now := s.now().UTC()
	amzDate := now.Format(isoLayout)
	scope := now.Format(dateLayout) + "/" + s.region + "/" + signService + "/aws4_request"

	signable := map[string]string{"x-amz-date": amzDate, "x-amz-content-sha256": emptyPayloadHash}
	for name, value := range headers {
		signable[strings.ToLower(name)] = value
	}
	canonicalHeaders, signedHeaders := canonicalizeHeaders(s.endpoint.Host, signable)
	canonical := strings.Join([]string{
		method, path, "", canonicalHeaders, signedHeaders, emptyPayloadHash,
	}, "\n")
	signature := s.sign(now, canonical, amzDate, scope)

	for name, value := range signable {
		req.Header.Set(name, value)
	}
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		signAlgorithm, s.accessKey, scope, signedHeaders, signature))

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("objectstore: %s %s/%s: %w", method, bucket, key, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: %s/%s", ErrNotFound, bucket, key)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("objectstore: %s %s/%s: status %d: %s",
			method, bucket, key, resp.StatusCode, summarize(detail))
	}
	return resp, nil
}

// sign derives the request signing key and signs the string to sign.
func (s *S3) sign(now time.Time, canonicalRequest, amzDate, scope string) string {
	hashed := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		signAlgorithm, amzDate, scope, hex.EncodeToString(hashed[:]),
	}, "\n")

	key := hmacSHA256([]byte("AWS4"+s.secretKey), now.Format(dateLayout))
	key = hmacSHA256(key, s.region)
	key = hmacSHA256(key, signService)
	key = hmacSHA256(key, "aws4_request")
	return hex.EncodeToString(hmacSHA256(key, stringToSign))
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// canonicalizeHeaders renders the canonical header block and the signed header list. Host
// is always signed; everything else comes from the caller.
func canonicalizeHeaders(host string, headers map[string]string) (canonical, signed string) {
	all := map[string]string{"host": host}
	for name, value := range headers {
		all[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(all[name])
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

// encodeQuery renders the canonical query string. url.Values.Encode is deliberately not
// used: it escapes with the HTML form rules, and SigV4 wants RFC 3986, where a space is
// %20 rather than a plus and a tilde is left alone.
func encodeQuery(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		for _, value := range values[key] {
			parts = append(parts, escape(key)+"="+escape(value))
		}
	}
	return strings.Join(parts, "&")
}

// escapePath escapes each path segment and keeps the separators, which is what S3's
// canonical URI is.
func escapePath(key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = escape(segment)
	}
	return strings.Join(segments, "/")
}

// escape is RFC 3986 unreserved-set percent encoding.
func escape(s string) string {
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// summarize trims a store error body to something a log line can carry.
func summarize(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if len(s) > 300 {
		return s[:300]
	}
	return s
}
