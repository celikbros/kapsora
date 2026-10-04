package idempotency

import (
	"bytes"
	"net/http"
	"testing"
)

func TestIfMatchCanBeBoundToOneCommandHash(t *testing.T) {
	request := func(version string) *http.Request {
		r, err := http.NewRequest(http.MethodPost, "https://example.invalid/api/v1/admin/users/member/suspend", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("If-Match", version)
		return r
	}
	first, second := request(`"1"`), request(`"2"`)
	if !bytes.Equal(requestHash(first, Scope{}, []byte(`{"reasonCode":"ACCESS_REVIEW"}`)), requestHash(second, Scope{}, []byte(`{"reasonCode":"ACCESS_REVIEW"}`))) {
		t.Fatal("default hash changed for commands without HashHeaders")
	}
	if bytes.Equal(requestHashWithHeaders(first, Scope{}, []byte(`{"reasonCode":"ACCESS_REVIEW"}`), []string{"If-Match"}), requestHashWithHeaders(second, Scope{}, []byte(`{"reasonCode":"ACCESS_REVIEW"}`), []string{"If-Match"})) {
		t.Fatal("same key with altered membership ETag would replay")
	}
}
