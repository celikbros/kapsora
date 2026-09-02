// Package idempotency implements the Idempotency-Key contract for command endpoints
// (v1.2 sections 11.13 and 14.6, ADR-015): the same key with the same payload returns
// the stored response, a different payload is rejected, and concurrent duplicates are
// told to wait. Records live in system.idempotency_record inside the tenant's RLS scope.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Header names.
const (
	KeyHeader      = "Idempotency-Key"
	ReplayedHeader = "Idempotent-Replayed"
)

// Scope identifies who executes the command; records are unique per scope + command + key.
type Scope struct {
	TenantID uuid.UUID
	ActorID  uuid.UUID
}

// ScopeFunc resolves the scope from the request; the integrator wires it to
// identity.FromContext. ok=false means the request is not authenticated.
type ScopeFunc func(r *http.Request) (Scope, bool)

// Options configures one command endpoint.
type Options struct {
	CommandCode     string        // required, e.g. "organization.create"
	Scope           ScopeFunc     // required
	Optional        bool          // query-style POSTs: honour the key when present
	TTL             time.Duration // record retention; default 24h
	MaxBodyBytes    int64         // largest response body stored for replay; default 256 KiB
	MaxRequestBytes int64         // largest request body hashed; default 1 MiB
	StaleInProgress time.Duration // abandoned IN_PROGRESS records are replaced after this; default 5m
	Logger          *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.TTL <= 0 {
		o.TTL = 24 * time.Hour
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = 256 << 10
	}
	if o.MaxRequestBytes <= 0 {
		o.MaxRequestBytes = 1 << 20
	}
	if o.StaleInProgress <= 0 {
		o.StaleInProgress = 5 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o
}

// storedResponse is the envelope kept in response_body. The body is kept as raw bytes
// (base64 in JSON) so a replay is byte-identical, whatever the content type.
type storedResponse struct {
	Status      int               `json:"status"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        []byte            `json:"body,omitempty"`
	BodyOmitted bool              `json:"bodyOmitted,omitempty"`
}

var replayedHeaders = []string{"Content-Type", "ETag", "Location", "Content-Language"}

// Middleware wraps a command handler. Panics in the handler release the record and
// propagate to the outer recoverer.
func Middleware(pool *pgxpool.Pool, opts Options) func(http.Handler) http.Handler {
	if opts.CommandCode == "" || opts.Scope == nil {
		panic("idempotency: CommandCode and Scope are required")
	}
	opts = opts.withDefaults()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimSpace(r.Header.Get(KeyHeader))
			if key == "" {
				if opts.Optional {
					next.ServeHTTP(w, r)
					return
				}
				writeProblem(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key başlığı zorunludur")
				return
			}
			if n := len(key); n < 16 || n > 128 {
				writeProblem(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_INVALID", "Idempotency-Key 16-128 karakter olmalıdır")
				return
			}
			scope, ok := opts.Scope(r)
			if !ok {
				writeProblem(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "Kimlik doğrulaması gerekli")
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, opts.MaxRequestBytes+1))
			if err != nil {
				writeProblem(w, r, http.StatusBadRequest, "REQUEST_BODY_UNREADABLE", "İstek gövdesi okunamadı")
				return
			}
			if int64(len(body)) > opts.MaxRequestBytes {
				writeProblem(w, r, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "İstek gövdesi çok büyük")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			hash := requestHash(r, scope, body)

			ctx := r.Context()
			tc := db.TenantContext{TenantID: scope.TenantID, ActorID: scope.ActorID}

			// Phase A: claim the key or learn what happened before.
			var (
				recordID uuid.UUID
				replay   *storedResponse
				outcome  string
			)
			err = db.WithTenantTx(ctx, pool, tc, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				recordID, replay, outcome, err = claim(ctx, tx, scope, opts, key, hash)
				return err
			})
			if err != nil {
				opts.Logger.Error("idempotency claim failed", "command", opts.CommandCode, "error", err)
				writeProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Beklenmeyen hata")
				return
			}
			switch outcome {
			case outcomeReused:
				writeProblem(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "Aynı Idempotency-Key farklı bir istek gövdesiyle kullanıldı")
				return
			case outcomeInProgress:
				w.Header().Set("Retry-After", "2")
				writeProblem(w, r, http.StatusConflict, "IDEMPOTENCY_IN_PROGRESS", "Aynı komut hâlâ işleniyor; kısa süre sonra tekrar deneyin")
				return
			case outcomeReplay:
				writeReplay(w, replay)
				return
			}

			// Execute exactly once, then store the outcome.
			rec := newRecorder(w, opts.MaxBodyBytes)
			completed := false
			defer func() {
				if !completed {
					// Panic or early return without completion: free the key for a retry.
					releaseRecord(pool, tc, recordID, opts.Logger)
				}
			}()
			next.ServeHTTP(rec, r)
			if !rec.wroteHeader {
				rec.WriteHeader(http.StatusOK)
			}
			completed = true
			complete(pool, tc, recordID, rec, opts)
		})
	}
}

const (
	outcomeExecute    = "EXECUTE"
	outcomeReplay     = "REPLAY"
	outcomeReused     = "REUSED"
	outcomeInProgress = "IN_PROGRESS"
)

func claim(ctx context.Context, tx pgx.Tx, scope Scope, opts Options, key string, hash []byte) (uuid.UUID, *storedResponse, string, error) {
	q := sqlcgen.New(tx)
	params := sqlcgen.TryInsertIdempotencyRecordParams{
		TenantID:       scope.TenantID,
		ActorID:        scope.ActorID,
		CommandCode:    opts.CommandCode,
		IdempotencyKey: key,
		RequestHash:    hash,
		ExpiresAt:      time.Now().Add(opts.TTL),
	}
	id, err := q.TryInsertIdempotencyRecord(ctx, params)
	if err == nil {
		return id, nil, outcomeExecute, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil, "", fmt.Errorf("insert record: %w", err)
	}

	existing, err := q.GetIdempotencyRecord(ctx, sqlcgen.GetIdempotencyRecordParams{
		TenantID: scope.TenantID, ActorID: scope.ActorID, CommandCode: opts.CommandCode, IdempotencyKey: key,
	})
	if err != nil {
		return uuid.Nil, nil, "", fmt.Errorf("read record: %w", err)
	}
	if !bytes.Equal(existing.RequestHash, hash) {
		return uuid.Nil, nil, outcomeReused, nil
	}
	switch existing.Status {
	case "IN_PROGRESS":
		if time.Since(existing.CreatedAt) > opts.StaleInProgress {
			// Abandoned by a crashed process: take it over.
			if err := q.DeleteIdempotencyRecord(ctx, existing.ID); err != nil {
				return uuid.Nil, nil, "", fmt.Errorf("delete stale record: %w", err)
			}
			id, err := q.TryInsertIdempotencyRecord(ctx, params)
			if err != nil {
				return uuid.Nil, nil, "", fmt.Errorf("re-insert record: %w", err)
			}
			return id, nil, outcomeExecute, nil
		}
		return uuid.Nil, nil, outcomeInProgress, nil
	default:
		var stored storedResponse
		if err := json.Unmarshal(existing.ResponseBody, &stored); err != nil {
			return uuid.Nil, nil, "", fmt.Errorf("decode stored response: %w", err)
		}
		return uuid.Nil, &stored, outcomeReplay, nil
	}
}

func complete(pool *pgxpool.Pool, tc db.TenantContext, recordID uuid.UUID, rec *recorder, opts Options) {
	ctx := context.Background()
	if rec.status >= 500 {
		// Server-side failure: do not pin it; the client may retry the same key.
		releaseRecord(pool, tc, recordID, opts.Logger)
		return
	}
	stored := storedResponse{Status: rec.status, Headers: map[string]string{}}
	for _, h := range replayedHeaders {
		if v := rec.Header().Get(h); v != "" {
			stored.Headers[h] = v
		}
	}
	if rec.overflow {
		stored.BodyOmitted = true
	} else if rec.body.Len() > 0 {
		stored.Body = append([]byte(nil), rec.body.Bytes()...)
	}
	envelope, err := json.Marshal(stored)
	if err != nil {
		opts.Logger.Error("idempotency: encode stored response", "error", err)
		releaseRecord(pool, tc, recordID, opts.Logger)
		return
	}
	status := "SUCCEEDED"
	if rec.status >= 400 {
		status = "FAILED"
	}
	resourceType, resourceID := resourceFromLocation(rec.Header().Get("Location"))
	if rec.status < 100 || rec.status > 599 {
		rec.status = http.StatusInternalServerError
	}
	responseStatus := int16(rec.status) //nolint:gosec // bounded to 100..599 above

	err = db.WithTenantTx(ctx, pool, tc, func(ctx context.Context, tx pgx.Tx) error {
		return sqlcgen.New(tx).CompleteIdempotencyRecord(ctx, sqlcgen.CompleteIdempotencyRecordParams{
			ID:             recordID,
			Status:         status,
			ResponseStatus: &responseStatus,
			ResponseBody:   envelope,
			ResourceType:   resourceType,
			ResourceID:     resourceID,
		})
	})
	if err != nil {
		opts.Logger.Error("idempotency: complete record failed", "command", opts.CommandCode, "error", err)
	}
}

func releaseRecord(pool *pgxpool.Pool, tc db.TenantContext, recordID uuid.UUID, logger *slog.Logger) {
	if recordID == uuid.Nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := db.WithTenantTx(ctx, pool, tc, func(ctx context.Context, tx pgx.Tx) error {
		return sqlcgen.New(tx).DeleteIdempotencyRecord(ctx, recordID)
	})
	if err != nil {
		logger.Error("idempotency: release record failed", "error", err)
	}
}

// PurgeExpired deletes records past their TTL; the scheduler job idempotency.purge calls it.
// It runs as the owner-independent application role: records of every tenant are visible
// only through RLS, so the purge must bypass tenant scoping. It therefore uses a plain
// connection and relies on the DELETE touching only rows the role can see; in production
// the purge job runs with a maintenance context that sets no tenant, which under RLS sees
// nothing. To keep the purge effective, callers pass tenant ids explicitly.
func PurgeExpired(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, before time.Time) (int64, error) {
	var n int64
	err := db.WithTenantTx(ctx, pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		n, err = sqlcgen.New(tx).PurgeExpiredIdempotencyRecords(ctx, before)
		return err
	})
	return n, err
}

func requestHash(r *http.Request, scope Scope, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.Path))
	h.Write([]byte{0})
	h.Write([]byte(scope.TenantID.String()))
	h.Write([]byte{0})
	h.Write(canonicalBody(body))
	return h.Sum(nil)
}

// canonicalBody re-encodes JSON with sorted object keys so key order never changes the hash.
func canonicalBody(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return trimmed
	}
	out, err := json.Marshal(v) // encoding/json sorts map keys
	if err != nil {
		return trimmed
	}
	return out
}

func resourceFromLocation(location string) (*string, uuid.NullUUID) {
	if location == "" {
		return nil, uuid.NullUUID{}
	}
	id, err := uuid.Parse(path.Base(location))
	if err != nil {
		return nil, uuid.NullUUID{}
	}
	rt := path.Base(path.Dir(location))
	return &rt, uuid.NullUUID{UUID: id, Valid: true}
}

func writeReplay(w http.ResponseWriter, stored *storedResponse) {
	for k, v := range stored.Headers {
		w.Header().Set(k, v)
	}
	w.Header().Set(ReplayedHeader, "true")
	if stored.BodyOmitted {
		if loc := stored.Headers["Location"]; loc != "" {
			w.Header().Set("Location", loc)
			w.WriteHeader(http.StatusSeeOther)
			return
		}
		w.WriteHeader(stored.Status)
		return
	}
	if len(stored.Body) > 0 {
		w.Header().Set("Content-Length", strconv.Itoa(len(stored.Body)))
	}
	w.WriteHeader(stored.Status)
	_, _ = w.Write(stored.Body)
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, title string) {
	httpx.WriteProblem(w, r, httpx.Problem{
		Type:   httpx.ProblemTypeBase + "platform/" + strings.ToLower(strings.ReplaceAll(code, "_", "-")),
		Title:  title,
		Status: status,
		Code:   code,
	})
}

// recorder writes through to the client while buffering up to max bytes for replay.
type recorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
	max         int64
	overflow    bool
}

func newRecorder(w http.ResponseWriter, maxBytes int64) *recorder {
	return &recorder{ResponseWriter: w, status: http.StatusOK, max: maxBytes}
}

func (r *recorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if !r.overflow {
		if int64(r.body.Len()+len(b)) > r.max {
			r.overflow = true
			r.body.Reset()
		} else {
			r.body.Write(b)
		}
	}
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
