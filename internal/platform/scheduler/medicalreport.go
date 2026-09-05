package scheduler

import (
	"context"
	"time"

	healthapp "github.com/celikbros/kapsora/internal/health/application"
)

// MedicalReportExpire moves every approved treatment report past its `valid_to` to EXPIRED
// (WP-I5-02 section 2.2). A report is valid to the end of its last day, so the sweep looks
// for a `valid_to` strictly before today.
//
// Running it twice expires once, and that is a property of the two statements rather than of
// a flag: the listing only looks at APPROVED rows and the update names APPROVED in its own
// predicate, so a second pass lists nothing the first one finished. Hourly rather than by the
// minute because the unit is a day, and a report that expires at 00:47 rather than 00:00 is a
// report nobody could have used in between.
func MedicalReportExpire(svc *healthapp.Service) Job {
	return Job{
		Code:  "medical_report.expire",
		Every: time.Hour,
		Run: func(ctx context.Context) (Metrics, error) {
			expired, err := svc.ExpireReports(ctx, time.Now().UTC())
			return Metrics{"expired": expired}, err
		},
	}
}
