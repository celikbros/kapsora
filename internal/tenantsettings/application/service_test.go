package application

import "testing"

func TestCommandValidation(t *testing.T) {
	good := Command{HoldMinutes: 15, QuoteTTLMinutes: 60, ReasonCode: "OPERATING_REQUIREMENT", IfMatch: `"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"`, Key: "settings-command-0001", RawBody: []byte(`{"holdMinutes":15,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"}`)}
	if err := validateCommand(good); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*Command){
		func(c *Command) { c.HoldMinutes = 0 }, func(c *Command) { c.QuoteTTLMinutes = 1441 }, func(c *Command) { c.ReasonCode = "OTHER" },
		func(c *Command) { c.IfMatch = `W/"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"` },
		func(c *Command) {
			c.RawBody = []byte(`{"holdMinutes":16,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT"}`)
		},
		func(c *Command) {
			c.RawBody = []byte(`{"holdMinutes":15,"quoteTtlMinutes":60,"reasonCode":"OPERATING_REQUIREMENT","other":1}`)
		},
	} {
		candidate := good
		edit(&candidate)
		if err := validateCommand(candidate); err == nil {
			t.Errorf("accepted %+v", candidate)
		}
	}
}
