package identitypg

import "testing"

func TestRoleChangeSnapshotEqualJSONBRepresentation(t *testing.T) {
	expected := []byte(`[{"code":"plan.publish","sensitivity":"PRIVILEGED"},{"code":"program.read","sensitivity":"NORMAL"}]`)
	for _, tc := range []struct {
		name   string
		stored string
		want   bool
	}{
		{"jsonb whitespace and object order", `[{ "sensitivity": "PRIVILEGED", "code": "plan.publish" }, {"sensitivity":"NORMAL", "code":"program.read"}]`, true},
		{"changed permission", `[{"code":"plan.read","sensitivity":"PRIVILEGED"},{"code":"program.read","sensitivity":"NORMAL"}]`, false},
		{"changed sensitivity", `[{"code":"plan.publish","sensitivity":"SENSITIVE"},{"code":"program.read","sensitivity":"NORMAL"}]`, false},
		{"changed array order", `[{"code":"program.read","sensitivity":"NORMAL"},{"code":"plan.publish","sensitivity":"PRIVILEGED"}]`, false},
		{"extra field", `[{"code":"plan.publish","sensitivity":"PRIVILEGED","other":"hidden"},{"code":"program.read","sensitivity":"NORMAL"}]`, false},
		{"missing field", `[{"code":"plan.publish"},{"code":"program.read","sensitivity":"NORMAL"}]`, false},
		{"invalid JSON", `[{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := roleChangeSnapshotEqual([]byte(tc.stored), expected); got != tc.want {
				t.Fatalf("snapshot equality = %t, want %t", got, tc.want)
			}
		})
	}
}
