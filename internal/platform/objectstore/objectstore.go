// Package objectstore is the port every file in KAPSORA travels through. It exists
// because of one rule the document pipeline is built on (v1.2 39.14): a file is never
// stored in the database, and the API never carries its bytes either. A client uploads
// straight into the quarantine bucket through a short-lived presigned URL and downloads
// straight out of the secure bucket through another one; the only bytes the platform's own
// processes ever read are the ones the worker streams to the virus scanner.
//
// The interface is deliberately small and says nothing about S3. Everything above it
// speaks in buckets and keys, which is what makes the whole pipeline testable against an
// in-memory store and what would let a different object store be dropped in without
// touching a use case.
package objectstore

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned when a key does not exist in the bucket. It is a value rather
// than a status code so callers do not have to know the transport underneath.
var ErrNotFound = errors.New("objectstore: object not found")

// PresignedURL is a URL that grants one operation on one key for a limited time. It is
// the only thing a client ever receives: the API hands out permission to talk to the
// object store, never the bytes themselves.
type PresignedURL struct {
	URL       string
	Method    string
	ExpiresAt time.Time
	// Headers are the request headers the client must send exactly as given. They are
	// part of the signature, so a client that sends a different content length or media
	// type is refused by the object store rather than by a check the API would have had
	// to make on bytes it never sees.
	Headers map[string]string
}

// PutConstraint is what an upload URL is allowed to write. Both fields are signed into
// the URL: this is where the size limit of an upload actually lives, because the API has
// no body to measure (WP-I4-04 section 2.3).
type PutConstraint struct {
	ContentType string
	ByteSize    int64
	TTL         time.Duration
}

// Store is the object storage port.
type Store interface {
	// PresignPut returns a URL that writes exactly one object of the declared size and
	// media type.
	PresignPut(ctx context.Context, bucket, key string, c PutConstraint) (PresignedURL, error)
	// PresignGet returns a URL that reads one object for ttl.
	PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (PresignedURL, error)
	// Open streams an object. The worker uses it to feed the scanner; nothing else in the
	// platform reads a file body.
	Open(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	// Write stores bytes the platform itself produced. It is the one way into this store
	// that does not go through a client's presigned URL, and it exists for exactly one kind
	// of file: the ones nobody uploaded. WP-I7-05's exports are rendered by the worker, so
	// there is no browser to hand a URL to and no untrusted bytes to quarantine — the
	// process that wrote them is the process that stores them.
	//
	// It is deliberately not a way to accept an upload. Nothing in the API layer may call
	// it, because the API never holds a file body (v1.2 39.14).
	Write(ctx context.Context, bucket, key string, body []byte, contentType string) error
	// Copy is a server-side copy: the promotion from quarantine to secure never pulls the
	// bytes through this process.
	Copy(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) error
	// Remove deletes an object. Deleting a key that is already gone succeeds, so the
	// worker can be redelivered the same scan without the second run failing.
	Remove(ctx context.Context, bucket, key string) error
	// Exists reports whether a key is present. It is what the infected-file test asks the
	// secure bucket, and what the promotion asks before it deletes the quarantine copy.
	Exists(ctx context.Context, bucket, key string) (bool, error)
}
