package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	accommodationapp "github.com/celikbros/kapsora/internal/accommodation/application"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// The accommodation schema of migration 000040 (WP-I6-01). The tests below are about the
// half of the vertical that lives in the database rather than in Go, because that half is
// what the application layer is allowed to lean on: a rule the service can forget is not a
// rule, and the two the whole package stands on are here.

type accommodationSeed struct {
	tenant     uuid.UUID
	actor      uuid.UUID
	providerOr uuid.UUID
	otherOr    uuid.UUID
	provider   uuid.UUID
	location   uuid.UUID
	definition uuid.UUID
	property   uuid.UUID
	roomType   uuid.UUID
}

func seedAccommodation(t *testing.T, h *dbtest.Harness, code string) accommodationSeed { //nolint:funlen // one linear fixture reads better whole
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	s := accommodationSeed{}
	s.tenant = h.CreateTenant(code)
	s.actor = h.CreateActor("acc-clerk-"+code, "Accommodation Clerk")
	s.providerOr = h.CreateTenantOrganization(s.tenant, "Otel A", "PROVIDER")
	s.otherOr = h.CreateTenantOrganization(s.tenant, "Otel B", "PROVIDER")

	scan(&s.provider, "provider profile", `
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status)
		VALUES ($1, $2, 'HOTEL', 'ACTIVE') RETURNING id`, s.tenant, s.providerOr)
	scan(&s.location, "location", `
		INSERT INTO provider.location (tenant_id, provider_profile_id, code, name, city, timezone)
		VALUES ($1, $2, 'MERKEZ', 'Merkez', 'Antalya', 'Europe/Istanbul') RETURNING id`,
		s.tenant, s.provider)

	var category uuid.UUID
	scan(&category, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'KONAKLAMA', 'Konaklama', 'ACCOMMODATION') RETURNING id`, s.tenant)
	scan(&s.definition, "service definition", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'OTEL_GECE', 'Otel gecelemesi', 'RESERVATION', 'NIGHT') RETURNING id`,
		s.tenant, category)

	scan(&s.property, "property", `
		INSERT INTO accommodation.property (tenant_id, provider_organization_id, location_id,
		                                    code, name, property_type, timezone, city,
		                                    region_code, amenities, created_by, updated_by)
		VALUES ($1, $2, $3, 'MERKEZ', 'Merkez Otel', 'HOTEL', 'Europe/Istanbul', 'Antalya',
		        'ANTALYA', '["WIFI","POOL"]'::jsonb, $4, $4) RETURNING id`,
		s.tenant, s.providerOr, s.location, s.actor)
	scan(&s.roomType, "room type", `
		INSERT INTO accommodation.room_type (tenant_id, property_id, code, name, max_adults,
		                                     max_children, max_occupancy, service_definition_id,
		                                     created_by, updated_by)
		VALUES ($1, $2, 'STD', 'Standart', 2, 2, 3, $3, $4, $4) RETURNING id`,
		s.tenant, s.property, s.definition, s.actor)
	return s
}

// TestInventoryCheckRefusesCapacityBelowCommitment is the test the whole vertical rests on,
// with the application layer bypassed entirely: these statements run as the schema owner
// through the admin pool, so no Go code of ours is between them and the constraint.
//
// If `ck_inventory_day_commitment` were dropped tomorrow, WP-I6-02's FOR UPDATE would still
// look correct and a capacity lowered under an existing hold would quietly sell a room
// twice. This is the test that would go red.
func TestInventoryCheckRefusesCapacityBelowCommitment(t *testing.T) {
	h := dbtest.New(t)
	s := seedAccommodation(t, h, "ACC_CHECK")

	// Ten rooms opened, three of them promised: two held and one confirmed.
	h.AdminExec(`
		INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, capacity, held, confirmed)
		VALUES ($1, $2, '2026-06-15', 10, 2, 1)`, s.tenant, s.roomType)

	// Lowering to exactly what is committed is allowed: a provider closing a season down to
	// what it has already promised is doing something legitimate.
	if err := h.AdminExecErr(`
		UPDATE accommodation.inventory_day SET capacity = 3
		 WHERE tenant_id = $1 AND room_type_id = $2 AND stay_date = '2026-06-15'`,
		s.tenant, s.roomType); err != nil {
		t.Fatalf("lowering capacity to exactly the commitment was refused: %v", err)
	}

	// One below it is not.
	err := h.AdminExecErr(`
		UPDATE accommodation.inventory_day SET capacity = 2
		 WHERE tenant_id = $1 AND room_type_id = $2 AND stay_date = '2026-06-15'`,
		s.tenant, s.roomType)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation,
		"lowering capacity below held + confirmed")

	// And the same constraint refuses the other direction: a hold taken beyond capacity.
	err = h.AdminExecErr(`
		UPDATE accommodation.inventory_day SET held = held + 1
		 WHERE tenant_id = $1 AND room_type_id = $2 AND stay_date = '2026-06-15'`,
		s.tenant, s.roomType)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation,
		"holding a room beyond capacity")

	// An insert that is already over is refused too, so a row can never be written in a
	// state an update would be refused for reaching.
	err = h.AdminExecErr(`
		INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, capacity, held, confirmed)
		VALUES ($1, $2, '2026-06-16', 1, 1, 1)`, s.tenant, s.roomType)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation,
		"inserting a night already over capacity")

	// The three counters are non-negative, each by its own CHECK.
	for _, column := range []string{"capacity", "held", "confirmed"} {
		err := h.AdminExecErr(`
			INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, `+column+`)
			VALUES ($1, $2, '2026-06-20', -1)`, s.tenant, s.roomType)
		dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "negative "+column)
	}
}

