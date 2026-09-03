// The service is exercised against the real PostgreSQL repositories: the lockout counter
// and the session lifetime live half in Go and half in SQL, and a fake repository would
// not test the half that matters.
package application_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

const (
	testPassword = "çilekli düğün pastası 2026"
	newPassword  = "başka bir uzun parola 42"
)

type fixture struct {
	h       *dbtest.Harness
	svc     *application.Service
	clock   *testClock
	events  *eventLog
	actorID uuid.UUID
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type eventLog struct {
	mu     sync.Mutex
	events []audit.Event
}

func (l *eventLog) sink(_ context.Context, ev audit.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
	return nil
}

func (l *eventLog) codes() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.events))
	for _, ev := range l.events {
		out = append(out, string(ev.Outcome)+":"+ev.ActionCode+":"+ev.ReasonCode)
	}
	return out
}

func newFixture(t *testing.T, username string) *fixture {
	t.Helper()
	h := dbtest.New(t)
	clock := &testClock{now: time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)}
	events := &eventLog{}

	svc, err := application.New(application.Deps{
		Credentials: identitypg.NewCredentialRepository(h.App),
		Sessions:    identitypg.NewSessionStore(h.App),
		Audit:       events.sink,
		Policy:      domain.DefaultPolicy(),
		Lockout:     domain.DefaultLockout(),
		Now:         clock.Now,
	})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	actorID, err := svc.CreateAccount(context.Background(), username, "Test Kullanıcı", username+"@example.test", testPassword, false)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return &fixture{h: h, svc: svc, clock: clock, events: events, actorID: actorID}
}

func (f *fixture) login(t *testing.T, username, password string) (application.SessionView, error) {
	t.Helper()
	return f.svc.Login(context.Background(), application.LoginInput{Username: username, Password: password})
}

