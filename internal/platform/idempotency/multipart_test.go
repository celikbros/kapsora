package idempotency

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMultipartHashIgnoresBoundaryButPreservesContent(t *testing.T) {
	hash := func(boundary, filename, content string, fields ...string) []byte {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := writer.SetBoundary(boundary); err != nil {
			t.Fatal(err)
		}
		for _, value := range fields {
			if err := writer.WriteField("sourceVersion", value); err != nil {
				t.Fatal(err)
			}
		}
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, content); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/imports", nil)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		return requestHash(req, Scope{}, body.Bytes())
	}
	first := hash("first-boundary", "members.csv", " a,b\r\n", "v1")
	if !bytes.Equal(first, hash("retry-boundary", "members.csv", " a,b\r\n", "v1")) {
		t.Fatal("transport boundary changed the request hash")
	}
	for name, changed := range map[string][]byte{
		"file bytes":      hash("first-boundary", "members.csv", "a,b\r\n", "v1"),
		"file name":       hash("first-boundary", "other.csv", " a,b\r\n", "v1"),
		"field value":     hash("first-boundary", "members.csv", " a,b\r\n", "v2"),
		"duplicate field": hash("first-boundary", "members.csv", " a,b\r\n", "v1", "v1"),
	} {
		if bytes.Equal(first, changed) {
			t.Errorf("%s did not change the request hash", name)
		}
	}
	if bytes.Equal(hash("one", "members.csv", "data", "v1", "v2"), hash("two", "members.csv", "data", "v2", "v1")) {
		t.Fatal("duplicate field order must remain significant")
	}
}

func TestMalformedMultipartHashPreservesOriginalBytes(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/imports", nil)
	req.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	body := []byte("--missing\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\ntruncated")
	if !bytes.Equal(body, canonicalRequestBody(req, body)) {
		t.Fatal("malformed multipart must not collapse to a partial canonical form")
	}
}
