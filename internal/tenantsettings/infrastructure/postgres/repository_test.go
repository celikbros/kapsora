package tenantsettingspg

import (
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/accommodation/settings"
)

func TestProjectionMatchesAccommodationConsumer(t *testing.T) {
	now := time.Now()
	cases := []struct {
		raw    string
		source string
		value  int
	}{
		{`15`, "CONFIGURED", 15}, {`"15"`, "CONFIGURED", 15}, {`"0015"`, "CONFIGURED", 15},
		{`"bad"`, "INVALID_FALLBACK", 15}, {`0`, "INVALID_FALLBACK", 15}, {`1441`, "INVALID_FALLBACK", 15},
	}
	for _, tc := range cases {
		found := map[string]row{settings.KeyHoldMinutes: {key: settings.KeyHoldMinutes, raw: []byte(tc.raw), version: 1, updated: now}}
		field := projection(found, false).HoldMinutes
		if field.Source != tc.source || field.EffectiveValue != tc.value || field.DefaultValue != 15 || field.UpdatedAt == nil {
			t.Errorf("raw=%s got=%+v", tc.raw, field)
		}
		consumer := settings.FromMap(map[string]string{settings.KeyHoldMinutes: unwrap(tc.raw)})
		if consumer.HoldMinutes != field.EffectiveValue {
			t.Errorf("consumer drift raw=%s consumer=%d projection=%d", tc.raw, consumer.HoldMinutes, field.EffectiveValue)
		}
	}
	empty := projection(nil, false)
	if empty.HoldMinutes.Source != "DEFAULT" || empty.HoldMinutes.UpdatedAt != nil || empty.QuoteTTLMinutes.EffectiveValue != 60 {
		t.Fatal(empty)
	}
}
func unwrap(s string) string {
	if len(s) > 1 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func TestAggregateVersionTracksOnlyAdmittedKeys(t *testing.T) {
	base := token("tenant", nil)
	hold := map[string]row{settings.KeyHoldMinutes: {version: 1}}
	if base == token("tenant", hold) {
		t.Fatal("absence omitted from version")
	}
	first := token("tenant", hold)
	hold[settings.KeyHoldMinutes] = row{version: 2}
	if first == token("tenant", hold) {
		t.Fatal("row version omitted")
	}
	prior := token("tenant", hold)
	hold["other.key"] = row{version: 99}
	if token("tenant", hold) != prior {
		t.Fatal("unrelated key changed version")
	}
}
