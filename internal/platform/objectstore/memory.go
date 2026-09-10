package objectstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
)

// Memory is an in-memory Store. It exists so the document pipeline can be exercised
// end to end on a machine with no object store running: the sequence quarantine → scan →
// secure, and the promise that an infected file leaves no bytes behind, are properties of
// the pipeline rather than of MinIO, and they are worth asserting either way.
//
// The presigned URLs it returns are not fetchable. A test that means to upload or download
// bytes calls Put and Bytes directly; a test that means to check that a URL was handed out
// at all reads the fields.
type Memory struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{objects: map[string][]byte{}}
}

var _ Store = (*Memory)(nil)

func memKey(bucket, key string) string { return bucket + "/" + key }

// Put writes bytes directly, standing in for the client's upload through a presigned URL.
func (m *Memory) Put(bucket, key string, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[memKey(bucket, key)] = bytes.Clone(body)
}

// Write implements Store. It is Put with a context, an error and a media type the in-memory
// store has no use for: the interface carries all three because the S3 client needs them.
func (m *Memory) Write(_ context.Context, bucket, key string, body []byte, _ string) error {
	if bucket == "" || key == "" {
		return errors.New("objectstore: bucket and key are required")
	}
	m.Put(bucket, key, body)
	return nil
}

// Bytes returns the stored body and whether the key exists.
func (m *Memory) Bytes(bucket, key string) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	body, ok := m.objects[memKey(bucket, key)]
	return bytes.Clone(body), ok
}

// Keys returns every key stored in a bucket, sorted.
func (m *Memory) Keys(bucket string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	prefix := bucket + "/"
	for key := range m.objects {
		if after, ok := strings.CutPrefix(key, prefix); ok {
			out = append(out, after)
		}
	}
	slices.Sort(out)
	return out
}

// PresignPut implements Store.
func (m *Memory) PresignPut(_ context.Context, bucket, key string, c PutConstraint) (PresignedURL, error) {
	return PresignedURL{
		URL: fmt.Sprintf("memory://%s/%s?upload", bucket, key), Method: "PUT",
		ExpiresAt: time.Now().UTC().Add(c.TTL),
		Headers:   map[string]string{"Content-Type": c.ContentType},
	}, nil
}

// PresignGet implements Store.
func (m *Memory) PresignGet(_ context.Context, bucket, key string, ttl time.Duration) (PresignedURL, error) {
	if _, ok := m.Bytes(bucket, key); !ok {
		return PresignedURL{}, fmt.Errorf("%w: %s/%s", ErrNotFound, bucket, key)
	}
	return PresignedURL{
		URL: fmt.Sprintf("memory://%s/%s?download", bucket, key), Method: "GET",
		ExpiresAt: time.Now().UTC().Add(ttl), Headers: map[string]string{},
	}, nil
}

// Open implements Store.
func (m *Memory) Open(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	body, ok := m.Bytes(bucket, key)
	if !ok {
		return nil, fmt.Errorf("%w: %s/%s", ErrNotFound, bucket, key)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// Copy implements Store.
func (m *Memory) Copy(_ context.Context, srcBucket, srcKey, dstBucket, dstKey string) error {
	body, ok := m.Bytes(srcBucket, srcKey)
	if !ok {
		return fmt.Errorf("%w: %s/%s", ErrNotFound, srcBucket, srcKey)
	}
	m.Put(dstBucket, dstKey, body)
	return nil
}

// Remove implements Store; removing an absent key succeeds.
func (m *Memory) Remove(_ context.Context, bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, memKey(bucket, key))
	return nil
}

// Exists implements Store.
func (m *Memory) Exists(_ context.Context, bucket, key string) (bool, error) {
	_, ok := m.Bytes(bucket, key)
	return ok, nil
}
