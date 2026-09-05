package scheduler

import (
	"context"
	"time"

	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
)

// AuthorizationExpire releases the holds of authorizations past their end and marks them
// EXPIRED (WP-I4-02 section 2.3). The sweep only looks at authorizations that still hold
// entitlement and every release carries a key derived from the line it releases, so a
// second run — or a run after a crash halfway through — releases nothing twice.
func AuthorizationExpire(svc *authorizationapp.Service) Job {
	return Job{
		Code:  "authorization.expire",
		Every: time.Minute,
		Run: func(ctx context.Context) (Metrics, error) {
			expired, err := svc.ExpireAuthorizations(ctx, time.Now().UTC())
			return Metrics{"expired": expired}, err
		},
	}
}

// AuthorizationExpiring tells a member three days before an authorization runs out
// (WP-I5-05 section 2.4). It publishes outbox rows and sends nothing itself; the
// deduplication key carries the authorization and the day it expires, so an hourly sweep
// of the same day writes one message and twenty-three no-ops.
func AuthorizationExpiring(svc *authorizationapp.Service) Job {
	return Job{
		Code:  "authorization.expiring",
		Every: time.Hour,
		Run: func(ctx context.Context) (Metrics, error) {
			published, err := svc.NotifyExpiringAuthorizations(ctx, time.Now().UTC())
			return Metrics{"published": published}, err
		},
	}
}
