package antivirus

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Clamd speaks clamd's stream protocol over TCP. ClamAV runs as a native service (ADR-021:
// there is no container in this project), so the address is a plain host:port from the
// environment.
//
// The client is deliberately thin. It opens a connection per scan, writes the body in
// length-prefixed chunks and turns the single line clamd answers into one of three
// outcomes. Everything that decides what to do about that answer lives in the document
// pipeline, which is what lets the pipeline be tested without a virus.
type Clamd struct {
	addr    string
	timeout time.Duration
	dialer  *net.Dialer

	// version is read once and remembered: it is the same for every scan a running daemon
	// performs, and asking for it per file would double the connections for a string.
	versionOnce sync.Once
	engine      string
	signatures  string
}

// ClamdOptions configures the client.
type ClamdOptions struct {
	// Address is host:port of the clamd TCP socket, for example 127.0.0.1:3310.
	Address string
	// Timeout bounds one whole scan, connection included; default 2 minutes. A large file
	// on a busy daemon is slow, and a scan that gives up early would be reported as an
	// error and retried forever.
	Timeout time.Duration
}

const (
	// chunkSize is the body chunk written per length prefix. clamd's own StreamMaxLength
	// applies to the whole stream, not to a chunk; 64 KiB keeps memory flat.
	chunkSize = 64 << 10
	// maxReplyBytes caps the reply read. clamd answers one short line; anything longer is
	// a daemon that is not clamd.
	maxReplyBytes = 4 << 10
)

// NewClamd validates the options and returns the client. It does not connect: a scanner
// that is down at start-up must not stop the worker from starting, because the files it
// cannot scan simply stay in quarantine.
func NewClamd(o ClamdOptions) (*Clamd, error) {
	if strings.TrimSpace(o.Address) == "" {
		return nil, errors.New("antivirus: clamd address is required")
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Minute
	}
	return &Clamd{addr: o.Address, timeout: o.Timeout, dialer: &net.Dialer{Timeout: 10 * time.Second}}, nil
}

var _ Scanner = (*Clamd)(nil)

// Ping implements Scanner.
func (c *Clamd) Ping(ctx context.Context) error {
	reply, err := c.command(ctx, "PING", nil)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(reply, "PONG") {
		return fmt.Errorf("%w: PING answered %q", ErrUnavailable, reply)
	}
	return nil
}

// Scan implements Scanner. The verdict is read from clamd's one-line answer:
//
//	stream: OK                                  → CLEAN
//	stream: Eicar-Test-Signature FOUND          → INFECTED, finding "Eicar-Test-Signature"
//	stream: <something> ERROR                   → ERROR
//
// Anything else is treated as ERROR rather than guessed at. A verdict this client cannot
// read is not a clean file.
func (c *Clamd) Scan(ctx context.Context, body io.Reader) (Result, error) {
	engine, signatures := c.version(ctx)
	result := Result{Engine: engine, SignatureVersion: signatures}

	reply, err := c.command(ctx, "INSTREAM", body)
	if err != nil {
		result.Outcome = OutcomeError
		result.Finding = truncate(err.Error(), 500)
		return result, err
	}

	line := strings.TrimSpace(reply)
	switch {
	case strings.HasSuffix(line, " OK"):
		result.Outcome = OutcomeClean
	case strings.HasSuffix(line, " FOUND"):
		result.Outcome = OutcomeInfected
		result.Finding = findingOf(line)
	default:
		result.Outcome = OutcomeError
		result.Finding = truncate(line, 500)
	}
	return result, nil
}

// version reads the daemon's VERSION line once. A daemon that will not say is not an
// error: the scan still has a verdict, and the columns recording what looked at the file
// are allowed to be empty rather than wrong.
func (c *Clamd) version(ctx context.Context) (engine, signatures string) {
	c.versionOnce.Do(func() {
		reply, err := c.command(ctx, "VERSION", nil)
		if err != nil {
			return
		}
		// "ClamAV 1.5.4/28113/Thu Sep  4 09:11:22 2026"
		line := strings.TrimSpace(reply)
		parts := strings.SplitN(line, "/", 3)
		c.engine = truncate(strings.TrimSpace(parts[0]), 100)
		if len(parts) > 1 {
			c.signatures = truncate(strings.TrimSpace(parts[1]), 100)
		}
	})
	if c.engine == "" {
		return "clamav", c.signatures
	}
	return c.engine, c.signatures
}

// command runs one clamd conversation. Commands are sent in the "z" form — prefixed with
// a NUL-terminating dialect marker — because it is the only form that is unambiguous
// about where a command ends, whatever the file's own bytes contain.
func (c *Clamd) command(ctx context.Context, command string, body io.Reader) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	conn, err := c.dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return "", fmt.Errorf("%w: dial %s: %w", ErrUnavailable, c.addr, err)
	}
	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return "", fmt.Errorf("%w: set deadline: %w", ErrUnavailable, err)
		}
	}
	// The context can be cancelled while a large body is in flight; closing the
	// connection is what makes the blocked Write and Read return.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	writer := bufio.NewWriter(conn)
	if _, err := writer.WriteString("z" + command + "\x00"); err != nil {
		return "", fmt.Errorf("%w: write command %s: %w", ErrUnavailable, command, err)
	}
	if body != nil {
		if err := writeStream(writer, body); err != nil {
			return "", err
		}
	}
	if err := writer.Flush(); err != nil {
		return "", fmt.Errorf("%w: flush command %s: %w", ErrUnavailable, command, err)
	}

	reply, err := io.ReadAll(io.LimitReader(conn, maxReplyBytes))
	if err != nil {
		return "", fmt.Errorf("%w: read reply to %s: %w", ErrUnavailable, command, err)
	}
	if len(reply) == 0 {
		return "", fmt.Errorf("%w: %s got an empty reply", ErrUnavailable, command)
	}
	return strings.TrimRight(string(reply), "\x00\n"), nil
}

// writeStream sends the body as clamd's INSTREAM chunks: a four-byte big-endian length
// then that many bytes, terminated by a zero length.
func writeStream(w *bufio.Writer, body io.Reader) error {
	buf := make([]byte, chunkSize)
	var header [4]byte
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			// n is bounded by len(buf), which is chunkSize, so the conversion cannot wrap.
			binary.BigEndian.PutUint32(header[:], uint32(min(n, chunkSize)))
			if _, err := w.Write(header[:]); err != nil {
				return fmt.Errorf("%w: write chunk header: %w", ErrUnavailable, err)
			}
			if _, err := w.Write(buf[:n]); err != nil {
				return fmt.Errorf("%w: write chunk: %w", ErrUnavailable, err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			// The body could not be read to the end, so there is no verdict to be had.
			// Reporting this as anything but a failure would be reporting a partial scan
			// as a complete one.
			return fmt.Errorf("antivirus: read body: %w", readErr)
		}
	}
	binary.BigEndian.PutUint32(header[:], 0)
	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("%w: write stream terminator: %w", ErrUnavailable, err)
	}
	return nil
}

// findingOf pulls the signature name out of "stream: <name> FOUND".
func findingOf(line string) string {
	trimmed := strings.TrimSuffix(line, " FOUND")
	if _, after, ok := strings.Cut(trimmed, ": "); ok {
		trimmed = after
	}
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return "UNKNOWN"
	}
	return truncate(trimmed, 500)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