// TestInventoryDayIsKeyedByTheNightAndNothingElse. The primary key is
// (tenant_id, room_type_id, stay_date) because the row for a room type on a night is the
// same row however it was reached. A surrogate key would allow two of them — which is to
// say, two answers to how many rooms are free, and a hold taken against the one nobody else
// is looking at.
func TestInventoryDayIsKeyedByTheNightAndNothingElse(t *testing.T) {
	h := dbtest.New(t)
	s := seedAccommodation(t, h, "ACC_PK")
	ctx, cancel := h.Ctx()
	defer cancel()

	var definition string
	if err := h.Admin.QueryRow(ctx, `
		SELECT pg_get_constraintdef(c.oid)
		  FROM pg_constraint c
		  JOIN pg_class t ON t.oid = c.conrelid
		  JOIN pg_namespace n ON n.oid = t.relnamespace
		 WHERE n.nspname = 'accommodation' AND t.relname = 'inventory_day'
		   AND c.contype = 'p'`).Scan(&definition); err != nil {
		t.Fatalf("read the primary key of accommodation.inventory_day: %v", err)
	}
	for _, column := range []string{"tenant_id", "room_type_id", "stay_date"} {
		if !contains(definition, column) {
			t.Errorf("%s is not part of the primary key: %s", column, definition)
		}
	}

	h.AdminExec(`
		INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, capacity)
		VALUES ($1, $2, '2026-07-01', 4)`, s.tenant, s.roomType)
	err := h.AdminExecErr(`
		INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, capacity)
		VALUES ($1, $2, '2026-07-01', 9)`, s.tenant, s.roomType)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation,
		"a second row for one room type on one night")

	// The table carries no surrogate id at all, so nothing can grow one by accident.
	var idColumns int
	if err := h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_schema = 'accommodation' AND table_name = 'inventory_day' AND column_name = 'id'`).
		Scan(&idColumns); err != nil {
		t.Fatalf("read the columns of accommodation.inventory_day: %v", err)
	}
	if idColumns != 0 {
		t.Error("accommodation.inventory_day has an id column; the night is the key")
	}
}

// TestAccommodationTablesAreTenantIsolatedAndTouched states the schema guarantees the
// application leans on and could not detect the loss of.
func TestAccommodationTablesAreTenantIsolatedAndTouched(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	for _, table := range []string{"property", "room_type", "inventory_day"} {
		var enabled, forced bool
		if err := h.Admin.QueryRow(ctx, `
			SELECT c.relrowsecurity, c.relforcerowsecurity
			  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'accommodation' AND c.relname = $1`, table).Scan(&enabled, &forced); err != nil {
			t.Fatalf("read RLS of accommodation.%s: %v", table, err)
		}
		if !enabled || !forced {
			t.Errorf("accommodation.%s RLS enabled=%t forced=%t", table, enabled, forced)
		}

		var triggers int
		if err := h.Admin.QueryRow(ctx, `
			SELECT count(*) FROM pg_trigger tg
			  JOIN pg_class c ON c.oid = tg.tgrelid
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'accommodation' AND c.relname = $1
			   AND tg.tgname = 'tg_touch_row' AND NOT tg.tgisinternal`, table).Scan(&triggers); err != nil {
			t.Fatalf("read the touch trigger of accommodation.%s: %v", table, err)
		}
		if triggers != 1 {
			t.Errorf("accommodation.%s has no tg_touch_row; its ETag would never move", table)
		}
	}

	// The composite tenant foreign keys, which are what keep a property, its rooms and
	// their nights inside one tenant however they were written.
	for _, want := range []struct{ table, constraint string }{
		{"property", "fk_property_organization"},
		{"property", "fk_property_location"},
		{"room_type", "fk_room_type_property"},
		{"room_type", "fk_room_type_service_definition"},
		{"inventory_day", "fk_inventory_day_room_type"},
	} {
		var columns int
		if err := h.Admin.QueryRow(ctx, `
			SELECT cardinality(c.conkey)
			  FROM pg_constraint c
			  JOIN pg_class t ON t.oid = c.conrelid
			  JOIN pg_namespace n ON n.oid = t.relnamespace
			 WHERE n.nspname = 'accommodation' AND t.relname = $1
			   AND c.conname = $2 AND c.contype = 'f'`, want.table, want.constraint).Scan(&columns); err != nil {
			t.Fatalf("read %s on accommodation.%s: %v", want.constraint, want.table, err)
		}
		if columns != 2 {
			t.Errorf("%s spans %d columns, want the composite (tenant_id, id) pair",
				want.constraint, columns)
		}
	}
}

// TestAccommodationRowsAreInvisibleToAnotherTenant is the isolation the whole platform
// rests on, exercised through the application role rather than the owner: kapsora_app has
// no BYPASSRLS, so what it can see is what a request can see.
func TestAccommodationRowsAreInvisibleToAnotherTenant(t *testing.T) {
	h := dbtest.New(t)
	a := seedAccommodation(t, h, "ACC_RLS_A")
	b := seedAccommodation(t, h, "ACC_RLS_B")

	h.AdminExec(`
		INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, capacity)
		VALUES ($1, $2, '2026-06-15', 5)`, a.tenant, a.roomType)

	count := func(tenant uuid.UUID, sql string, args ...any) int {
		t.Helper()
		var n int
		if err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, sql, args...).Scan(&n)
		}); err != nil {
			t.Fatalf("count as tenant %s: %v", tenant, err)
		}
		return n
	}

	if n := count(a.tenant, `SELECT count(*) FROM accommodation.property`); n != 1 {
		t.Errorf("tenant A sees %d of its own properties, want 1", n)
	}
	if n := count(b.tenant, `SELECT count(*) FROM accommodation.property WHERE id = $1`, a.property); n != 0 {
		t.Errorf("tenant B can see tenant A's property")
	}
	if n := count(b.tenant, `SELECT count(*) FROM accommodation.room_type WHERE id = $1`, a.roomType); n != 0 {
		t.Errorf("tenant B can see tenant A's room type")
	}
	if n := count(b.tenant, `
		SELECT count(*) FROM accommodation.inventory_day WHERE room_type_id = $1`, a.roomType); n != 0 {
		t.Errorf("tenant B can see tenant A's allotment")
	}

	// And writing across the boundary is refused by the policy's WITH CHECK rather than
	// silently landing in the other tenant.
	err := h.AppTx(b.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, capacity)
			VALUES ($1, $2, '2026-06-16', 3)`, a.tenant, a.roomType)
		return execErr
	})
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege,
		"writing an allotment into another tenant")
}

