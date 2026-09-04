package scheduler

import (
	"context"
	"time"

	documentapp "github.com/celikbros/kapsora/internal/document/application"
)

// DocumentRetention removes the bytes of stored documents older than the retention period
// (WP-I4-04 section 2.3). It skips every document a legal hold covers and reports how many
// it left alone, because "nothing was purged" and "everything was held" are different
// answers an operator has to be able to tell apart.
//
// The sweep is idempotent: purging sets purged_at rather than deleting a row, so a second
// run — or a run after a crash halfway through — finds nothing left to do rather than
// deleting a key twice. The row outlives the bytes on purpose: "this document existed and
// was removed on this day" is an answer a regulator can be given, and an empty table is not.
func DocumentRetention(svc *documentapp.Service, retention time.Duration) Job {
	return Job{
		Code:  "document.retention",
		Every: time.Hour,
		Run: func(ctx context.Context) (Metrics, error) {
			report, err := svc.PurgeExpired(ctx, time.Now().UTC().Add(-retention), 200)
			return Metrics{"purged": report.Purged, "held": report.Held}, err
		},
	}
}
