package antivirus_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/platform/antivirus"
)

// EICAR is the industry standard harmless file every scanner recognises. It is assembled
// from two halves so this source file is not itself quarantined by whatever scans the
// developer's disk.
var eicar = []byte(`X5O!P%@AP[4\PZX54(P^)7CC)7}$` + `EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`)

// liveClamd returns a client against the clamd the local runbook starts natively
// (ADR-021: no container), or skips.
func liveClamd(t *testing.T) *antivirus.Clamd {
	t.Helper()
	addr := os.Getenv("KAPSORA_CLAMAV_ADDR")
	if strings.TrimSpace(addr) == "" {
		addr = "127.0.0.1:3310"
	}
	scanner, err := antivirus.NewClamd(antivirus.ClamdOptions{Address: addr, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("new clamd client: %v", err)
	}
	if err := scanner.Ping(t.Context()); err != nil {
		t.Skipf("clamd on %s is not answering (scripts/native/up.*): %v", addr, err)
	}
	return scanner
}

// TestClamdFindsEicarAndClearsCleanBytes is the only thing this client has to get right:
// the two verdicts, from the real daemon, told apart.
func TestClamdFindsEicarAndClearsCleanBytes(t *testing.T) {
	scanner := liveClamd(t)

	clean, err := scanner.Scan(t.Context(), bytes.NewReader([]byte("bu dosyada hiçbir şey yok")))
	if err != nil {
		t.Fatalf("scan clean bytes: %v", err)
	}
	if clean.Outcome != antivirus.OutcomeClean {
		t.Fatalf("clean bytes scanned as %s (%q)", clean.Outcome, clean.Finding)
	}
	if clean.Finding != "" {
		t.Fatalf("a clean verdict named a finding: %q", clean.Finding)
	}
	if !strings.Contains(strings.ToLower(clean.Engine), "clam") {
		t.Fatalf("engine = %q, want the daemon's own name", clean.Engine)
	}
	if clean.SignatureVersion == "" {
		t.Fatalf("no signature version recorded; a clean verdict says nothing without one")
	}

	infected, err := scanner.Scan(t.Context(), bytes.NewReader(eicar))
	if err != nil {
		t.Fatalf("scan eicar: %v", err)
	}
	if infected.Outcome != antivirus.OutcomeInfected {
		t.Fatalf("EICAR scanned as %s, want INFECTED", infected.Outcome)
	}
	if !strings.Contains(strings.ToLower(infected.Finding), "eicar") {
		t.Fatalf("finding = %q, want it to name EICAR", infected.Finding)
	}
}

// TestClamdStreamsTheWholeBodyInChunks decodes what the daemon actually received. A file
// larger than one chunk is the case where a length-prefixed protocol goes wrong quietly:
// the daemon answers OK about the bytes it got, and a body that arrived truncated would be
// reported as a clean file. This asserts the bytes rather than the verdict.
//
// It is checked against a stand-in rather than clamd because clamd will not say what it
// read; the live daemon is exercised by the EICAR test above.
func TestClamdStreamsTheWholeBodyInChunks(t *testing.T) {
	// Room for the VERSION conversation the client has first as well as the scan itself:
	// a stand-in that blocked on a full channel would never answer the scan.
	received := make(chan []byte, 4)
	addr := serveOneReply(t, "stream: OK\x00", received)
	scanner, err := antivirus.NewClamd(antivirus.ClamdOptions{Address: addr, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("new clamd client: %v", err)
	}

	// Deliberately not a multiple of the 64 KiB chunk, so the last chunk is a short one.
	body := bytes.Repeat([]byte("kapsora "), 40_000)
	result, err := scanner.Scan(t.Context(), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if result.Outcome != antivirus.OutcomeClean {
		t.Fatalf("outcome = %s, want CLEAN", result.Outcome)
	}

	var raw []byte
	deadline := time.After(5 * time.Second)
	for raw == nil {
		select {
		case candidate := <-received:
			if bytes.HasPrefix(candidate, []byte("zINSTREAM\x00")) {
				raw = candidate
			}
		case <-deadline:
			t.Fatal("the stand-in daemon received no INSTREAM")
		}
	}
	_, streamed := decodeInstream(t, raw)
	if !bytes.Equal(streamed, body) {
		t.Fatalf("the daemon received %d bytes, the file has %d", len(streamed), len(body))
	}
}

// decodeInstream splits what the stand-in received into the command and the reassembled
// body, checking the terminator that tells clamd the file has ended.
func decodeInstream(t *testing.T, raw []byte) (command string, body []byte) {
	t.Helper()
	end := bytes.IndexByte(raw, 0)
	if end < 0 {
		t.Fatalf("no NUL-terminated command in %d bytes", len(raw))
	}
	command = string(raw[:end+1])
	rest := raw[end+1:]
	for {
		if len(rest) < 4 {
			t.Fatalf("stream ended without a terminator, %d bytes left", len(rest))
		}
		size := binary.BigEndian.Uint32(rest[:4])
		rest = rest[4:]
		if size == 0 {
			if len(rest) != 0 {
				t.Fatalf("%d bytes follow the stream terminator", len(rest))
			}
			return command, body
		}
		if uint32(len(rest)) < size {
			t.Fatalf("chunk declares %d bytes but only %d follow", size, len(rest))
		}
		body = append(body, rest[:size]...)
		rest = rest[size:]
	}
}

// TestClamdReportsAnUnreachableDaemon is what the pipeline relies on to leave a file
// FAILED rather than promoting it: a scanner that cannot be reached is never a clean
// verdict.
func TestClamdReportsAnUnreachableDaemon(t *testing.T) {
	// A listener taken and immediately closed leaves a port nothing is listening on.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	scanner, err := antivirus.NewClamd(antivirus.ClamdOptions{Address: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("new clamd client: %v", err)
	}
	result, err := scanner.Scan(t.Context(), bytes.NewReader(eicar))
	if !errors.Is(err, antivirus.ErrUnavailable) {
		t.Fatalf("scan against a dead port = %v, want ErrUnavailable", err)
	}
	if result.Outcome != antivirus.OutcomeError {
		t.Fatalf("outcome = %s, want ERROR", result.Outcome)
	}
	if err := scanner.Ping(t.Context()); !errors.Is(err, antivirus.ErrUnavailable) {
		t.Fatalf("ping against a dead port = %v, want ErrUnavailable", err)
	}
}

// TestClamdTreatsAnUnreadableReplyAsAnError covers the daemon that answers something this
// client cannot read. Guessing "probably clean" there is exactly the mistake the whole
// package exists to avoid.
func TestClamdTreatsAnUnreadableReplyAsAnError(t *testing.T) {
	addr := serveOneReply(t, "stream: something went wrong ERROR\x00", nil)
	scanner, err := antivirus.NewClamd(antivirus.ClamdOptions{Address: addr, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("new clamd client: %v", err)
	}
	result, err := scanner.Scan(t.Context(), bytes.NewReader([]byte("body")))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if result.Outcome != antivirus.OutcomeError {
		t.Fatalf("outcome = %s, want ERROR", result.Outcome)
	}
	if !strings.Contains(result.Finding, "something went wrong") {
		t.Fatalf("finding = %q, want the daemon's own words", result.Finding)
	}
}

// serveOneReply stands in for clamd: it reads whatever is sent until the client stops
// writing, answers a fixed line, and, when received is not nil, hands the raw bytes back
// so a test can decode what the daemon would have seen.
func serveOneReply(t *testing.T, reply string, received chan<- []byte) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				// The client writes its command and the whole body, then waits for the
				// reply; reading until the write side goes quiet is how the stand-in knows
				// the file has ended without speaking the protocol itself.
				raw := readUntilIdle(conn)
				if received != nil {
					received <- raw
				}
				_, _ = conn.Write([]byte(reply))
			}()
		}
	}()
	return listener.Addr().String()
}

// readUntilIdle reads until 200 ms pass with nothing arriving. The client keeps the
// connection open waiting for a verdict, so end-of-file never comes.
func readUntilIdle(conn net.Conn) []byte {
	var raw []byte
	buf := make([]byte, 32<<10)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			return raw
		}
		n, err := conn.Read(buf)
		raw = append(raw, buf[:n]...)
		if err != nil {
			return raw
		}
	}
}
