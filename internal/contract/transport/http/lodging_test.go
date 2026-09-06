package contracthttp_test

import (
	"net/http"
	"strings"
	"testing"
)

// lodgingPerms is what writing a version's lodging terms actually takes: contract.manage to
// reach the draft plus the new contract.lodging_terms.manage of migration 000039.
const lodgingPerms = managePerms + ",contract.lodging_terms.manage"

const nightsBody = `{"freeCancellationHoursBefore":48,"penaltyKind":"NIGHTS","penaltyNights":1,` +
	`"noShowPercent":"100","minNights":1}`

// TestLodgingTermsRoundTripOverHTTP is the wire contract: the terms go out and come back as
// the same exact decimals, under the contract version's ETag, and a version that has none
// answers a 404 with a Turkish problem rather than an empty body.
func TestLodgingTermsRoundTripOverHTTP(t *testing.T) {
	s := newServer(t)
	versionID, versionETag := s.draftSheet(t, "HTTP_LODGE")
	path := "/api/v1/contract-versions/" + versionID + "/lodging-terms"

	// Before anything is agreed: 404 with a code a screen can branch on.
	res := s.do(t, http.MethodGet, path, readOnly, "", nil)
	if res.code != http.StatusNotFound || problemCode(t, res) != "LODGING_TERMS_NOT_FOUND" {
		t.Fatalf("terms before they are written: %d %v", res.code, res.body)
	}
	title, _ := res.body["title"].(string)
	if !strings.ContainsAny(title, "şğüıöçİŞĞÜÖÇ") {
		t.Fatalf("problem title %q is not Turkish", title)
	}

	body := `{"freeCancellationHoursBefore":48,"penaltyKind":"PERCENT","penaltyPercent":"42.5",` +
		`"noShowPercent":"87.25","holdMinutes":30,"minNights":2,"maxNights":14,"childFreeUnderAge":6}`
	res = s.do(t, http.MethodPut, path, lodgingPerms, body, ifMatch(versionETag))
	if res.code != http.StatusOK {
		t.Fatalf("put lodging terms: %d %v", res.code, res.body)
	}
	if res.etag == "" || res.etag == versionETag {
		t.Fatalf("ETag after the write = %q, was %q; writing a child must move the version", res.etag, versionETag)
	}

	res = s.do(t, http.MethodGet, path, readOnly, "", nil)
	if res.code != http.StatusOK {
		t.Fatalf("get lodging terms: %d %v", res.code, res.body)
	}
	// Exact decimals as strings, on the wire and nowhere near a float.
	if got, _ := res.body["penaltyPercent"].(string); got != "42.5" {
		t.Errorf("penaltyPercent = %v, want the string 42.5", res.body["penaltyPercent"])
	}
	if got, _ := res.body["noShowPercent"].(string); got != "87.25" {
		t.Errorf("noShowPercent = %v, want the string 87.25", res.body["noShowPercent"])
	}
	if got, _ := res.body["penaltyKind"].(string); got != "PERCENT" {
		t.Errorf("penaltyKind = %v", res.body["penaltyKind"])
	}
	if _, present := res.body["penaltyNights"]; present && res.body["penaltyNights"] != nil {
		t.Errorf("a PERCENT policy carries penaltyNights = %v", res.body["penaltyNights"])
	}

	// The snapshot the booking module freezes: the same policy plus the version, the moment
	// and the zone.
	res = s.do(t, http.MethodGet, "/api/v1/contract-versions/"+versionID+"/lodging-policy", readOnly, "", nil)
	if res.code != http.StatusOK {
		t.Fatalf("get lodging policy: %d %v", res.code, res.body)
	}
	if got, _ := res.body["contractVersionId"].(string); got != versionID {
		t.Errorf("snapshot names version %v, want %s", res.body["contractVersionId"], versionID)
	}
	for _, field := range []string{"snapshotAt", "timezone", "noShowPercent", "penaltyKind"} {
		if v, _ := res.body[field].(string); v == "" {
			t.Errorf("the snapshot carries no %s: %v", field, res.body)
		}
	}
}

// TestLodgingTermsNeedTheirOwnPermission is the two-halves permission in the place a caller
// meets it. contract.manage reaches the draft and is not enough on its own; that is the
// whole reason the code exists as a separate grant.
func TestLodgingTermsNeedTheirOwnPermission(t *testing.T) {
	s := newServer(t)
	versionID, versionETag := s.draftSheet(t, "HTTP_LODGE_PERM")
	path := "/api/v1/contract-versions/" + versionID + "/lodging-terms"

	res := s.do(t, http.MethodPut, path, managePerms, nightsBody, ifMatch(versionETag))
	if res.code != http.StatusForbidden || problemCode(t, res) != "PERMISSION_DENIED" {
		t.Fatalf("put with contract.manage alone: %d %v", res.code, res.body)
	}
	// The denial names the permission that was missing, so an access review can see it.
	if len(s.denied.permissions) == 0 ||
		s.denied.permissions[len(s.denied.permissions)-1] != "contract.lodging_terms.manage" {
		t.Fatalf("the denial recorded %v", s.denied.permissions)
	}

	// Reading is contract.read, as the payment term is: what a cancellation costs is a term
	// everybody downstream is bound by.
	res = s.do(t, http.MethodPut, path, lodgingPerms, nightsBody, ifMatch(versionETag))
	if res.code != http.StatusOK {
		t.Fatalf("put with the lodging permission: %d %v", res.code, res.body)
	}
	if res := s.do(t, http.MethodGet, path, readOnly, "", nil); res.code != http.StatusOK {
		t.Fatalf("get with contract.read: %d %v", res.code, res.body)
	}
	// And a caller with neither reads nothing.
	if res := s.do(t, http.MethodGet, path, "", "", nil); res.code != http.StatusForbidden {
		t.Fatalf("get with no permissions: %d %v", res.code, res.body)
	}
}

// TestLodgingTermsRefuseABadSubmissionOverHTTP keeps the validator wired to the wire: an
// impossible policy comes back as field errors naming the field, not as a 500 carrying a
// constraint name.
func TestLodgingTermsRefuseABadSubmissionOverHTTP(t *testing.T) {
	s := newServer(t)
	versionID, versionETag := s.draftSheet(t, "HTTP_LODGE_BAD")
	path := "/api/v1/contract-versions/" + versionID + "/lodging-terms"

	both := `{"freeCancellationHoursBefore":48,"penaltyKind":"NIGHTS","penaltyNights":1,` +
		`"penaltyPercent":"10","noShowPercent":"100","minNights":1}`
	res := s.do(t, http.MethodPut, path, lodgingPerms, both, ifMatch(versionETag))
	if res.code != http.StatusUnprocessableEntity {
		t.Fatalf("two penalties: %d %v", res.code, res.body)
	}
	if field, code := firstFieldError(t, res); field != "penaltyPercent" || code != "FORBIDDEN" {
		t.Fatalf("field error = %s/%s, want penaltyPercent/FORBIDDEN", field, code)
	}

	// A missing If-Match is 428, as it is for every other child of a version.
	if res := s.do(t, http.MethodPut, path, lodgingPerms, nightsBody, nil); res.code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d %v", res.code, res.body)
	}
	// A stale one is 412.
	if res := s.do(t, http.MethodPut, path, lodgingPerms, nightsBody, ifMatch("99")); res.code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d %v", res.code, res.body)
	}
	// And nothing was written on the way through any of those refusals.
	if res := s.do(t, http.MethodGet, path, readOnly, "", nil); res.code != http.StatusNotFound {
		t.Fatalf("a refused write left a row: %d %v", res.code, res.body)
	}
}