// TestRoomTypeOccupancyAndPropertyShapeAreConstrained. The occupancy pair is checked in Go
// as well, because a constraint violation is not something a clerk can read; the CHECK is
// what makes it a fact about the schema rather than about whichever code path wrote the row.
func TestRoomTypeOccupancyAndPropertyShapeAreConstrained(t *testing.T) {
	h := dbtest.New(t)
	s := seedAccommodation(t, h, "ACC_SHAPE")

	err := h.AdminExecErr(`
		INSERT INTO accommodation.room_type (tenant_id, property_id, code, name, max_adults,
		                                     max_children, max_occupancy, service_definition_id)
		VALUES ($1, $2, 'SUITE', 'Suit', 4, 2, 3, $3)`, s.tenant, s.property, s.definition)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation,
		"a room that sleeps four adults and three people in total")

	// A property's code is unique inside its provider and not inside the tenant: two hotel
	// chains both calling their flagship MERKEZ is ordinary.
	if err := h.AdminExecErr(`
		INSERT INTO accommodation.property (tenant_id, provider_organization_id, code, name,
		                                    property_type, timezone)
		VALUES ($1, $2, 'MERKEZ', 'Diğer Merkez', 'HOTEL', 'Europe/Istanbul')`,
		s.tenant, s.otherOr); err != nil {
		t.Errorf("a second provider could not use the code MERKEZ: %v", err)
	}
	err = h.AdminExecErr(`
		INSERT INTO accommodation.property (tenant_id, provider_organization_id, code, name,
		                                    property_type, timezone)
		VALUES ($1, $2, 'MERKEZ', 'Kopya', 'HOTEL', 'Europe/Istanbul')`,
		s.tenant, s.providerOr)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation,
		"one provider using one code twice")

	// The amenities column is an array and never an object, so nothing can put a free-text
	// note where a set of keys belongs.
	err = h.AdminExecErr(`
		INSERT INTO accommodation.property (tenant_id, provider_organization_id, code, name,
		                                    property_type, timezone, amenities)
		VALUES ($1, $2, 'NESNE', 'Nesne', 'HOTEL', 'Europe/Istanbul', '{"note":"deniz manzarali"}'::jsonb)`,
		s.tenant, s.otherOr)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "amenities as an object")
}

