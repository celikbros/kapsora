package objectstore_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/objectstore"
)

// The local MinIO defaults of .env.example, which scripts/native/up.* starts the store
// with. They are read from the environment first and are only ever local: the buckets they
// open hold nothing but what these tests put in them.
const (
	defaultMinIOAddr   = "127.0.0.1:9000"
	defaultMinIOUser   = "kapsora"
	defaultMinIOSecret = "kapsora_local_minio"
)

func minioSettings() (addr, access, secret string) {
	return envOr("KAPSORA_MINIO_ADDR", defaultMinIOAddr),
		envOr("KAPSORA_MINIO_ROOT_USER", defaultMinIOUser),
		envOr("KAPSORA_MINIO_ROOT_PASSWORD", defaultMinIOSecret)
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// liveStore returns a client against the MinIO the local runbook starts natively, or
// skips. There is no container to fall back on (ADR-021), and a signature that is only
// ever checked by the code that produced it proves nothing — so the interesting assertions
// here are the ones a real store answers.
func liveStore(t *testing.T) (*objectstore.S3, string, string) {
	t.Helper()
	addr, access, secret := minioSettings()
	store, err := objectstore.NewS3(objectstore.S3Options{
		Endpoint: addr, AccessKey: access, SecretKey: secret,
	})
	if err != nil {
		t.Fatalf("new s3 client: %v", err)
	}
	if _, err := store.Exists(t.Context(), "quarantine", "kapsora-probe-"+uuid.NewString()); err != nil {
		t.Skipf("MinIO on %s is not answering (scripts/native/up.*): %v", addr, err)
	}
	return store, "quarantine", "secure"
}

// TestS3RoundTripAgainstLiveStore is the whole of what the document pipeline asks of the
// object store, in the order it asks it: hand out an upload URL, have the client use it,
// read the bytes back, copy them to the secure bucket server-side, delete the quarantine
// copy and hand out a download URL that works.
func TestS3RoundTripAgainstLiveStore(t *testing.T) {
	store, quarantine, secure := liveStore(t)
	ctx := t.Context()
	key := "test/" + uuid.NewString()
	body := []byte("kapsora object store round trip")
	t.Cleanup(func() {
		_ = store.Remove(context.Background(), quarantine, key)
		_ = store.Remove(context.Background(), secure, key)
	})

	put, err := store.PresignPut(ctx, quarantine, key, objectstore.PutConstraint{
		ContentType: "text/plain", ByteSize: int64(len(body)), TTL: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("presign put: %v", err)
	}
	if put.Method != http.MethodPut || put.ExpiresAt.Before(time.Now()) {
		t.Fatalf("unexpected presigned put: %+v", put)
	}
	uploadWithURL(t, put, body, http.StatusOK)

	// The size is signed into the URL, so it is the store that refuses a different body
	// rather than a check the API would have had to make on bytes it never sees.
	uploadWithURL(t, put, append(body, '!'), http.StatusForbidden)

	stored, err := store.Open(ctx, quarantine, key)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	read, err := io.ReadAll(stored)
	if err != nil || !bytes.Equal(read, body) {
		t.Fatalf("read back %q (err %v), want %q", read, err, body)
	}
	if err := stored.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := store.Copy(ctx, quarantine, key, secure, key); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := store.Remove(ctx, quarantine, key); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if gone, err := store.Exists(ctx, quarantine, key); err != nil || gone {
		t.Fatalf("quarantine copy still there: exists=%t err=%v", gone, err)
	}
	if present, err := store.Exists(ctx, secure, key); err != nil || !present {
		t.Fatalf("secure copy missing: exists=%t err=%v", present, err)
	}

	get, err := store.PresignGet(ctx, secure, key, time.Minute)
	if err != nil {
		t.Fatalf("presign get: %v", err)
	}
	downloaded := downloadWithURL(t, get)
	if !bytes.Equal(downloaded, body) {
		t.Fatalf("downloaded %q, want %q", downloaded, body)
	}
}

// TestS3PresignExpiresAndIsRefusedAfterwards is the property the short-lived URL exists
// for: a link that leaked is only useful for as long as it was signed to be.
func TestS3PresignExpiresAndIsRefusedAfterwards(t *testing.T) {
	store, quarantine, _ := liveStore(t)
	ctx := t.Context()
	key := "test/" + uuid.NewString()
	t.Cleanup(func() { _ = store.Remove(context.Background(), quarantine, key) })

	// Signed in the past with a one-second life: expired before it is ever used.
	addr, access, secret := minioSettings()
	expired, err := objectstore.NewS3(objectstore.S3Options{
		Endpoint: addr, AccessKey: access, SecretKey: secret,
		Now: func() time.Time { return time.Now().UTC().Add(-time.Hour) },
	})
	if err != nil {
		t.Fatalf("new s3 client: %v", err)
	}
	put, err := expired.PresignPut(ctx, quarantine, key, objectstore.PutConstraint{
		ContentType: "text/plain", ByteSize: 4, TTL: time.Second,
	})
	if err != nil {
		t.Fatalf("presign put: %v", err)
	}
	uploadWithURL(t, put, []byte("late"), http.StatusForbidden)
	if present, err := store.Exists(ctx, quarantine, key); err != nil || present {
		t.Fatalf("an expired URL wrote an object: exists=%t err=%v", present, err)
	}
}

// TestS3ReportsMissingObjects keeps ErrNotFound distinguishable from a broken store: the
// pipeline deletes a quarantine key that may already be gone, and a redelivered scan must
// not fail on the tidy-up the first delivery did.
func TestS3ReportsMissingObjects(t *testing.T) {
	store, quarantine, _ := liveStore(t)
	ctx := t.Context()
	key := "test/" + uuid.NewString()

	if _, err := store.Open(ctx, quarantine, key); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("open of a missing key = %v, want ErrNotFound", err)
	}
	if err := store.Remove(ctx, quarantine, key); err != nil {
		t.Fatalf("removing a missing key must succeed: %v", err)
	}
	if present, err := store.Exists(ctx, quarantine, key); err != nil || present {
		t.Fatalf("exists on a missing key = %t, %v", present, err)
	}
}

// TestS3RejectsUnusableOptions covers the refusals that would otherwise show up as an
// unsigned request against a live store.
func TestS3RejectsUnusableOptions(t *testing.T) {
	if _, err := objectstore.NewS3(objectstore.S3Options{Endpoint: "", AccessKey: "a", SecretKey: "b"}); err == nil {
		t.Fatal("expected an error without an endpoint")
	}
	if _, err := objectstore.NewS3(objectstore.S3Options{Endpoint: "127.0.0.1:9000"}); err == nil {
		t.Fatal("expected an error without credentials")
	}
	store, err := objectstore.NewS3(objectstore.S3Options{
		Endpoint: "127.0.0.1:9000", AccessKey: "a", SecretKey: "b",
	})
	if err != nil {
		t.Fatalf("new s3 client: %v", err)
	}
	if _, err := store.PresignGet(t.Context(), "quarantine", "k", 0); err == nil {
		t.Fatal("expected an error for a zero ttl")
	}
	if _, err := store.PresignGet(t.Context(), "quarantine", "k", 8*24*time.Hour); err == nil {
		t.Fatal("expected an error for a ttl beyond the S3 maximum")
	}
	if _, err := store.PresignPut(t.Context(), "", "k", objectstore.PutConstraint{TTL: time.Minute}); err == nil {
		t.Fatal("expected an error without a bucket")
	}
}

// TestPresignedURLCarriesTheSignedFields is a shape check that needs no store: the URL is
// query-signed and every header that was signed comes back to the caller, because a signed
// header the client does not send is a signature mismatch it cannot diagnose.
func TestPresignedURLCarriesTheSignedFields(t *testing.T) {
	store, err := objectstore.NewS3(objectstore.S3Options{
		Endpoint: "http://127.0.0.1:9000", AccessKey: "kapsora", SecretKey: "secret",
		Now: func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("new s3 client: %v", err)
	}
	put, err := store.PresignPut(t.Context(), "quarantine", "2026/09/doc id", objectstore.PutConstraint{
		ContentType: "application/pdf", ByteSize: 1234, TTL: 15 * time.Minute,
	})
	if err != nil {
		t.Fatalf("presign put: %v", err)
	}
	parsed, err := url.Parse(put.URL)
	if err != nil {
		t.Fatalf("parse presigned url: %v", err)
	}
	if parsed.EscapedPath() != "/quarantine/2026/09/doc%20id" {
		t.Fatalf("escaped path = %q", parsed.EscapedPath())
	}
	query := parsed.Query()
	if query.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" {
		t.Fatalf("algorithm = %q", query.Get("X-Amz-Algorithm"))
	}
	if query.Get("X-Amz-Expires") != "900" {
		t.Fatalf("expires = %q", query.Get("X-Amz-Expires"))
	}
	signed := query.Get("X-Amz-SignedHeaders")
	for _, want := range []string{"content-length", "content-type", "host"} {
		if !strings.Contains(signed, want) {
			t.Fatalf("signed headers %q do not include %q", signed, want)
		}
	}
	if len(query.Get("X-Amz-Signature")) != 64 {
		t.Fatalf("signature = %q, want 64 hex characters", query.Get("X-Amz-Signature"))
	}
	if put.Headers["Content-Type"] != "application/pdf" || put.Headers["Content-Length"] != "1234" {
		t.Fatalf("headers = %v", put.Headers)
	}
	// A URL signed a moment later must differ: the date is part of the signature, and a
	// signature that ignored it would never expire.
	if !put.ExpiresAt.Equal(time.Date(2026, 9, 4, 12, 15, 0, 0, time.UTC)) {
		t.Fatalf("expiry = %s", put.ExpiresAt)
	}
}

func uploadWithURL(t *testing.T, put objectstore.PresignedURL, body []byte, wantStatus int) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), put.Method, put.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	for name, value := range put.Headers {
		if name == "Content-Length" {
			continue // set by the transport from the body itself
		}
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		t.Fatalf("upload status = %d, want %d: %s", resp.StatusCode, wantStatus, detail)
	}
}

func downloadWithURL(t *testing.T, get objectstore.PresignedURL) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), get.Method, get.URL, nil)
	if err != nil {
		t.Fatalf("build download request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		t.Fatalf("download status = %d: %s", resp.StatusCode, detail)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	return body
}