func TestLoginSucceedsAndIsCaseInsensitive(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")

	result, err := f.login(t, "  AHMET@Example.TEST ", testPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if result.Session.ID == "" || result.Session.ActorID != f.actorID {
		t.Fatalf("unexpected session: %+v", result.Session)
	}
	if result.DisplayName != "Test Kullanıcı" {
		t.Fatalf("display name = %q", result.DisplayName)
	}
	if !result.Session.ExpiresAt.Equal(f.clock.Now().Add(8 * time.Hour)) {
		t.Fatalf("absolute expiry = %v", result.Session.ExpiresAt)
	}

	loaded, err := f.svc.LoadSession(context.Background(), result.Session.ID)
	if err != nil || loaded.ActorID != f.actorID {
		t.Fatalf("load session: %+v err=%v", loaded, err)
	}
}

func TestUnknownUserAndWrongPasswordAreIndistinguishable(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")

	_, errUnknown := f.login(t, "kimse@example.test", testPassword)
	_, errWrong := f.login(t, "ahmet@example.test", "tamamen başka bir parola")

	if !errors.Is(errUnknown, application.ErrInvalidCredentials) || !errors.Is(errWrong, application.ErrInvalidCredentials) {
		t.Fatalf("errors differ: unknown=%v wrong=%v", errUnknown, errWrong)
	}
	if errUnknown.Error() != errWrong.Error() {
		t.Fatalf("messages differ: %q vs %q", errUnknown, errWrong)
	}

	codes := f.events.codes()
	if !contains(codes, "FAILURE:session.login:UNKNOWN_USER") || !contains(codes, "FAILURE:session.login:BAD_PASSWORD") {
		t.Fatalf("audit events missing: %v", codes)
	}
}

func TestLockoutAfterRepeatedFailuresAndResetOnSuccess(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")
	lock := domain.DefaultLockout()

	for i := 1; i < lock.MaxFailedAttempts; i++ {
		if _, err := f.login(t, "ahmet@example.test", "yanlış parola dene"); !errors.Is(err, application.ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// The correct password still works while the counter is below the threshold.
	if _, err := f.login(t, "ahmet@example.test", testPassword); err != nil {
		t.Fatalf("login below threshold: %v", err)
	}

	for i := 0; i < lock.MaxFailedAttempts; i++ {
		if _, err := f.login(t, "ahmet@example.test", "yanlış parola dene"); !errors.Is(err, application.ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// Now even the correct password is refused, and the reason differs from a bad password.
	if _, err := f.login(t, "ahmet@example.test", testPassword); !errors.Is(err, application.ErrAccountLocked) {
		t.Fatalf("expected a locked account, got %v", err)
	}

	// The lock lifts on its own once the window passes.
	f.clock.Advance(lock.Duration + time.Minute)
	// The service compares locked_until with its (fake) clock, so the expiry must be expressed
	// in that clock, not the database's; otherwise the test depends on the time of day.
	f.h.AdminExec(`UPDATE iam.credential SET locked_until = $2 WHERE actor_id = $1`, f.actorID, f.clock.Now().Add(-time.Minute))
	if _, err := f.login(t, "ahmet@example.test", testPassword); err != nil {
		t.Fatalf("login after the lock expired: %v", err)
	}

	ctx, cancel := f.h.Ctx()
	defer cancel()
	var attempts int
	var lockedUntil *time.Time
	if err := f.h.Admin.QueryRow(ctx, `SELECT failed_attempts, locked_until FROM iam.credential WHERE actor_id = $1`, f.actorID).Scan(&attempts, &lockedUntil); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || lockedUntil != nil {
		t.Fatalf("counters not reset after success: attempts=%d locked_until=%v", attempts, lockedUntil)
	}
}

// A failure counter that is read, incremented and written in Go would lose updates under
// concurrency and let an attacker exceed the threshold. The counter is a single UPDATE, so
// every attempt that reaches it is counted exactly once.
func TestParallelWrongPasswordsAreAllCounted(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")

	const attemptCount = 25
	var (
		mu      sync.Mutex
		counted int // attempts that reached the counter (the rest found the account locked)
		wg      sync.WaitGroup
	)
	for i := 0; i < attemptCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.login(t, "ahmet@example.test", "yanlış parola dene")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, application.ErrInvalidCredentials):
				counted++
			case errors.Is(err, application.ErrAccountLocked):
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	ctx, cancel := f.h.Ctx()
	defer cancel()
	var attempts int
	if err := f.h.Admin.QueryRow(ctx, `SELECT failed_attempts FROM iam.credential WHERE actor_id = $1`, f.actorID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != counted {
		t.Fatalf("failed_attempts = %d but %d attempts reached the counter (lost updates)", attempts, counted)
	}
	if attempts < domain.DefaultLockout().MaxFailedAttempts {
		t.Fatalf("only %d attempts counted; the account should have locked", attempts)
	}
	if _, err := f.login(t, "ahmet@example.test", testPassword); !errors.Is(err, application.ErrAccountLocked) {
		t.Fatalf("account should be locked, got %v", err)
	}
}

func TestSuspendedActorCannotLogInEvenWithTheRightPassword(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")
	f.h.AdminExec(`UPDATE iam.actor SET status = 'SUSPENDED' WHERE id = $1`, f.actorID)

	if _, err := f.login(t, "ahmet@example.test", testPassword); !errors.Is(err, application.ErrActorSuspended) {
		t.Fatalf("expected a suspended actor error, got %v", err)
	}
	if !contains(f.events.codes(), "DENIED:session.login:ACTOR_SUSPENDED") {
		t.Fatalf("audit events: %v", f.events.codes())
	}
}

func TestSessionExpiryByIdleTimeoutAndAbsoluteLimit(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")
	policy := domain.DefaultPolicy()

	idle, err := f.login(t, "ahmet@example.test", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(policy.IdleTimeout + time.Minute)
	if _, err := f.svc.LoadSession(context.Background(), idle.Session.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Fatalf("idle session: %v", err)
	}
	// The row is gone, not merely hidden.
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.session WHERE actor_id = $1`, f.actorID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("idle session was not deleted: %d rows", n)
	}

	long, err := f.login(t, "ahmet@example.test", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	// Stay active, but pass the absolute limit.
	for i := 0; i < 20; i++ {
		f.clock.Advance(25 * time.Minute)
		if _, err := f.svc.LoadSession(context.Background(), long.Session.ID); err != nil {
			if !errors.Is(err, identity.ErrSessionNotFound) {
				t.Fatalf("unexpected error at step %d: %v", i, err)
			}
			if f.clock.Now().Before(long.Session.ExpiresAt) {
				t.Fatalf("session ended before its absolute expiry")
			}
			return
		}
	}
	t.Fatal("session survived past its absolute lifetime")
}

func TestTouchIsThrottled(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")
	result, err := f.login(t, "ahmet@example.test", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	readLastSeen := func() time.Time {
		ctx, cancel := f.h.Ctx()
		defer cancel()
		var ts time.Time
		if err := f.h.Admin.QueryRow(ctx, `SELECT last_seen_at FROM iam.session WHERE actor_id = $1`, f.actorID).Scan(&ts); err != nil {
			t.Fatal(err)
		}
		return ts
	}
	before := readLastSeen()

	f.clock.Advance(10 * time.Second)
	if _, err := f.svc.LoadSession(context.Background(), result.Session.ID); err != nil {
		t.Fatal(err)
	}
	if got := readLastSeen(); !got.Equal(before) {
		t.Fatalf("last_seen_at written within the touch interval: %v -> %v", before, got)
	}

	f.clock.Advance(2 * time.Minute)
	if _, err := f.svc.LoadSession(context.Background(), result.Session.ID); err != nil {
		t.Fatal(err)
	}
	if got := readLastSeen(); !got.After(before) {
		t.Fatalf("last_seen_at not written after the touch interval: %v", got)
	}
}

func TestStepUpRequiresThePasswordAndExpires(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")
	result, err := f.login(t, "ahmet@example.test", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.DefaultPolicy()

	if _, err := f.svc.StepUp(context.Background(), result.Session.ID, "yanlış parola dene"); !errors.Is(err, application.ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	until, err := f.svc.StepUp(context.Background(), result.Session.ID, testPassword)
	if err != nil {
		t.Fatalf("step-up: %v", err)
	}
	if !until.Equal(f.clock.Now().Add(policy.StepUpWindow)) {
		t.Fatalf("step-up window = %v", until)
	}

	session, err := f.svc.LoadSession(context.Background(), result.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.StepUpValid(session, f.clock.Now()) {
		t.Fatal("step-up should be valid right after it was granted")
	}
	f.clock.Advance(policy.StepUpWindow + time.Second)
	session, err = f.svc.LoadSession(context.Background(), result.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if policy.StepUpValid(session, f.clock.Now()) {
		t.Fatal("step-up should have expired")
	}
	if !contains(f.events.codes(), "SUCCESS:session.step_up:") {
		t.Fatalf("audit events: %v", f.events.codes())
	}
}

func TestChangePasswordEndsOtherSessionsAndEnforcesPolicy(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")
	first, err := f.login(t, "ahmet@example.test", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.login(t, "ahmet@example.test", testPassword)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := f.svc.ChangePassword(ctx, second.Session.ID, "yanlış parola dene", newPassword); !errors.Is(err, application.ErrInvalidCredentials) {
		t.Fatalf("wrong current password: %v", err)
	}
	if err := f.svc.ChangePassword(ctx, second.Session.ID, testPassword, "kısa"); !errors.Is(err, domain.ErrPasswordTooShort) {
		t.Fatalf("short new password: %v", err)
	}
	if err := f.svc.ChangePassword(ctx, second.Session.ID, testPassword, newPassword); err != nil {
		t.Fatalf("change password: %v", err)
	}

	// The other session is gone, the caller's own still works.
	if _, err := f.svc.LoadSession(ctx, first.Session.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Fatalf("other session survived: %v", err)
	}
	if _, err := f.svc.LoadSession(ctx, second.Session.ID); err != nil {
		t.Fatalf("caller session was dropped: %v", err)
	}
	// Only the new password works from now on.
	if _, err := f.login(t, "ahmet@example.test", testPassword); !errors.Is(err, application.ErrInvalidCredentials) {
		t.Fatalf("old password still works: %v", err)
	}
	if _, err := f.login(t, "ahmet@example.test", newPassword); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
}

// Describe is what a page load calls; it must report the profile, not just the session.
func TestDescribeReportsProfileAndRejectsASuspendedAccount(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")
	ctx := context.Background()
	result, err := f.login(t, "ahmet@example.test", testPassword)
	if err != nil {
		t.Fatal(err)
	}

	view, err := f.svc.Describe(ctx, result.Session.ID)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if view.DisplayName != "Test Kullanıcı" || view.MustChangePassword {
		t.Fatalf("profile = %+v", view)
	}

	f.h.AdminExec(`UPDATE iam.actor SET status = 'SUSPENDED' WHERE id = $1`, f.actorID)
	if _, err := f.svc.Describe(ctx, result.Session.ID); !errors.Is(err, application.ErrActorSuspended) {
		t.Fatalf("suspended account: %v", err)
	}
	// The session is dropped, not merely refused.
	if _, err := f.svc.LoadSession(ctx, result.Session.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Fatalf("session survived suspension: %v", err)
	}
}

func TestLogoutAndRevokeAllForActor(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")
	ctx := context.Background()

	one, _ := f.login(t, "ahmet@example.test", testPassword)
	two, _ := f.login(t, "ahmet@example.test", testPassword)

	if err := f.svc.Logout(ctx, one.Session.ID); err != nil {
		t.Fatalf("logout: %v", err)
	}
	// Logging out twice is not an error.
	if err := f.svc.Logout(ctx, one.Session.ID); err != nil {
		t.Fatalf("second logout: %v", err)
	}
	if _, err := f.svc.LoadSession(ctx, one.Session.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Fatalf("session survived logout: %v", err)
	}
	if _, err := f.svc.LoadSession(ctx, two.Session.ID); err != nil {
		t.Fatalf("the other session must be untouched: %v", err)
	}

	n, err := f.svc.RevokeAllForActor(ctx, f.actorID)
	if err != nil || n != 1 {
		t.Fatalf("revoke all: n=%d err=%v", n, err)
	}
	if _, err := f.svc.LoadSession(ctx, two.Session.ID); !errors.Is(err, identity.ErrSessionNotFound) {
		t.Fatalf("session survived revoke-all: %v", err)
	}
	if !contains(f.events.codes(), "SUCCESS:session.logout:") {
		t.Fatalf("audit events: %v", f.events.codes())
	}
}

func TestCreateAccountRejectsDuplicatesAndWeakPasswords(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")
	ctx := context.Background()

	if _, err := f.svc.CreateAccount(ctx, "AHMET@example.test", "Kopya", "", testPassword, false); err == nil {
		t.Fatal("duplicate user name accepted")
	}
	if _, err := f.svc.CreateAccount(ctx, "mehmet@example.test", "Mehmet", "", "kısa", false); !errors.Is(err, domain.ErrPasswordTooShort) {
		t.Fatalf("weak password: %v", err)
	}
	if _, err := f.svc.CreateAccount(ctx, "", "Adsız", "", testPassword, false); err == nil {
		t.Fatal("empty user name accepted")
	}
}

func TestStoredRowsNeverContainThePasswordOrTheSessionID(t *testing.T) {
	f := newFixture(t, "ahmet@example.test")
	result, err := f.login(t, "ahmet@example.test", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := f.h.Ctx()
	defer cancel()

	var hash string
	if err := f.h.Admin.QueryRow(ctx, `SELECT password_hash FROM iam.credential WHERE actor_id = $1`, f.actorID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, testPassword) || !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("password hash looks wrong: %s", hash)
	}

	var rowText string
	if err := f.h.Admin.QueryRow(ctx, `SELECT s::text FROM iam.session s WHERE actor_id = $1`, f.actorID).Scan(&rowText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rowText, result.Session.ID) {
		t.Fatal("the session row contains the plaintext session id")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