// TestAccommodationPropertyReadPermissionIsSeededAndGrantable is the two-halves test WP-I6-01
// section 3 asks for: the catalogue row of migration 000040 and the role templates in
// internal/identity/application/roles.go have to agree. A permission in one and not the other
// is a permission nobody can hold, or one nobody can be given.
func TestAccommodationPropertyReadPermissionIsSeededAndGrantable(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	const code = accommodationapp.PermissionRead

	var n int
	if err := h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
		t.Fatalf("read permission %s: %v", code, err)
	}
	if n != 1 {
		t.Fatalf("permission %s is seeded %d times, want once", code, n)
	}
	var sensitivity string
	if err := h.Admin.QueryRow(ctx,
		`SELECT sensitivity FROM iam.permission WHERE code = $1`, code).Scan(&sensitivity); err != nil {
		t.Fatalf("read sensitivity of %s: %v", code, err)
	}
	// NORMAL, not SENSITIVE: a hotel's name, its town and how many rooms are free on a
	// Tuesday are facts about a building. Marking it SENSITIVE would put every member
	// account into the access reviews that exist to list the grants over personal data.
	if sensitivity != "NORMAL" {
		t.Errorf("permission %s sensitivity = %s, want NORMAL", code, sensitivity)
	}

	byRole := map[string]map[string]bool{}
	for _, tpl := range identityapp.RoleTemplates() {
		byRole[tpl.Code] = map[string]bool{}
		for _, p := range tpl.Permissions {
			byRole[tpl.Code][p] = true
		}
	}

	// The four roles the work package names, by name, because each of them is a different
	// reason to look at a hotel and losing any one of them breaks a different screen.
	for _, role := range []string{"MEMBER", "PROVIDER_RESERVATION", "PROGRAM_MANAGER", "SPONSOR_HR"} {
		if !byRole[role][code] {
			t.Errorf("role %s does not hold %s", role, code)
		}
	}

	// And the pairings that must hold whatever anybody renames: nobody may open an
	// allotment or take a booking without being able to read the building it is in.
	for _, tpl := range identityapp.RoleTemplates() {
		for _, write := range []string{
			"accommodation.inventory.manage", "accommodation.booking.create",
			"accommodation.booking.manage",
		} {
			if byRole[tpl.Code][write] && !byRole[tpl.Code][code] {
				t.Errorf("role %s holds %s but cannot read a property", tpl.Code, write)
			}
		}
	}

	// The read is not a licence to write: a role that may look at a hotel does not thereby
	// gain the allotment.
	if byRole["SPONSOR_HR"]["accommodation.inventory.manage"] {
		t.Error("SPONSOR_HR can open an allotment")
	}
	if byRole["MEMBER"]["accommodation.inventory.manage"] {
		t.Error("MEMBER can open an allotment")
	}

	// Every permission a template names exists in the catalogue, so the two halves cannot
	// drift in the other direction either.
	for _, tpl := range identityapp.RoleTemplates() {
		for _, p := range tpl.Permissions {
			var seeded int
			if err := h.Admin.QueryRow(ctx,
				`SELECT count(*) FROM iam.permission WHERE code = $1`, p).Scan(&seeded); err != nil {
				t.Fatalf("read permission %s: %v", p, err)
			}
			if seeded != 1 {
				t.Errorf("role %s names permission %s, which is seeded %d times",
					tpl.Code, p, seeded)
			}
		}
	}
}
