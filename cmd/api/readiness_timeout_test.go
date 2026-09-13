package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/platform/config"
)

func TestReadinessCancelsUnresponsiveStorage(t *testing.T) {
	store := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer store.Close()
	checker, err := newReadiness(func(context.Context) error { return nil }, config.DocumentConfig{
		Endpoint: store.URL, AccessKey: "synthetic-access", SecretKey: "synthetic-secret",
		QuarantineBucket: "quarantine", SecureBucket: "secure", ScannerAddr: "127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	result := checker.Run(ctx)
	if result.Status != "DOWN" || result.Checks["objectstore.quarantine"] != "DOWN" || result.Checks["objectstore.secure"] != "DOWN" {
		t.Fatalf("unexpected readiness: %+v", result)
	}
	if time.Since(started) > time.Second {
		t.Fatal("storage ignored readiness cancellation")
	}
}
