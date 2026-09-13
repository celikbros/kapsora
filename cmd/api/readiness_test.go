package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/platform/config"
	"github.com/celikbros/kapsora/internal/platform/health"
)

func TestReadinessTracksDocumentDependencies(t *testing.T) {
	var storeStatus atomic.Int32
	storeStatus.Store(http.StatusOK)
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || (r.URL.Path != "/quarantine" && r.URL.Path != "/secure") || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("expected authenticated bucket HEAD, got %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Fail only secure: readiness must probe both buckets.
		if r.URL.Path == "/secure" {
			w.WriteHeader(int(storeStatus.Load()))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer store.Close()
	var scannerDown atomic.Bool
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			command, err := bufio.NewReader(conn).ReadString(0)
			if err == nil && command == "zPING\x00" {
				reply := "PONG\x00"
				if scannerDown.Load() {
					reply = "ERROR\x00"
				}
				_, _ = conn.Write([]byte(reply))
			}
			_ = conn.Close()
		}
	}()
	checker, err := newReadiness(func(context.Context) error { return nil }, config.DocumentConfig{
		Endpoint: store.URL, AccessKey: "synthetic-access", SecretKey: "synthetic-secret",
		QuarantineBucket: "quarantine", SecureBucket: "secure", ScannerAddr: listener.Addr().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertStatus := func(want int, dependency string) {
		t.Helper()
		rec := httptest.NewRecorder()
		health.ReadyHandler(checker).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
		if rec.Code != want {
			t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
		}
		if dependency != "" && !strings.Contains(rec.Body.String(), dependency) {
			t.Fatalf("missing failed dependency: %s", rec.Body)
		}
		if strings.Contains(rec.Body.String(), "synthetic-") || strings.Contains(rec.Body.String(), store.URL) {
			t.Fatal("readiness leaked credentials or endpoint")
		}
	}
	assertStatus(http.StatusOK, "")
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			storeStatus.Store(int32(code))
			assertStatus(http.StatusServiceUnavailable, "objectstore.secure")
			storeStatus.Store(http.StatusOK)
			assertStatus(http.StatusOK, "")
		})
	}
	scannerDown.Store(true)
	assertStatus(http.StatusServiceUnavailable, "clamd")
	scannerDown.Store(false)
	assertStatus(http.StatusOK, "")
	store.Close()
	assertStatus(http.StatusServiceUnavailable, "objectstore")
	rec := httptest.NewRecorder()
	health.LiveHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if rec.Code != http.StatusOK {
		t.Fatal("dependency failure changed liveness")
	}
}
