package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// documentSeed is one tenant with a provider organization and one uploaded document, which
// is the smallest thing migration 000028 has anything to say about.
type documentSeed struct {
	tenant  uuid.UUID
	actor   uuid.UUID
	orgA    uuid.UUID
	orgB    uuid.UUID
	object  uuid.UUID
	version uuid.UUID
}

func seedDocument(h *dbtest.Harness, code string) documentSeed {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := documentSeed{tenant: h.CreateTenant(code)}
	s.actor = h.CreateActor("document-db-"+code, "Document "+code)
	s.orgA = h.CreateTenantOrganization(s.tenant, "Sağlayıcı A "+code, "PROVIDER")
	s.orgB = h.CreateTenantOrganization(s.tenant, "Sağlayıcı B "+code, "PROVIDER")

	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		h.T.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			h.T.Fatalf("seed %s: %v", what, err)
		}
	}
	scan(&s.object, "document object", `
		INSERT INTO document.object (tenant_id, object_key, classification, original_filename,
		                             content_type, owner_tenant_organization_id, uploaded_by)
		VALUES ($1, $2, 'INTERNAL', 'fatura.pdf', 'application/pdf', $3, $4)
		RETURNING id`, s.tenant, "seed/"+code+"/"+uuid.NewString(), s.orgA, s.actor)
	scan(&s.version, "document version", `
		INSERT INTO document.version (tenant_id, object_id, version_no, byte_size, content_type)
		VALUES ($1, $2, 1, 1024, 'application/pdf') RETURNING id`, s.tenant, s.object)
	return s
}

// TestDocumentPermissionsAreSeededAndGrantable checks both halves of the same fact. The
// catalogue row and the role template have to agree, because a permission that exists in
// one and not the other is a permission nobody can hold or one nobody can be given.
func TestDocumentPermissionsAreSeededAndGrantable(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	granted := map[string][]string{}
	for _, tpl := range identityapp.RoleTemplates() {
		for _, code := range tpl.Permissions {
			granted[code] = append(granted[code], tpl.Code)
		}
	}
	for _, code := range []string{
		"document.upload", "document.read", "document.link", "document.legal_hold.manage",
	} {
		var n int
		if err := h.Admin.QueryRow(ctx,
			`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
			t.Fatalf("read permission %s: %v", code, err)
		}
		if n != 1 {
			t.Fatalf("permission %s is seeded %d times, want once", code, n)
		}
		if len(granted[code]) == 0 {
			t.Fatalf("permission %s is in the catalogue but in no role template: nobody can hold it", code)
		}
	}
	for _, want := range []struct{ role, permission string }{
		{"PROVIDER_STAFF", "document.upload"},
		{"PROVIDER_STAFF", "document.read"},
		{"PROVIDER_STAFF", "document.link"},
		{"MEMBER", "document.upload"},
		{"MEMBER", "document.read"},
		{"MEDICAL_REVIEWER", "document.read"},
		{"MEDICAL_REVIEWER", "document.link"},
		{"FINANCIAL_REVIEWER", "document.read"},
		{"FINANCIAL_REVIEWER", "document.link"},
		// Putting a document beyond the reach of retention is an administrative act, and
		// a compliance one: neither a reviewer nor a provider holds it.
		{"TENANT_ADMIN", "document.legal_hold.manage"},
	} {
		found := false
		for _, role := range granted[want.permission] {
			if role == want.role {
				found = true
			}
		}
		if !found {
			t.Fatalf("role %s does not hold %s", want.role, want.permission)
		}
	}
	// The other half of "who may hold this": a legal hold is not something a provider or a
	// member can place on the platform's own records.
	for _, role := range granted["document.legal_hold.manage"] {
		switch role {
		case "PROVIDER_ADMIN", "PROVIDER_STAFF", "PROVIDER_BILLING", "PROVIDER_RESERVATION", "MEMBER":
			t.Fatalf("role %s holds document.legal_hold.manage; retention would be overridable from outside the tenant", role)
		}
	}
}

// TestNoDocumentColumnCanHoldFileBytes is v1.2 39.14 written as an assertion about the
// schema itself: **a file is never stored in the database**. The only binary column in the
// whole schema is the 32-byte digest, and its length is constrained, so a file cannot be
// smuggled into it either.
func TestNoDocumentColumnCanHoldFileBytes(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	// Anything that could hold arbitrary binary content: bytea, a large object reference,
	// or a base64 blob in json.
	rows, err := h.Admin.Query(ctx, `
		SELECT c.table_name, c.column_name, c.data_type, c.udt_name
		  FROM information_schema.columns c
		 WHERE c.table_schema = 'document'
		   AND (c.udt_name IN ('bytea','oid','lo') OR c.data_type IN ('bytea','json','jsonb'))
		 ORDER BY c.table_name, c.column_name`)
	if err != nil {
		t.Fatalf("read document columns: %v", err)
	}
	defer rows.Close()

	type column struct{ table, name, dataType string }
	var binary []column
	for rows.Next() {
		var c column
		var udt string
		if err := rows.Scan(&c.table, &c.name, &c.dataType, &udt); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		binary = append(binary, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read document columns: %v", err)
	}
	if len(binary) != 1 {
		t.Fatalf("the document schema has %d binary/json columns, want exactly one (object.sha256): %+v",
			len(binary), binary)
	}
	if binary[0].table != "object" || binary[0].name != "sha256" {
		t.Fatalf("the one binary column is %s.%s, want object.sha256", binary[0].table, binary[0].name)
	}

	// And it is a digest, not a file: the CHECK pins it to exactly 32 octets.
	var checked bool
	if err := h.Admin.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint con
			  JOIN pg_class rel ON rel.oid = con.conrelid
			  JOIN pg_namespace ns ON ns.oid = rel.relnamespace
			 WHERE ns.nspname = 'document' AND rel.relname = 'object' AND con.contype = 'c'
			   AND pg_get_constraintdef(con.oid) ILIKE '%octet_length(sha256) = 32%')`).Scan(&checked); err != nil {
		t.Fatalf("read the digest constraint: %v", err)
	}
	if !checked {
		t.Fatal("document.object.sha256 has no octet_length = 32 CHECK; a file could be written into it")
	}

	s := seedDocument(h, "DOC_BYTES")
	// A 33-byte value is refused, so the column cannot grow into a body one octet at a time.
	err = h.AdminExecErr(`
		UPDATE document.object SET sha256 = decode(repeat('ab', 33), 'hex')
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.object)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a digest longer than 32 bytes")

	// Every text column in the schema is bounded by a CHECK, so none of them is a place to
	// put a base64 of a file either.
	unbounded, err := h.Admin.Query(ctx, `
		SELECT c.table_name, c.column_name
		  FROM information_schema.columns c
		 WHERE c.table_schema = 'document'
		   AND c.data_type = 'text'
		   AND c.character_maximum_length IS NULL
		   AND NOT EXISTS (
		        SELECT 1
		          FROM pg_constraint con
		          JOIN pg_class rel ON rel.oid = con.conrelid
		          JOIN pg_namespace ns ON ns.oid = rel.relnamespace
		         WHERE ns.nspname = c.table_schema
		           AND rel.relname = c.table_name
		           AND con.contype = 'c'
		           AND pg_get_constraintdef(con.oid) ~ ('\y' || c.column_name || '\y')
		           -- The three shapes a bound takes: an explicit length check, a closed
		           -- list (which PostgreSQL stores as = ANY (ARRAY[...])), or a regex,
		           -- all of which cap what the column can hold.
		           AND (pg_get_constraintdef(con.oid) ILIKE '%length(%'
		                OR pg_get_constraintdef(con.oid) ILIKE '%ANY (ARRAY%'
		                OR pg_get_constraintdef(con.oid) ILIKE '%~%'))
		 ORDER BY c.table_name, c.column_name`)
	if err != nil {
		t.Fatalf("read text columns: %v", err)
	}
	defer unbounded.Close()
	var loose []string
	for unbounded.Next() {
		var table, name string
		if err := unbounded.Scan(&table, &name); err != nil {
			t.Fatalf("scan text column: %v", err)
		}
		loose = append(loose, table+"."+name)
	}
	if len(loose) > 0 {
		t.Fatalf("unbounded text columns in the document schema: %v; a file could be base64'd into one", loose)
	}
}

// TestDocumentSecureBucketRequiresACleanVerdict is the safety property of the whole package
// written as a constraint: bytes are in the secure bucket only when a scan cleared them.
// The schema refuses the row rather than trusting the one function that writes it.
func TestDocumentSecureBucketRequiresACleanVerdict(t *testing.T) {
	h := dbtest.New(t)
	s := seedDocument(h, "DOC_SECURE")

	for _, status := range []string{"PENDING", "SCANNING", "INFECTED", "FAILED"} {
		err := h.AdminExecErr(`
			UPDATE document.object SET bucket = 'secure', scan_status = $3,
			       byte_size = 10, sha256 = decode(repeat('ab', 32), 'hex')
			 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.object, status)
		dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation,
			"the secure bucket with scan_status "+status)
	}
	// A CLEAN verdict, with the size and digest a decision needs, is allowed.
	h.AdminExec(`
		UPDATE document.object SET bucket = 'secure', scan_status = 'CLEAN',
		       byte_size = 10, sha256 = decode(repeat('ab', 32), 'hex')
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.object)

	// A decided document says how big it was and what its digest is; without them nobody
	// can tell one stored file from another.
	err := h.AdminExecErr(`
		UPDATE document.object SET sha256 = NULL WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.object)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a CLEAN document with no digest")
}

// TestDocumentDigestIsUniquePerCleanObject is what makes the same file uploaded twice one
// object, and what lets a duplicate row point at the canonical copy without becoming a
// second one.
func TestDocumentDigestIsUniquePerCleanObject(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()
	s := seedDocument(h, "DOC_DIGEST")

	digest := `decode(repeat('cd', 32), 'hex')`
	h.AdminExec(`
		UPDATE document.object SET bucket = 'secure', scan_status = 'CLEAN',
		       byte_size = 10, sha256 = `+digest+`
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.object)

	var second uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO document.object (tenant_id, object_key, classification, original_filename,
		                             content_type, byte_size, sha256, scan_status, bucket)
		VALUES ($1, $2, 'INTERNAL', 'aynı.pdf', 'application/pdf', 10, `+digest+`, 'SCANNING', 'quarantine')
		RETURNING id`, s.tenant, "seed/digest/"+uuid.NewString()).Scan(&second); err != nil {
		t.Fatalf("insert the second object: %v", err)
	}

	// A second canonical CLEAN object with the same digest is refused.
	err := h.AdminExecErr(`
		UPDATE document.object SET scan_status = 'CLEAN', bucket = 'secure'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, second)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "two canonical CLEAN objects for one digest")

	// Pointing it at the canonical object is allowed, and it then carries the same key
	// without owning it: there is one stored file however many rows name it.
	var canonicalKey string
	if err := h.Admin.QueryRow(ctx,
		`SELECT object_key FROM document.object WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.object).Scan(&canonicalKey); err != nil {
		t.Fatalf("read the canonical key: %v", err)
	}
	h.AdminExec(`
		UPDATE document.object
		   SET scan_status = 'CLEAN', bucket = 'secure',
		       object_key = $3, duplicate_of_object_id = $4
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, second, canonicalKey, s.object)

	// A document may not point at itself.
	err = h.AdminExecErr(`
		UPDATE document.object SET duplicate_of_object_id = id
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, second)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a document that duplicates itself")
}

// TestDocumentVersionsAndScanResultsAreAppendOnly: a version that could be edited after the
// scan that cleared it is not a record of what was scanned, and a verdict that could be
// rewritten is not evidence of anything.
func TestDocumentVersionsAndScanResultsAreAppendOnly(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()
	s := seedDocument(h, "DOC_APPEND")

	var result uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO document.scan_result (tenant_id, object_id, version_id, engine, outcome)
		VALUES ($1, $2, $3, 'clamav', 'CLEAN') RETURNING id`,
		s.tenant, s.object, s.version).Scan(&result); err != nil {
		t.Fatalf("insert scan result: %v", err)
	}

	for _, c := range []struct {
		what string
		sql  string
		args []any
	}{
		{"updating a version", `UPDATE document.version SET byte_size = 1 WHERE tenant_id = $1 AND id = $2`,
			[]any{s.tenant, s.version}},
		{"deleting a version", `DELETE FROM document.version WHERE tenant_id = $1 AND id = $2`,
			[]any{s.tenant, s.version}},
		{"updating a verdict", `UPDATE document.scan_result SET outcome = 'INFECTED' WHERE tenant_id = $1 AND id = $2`,
			[]any{s.tenant, result}},
		{"deleting a verdict", `DELETE FROM document.scan_result WHERE tenant_id = $1 AND id = $2`,
			[]any{s.tenant, result}},
	} {
		dbtest.ExpectSQLState(t, h.AdminExecErr(c.sql, c.args...),
			dbtest.SQLStateIntegrityConstraint, c.what)
	}

	// An INFECTED verdict that names nothing is refused: an incident nobody can act on is
	// not a record.
	err := h.AdminExecErr(`
		INSERT INTO document.scan_result (tenant_id, object_id, version_id, engine, outcome)
		VALUES ($1, $2, $3, 'clamav', 'INFECTED')`, s.tenant, s.object, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "an INFECTED verdict with no finding")
	// And a CLEAN one that names something is a contradiction.
	err = h.AdminExecErr(`
		INSERT INTO document.scan_result (tenant_id, object_id, version_id, engine, outcome, finding)
		VALUES ($1, $2, $3, 'clamav', 'CLEAN', 'Eicar')`, s.tenant, s.object, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a CLEAN verdict that names a finding")
}

// TestDocumentLegalHoldShape covers the three things a hold has to get right: it names
// something, only one is active per target, and a release says both when and by whom.
func TestDocumentLegalHoldShape(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()
	s := seedDocument(h, "DOC_HOLD")

	// A hold over nothing would look like protection and protect nothing.
	err := h.AdminExecErr(`
		INSERT INTO document.legal_hold (tenant_id, reason) VALUES ($1, 'boş')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a legal hold naming no target")

	var hold uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO document.legal_hold (tenant_id, object_id, reason, placed_by)
		VALUES ($1, $2, 'dava', $3) RETURNING id`, s.tenant, s.object, s.actor).Scan(&hold); err != nil {
		t.Fatalf("insert legal hold: %v", err)
	}
	// A second active hold on the same document would make releasing one look like
	// releasing the document.
	err = h.AdminExecErr(`
		INSERT INTO document.legal_hold (tenant_id, object_id, reason) VALUES ($1, $2, 'ikinci')`,
		s.tenant, s.object)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "two active holds on one document")

	// A release is when and by whom, together.
	err = h.AdminExecErr(`
		UPDATE document.legal_hold SET released_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, hold)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a release with no releaser")

	h.AdminExec(`
		UPDATE document.legal_hold SET released_at = clock_timestamp(), released_by = $3
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, hold, s.actor)

	// Once released, the target may be held again.
	h.AdminExec(`
		INSERT INTO document.legal_hold (tenant_id, object_id, reason) VALUES ($1, $2, 'yeni dava')`,
		s.tenant, s.object)

	// The aggregate half of a hold arrives as a pair.
	err = h.AdminExecErr(`
		INSERT INTO document.legal_hold (tenant_id, aggregate_id, reason)
		VALUES ($1, gen_random_uuid(), 'yarım')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "an aggregate hold with no type")
}

// TestDocumentRowsAreTenantIsolated is the RLS check for this schema: the application role
// sees its own tenant's documents and nothing else, and cannot write a row into another
// tenant either.
func TestDocumentRowsAreTenantIsolated(t *testing.T) {
	h := dbtest.New(t)
	first := seedDocument(h, "DOC_RLS_A")
	second := seedDocument(h, "DOC_RLS_B")

	if err := h.AppTx(first.tenant, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM document.object`).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("the application role sees %d documents, want only its own tenant's 1", n)
		}
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM document.object WHERE id = $1`, second.object).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("the application role can read another tenant's document")
		}
		return nil
	}); err != nil {
		t.Fatalf("read as the application role: %v", err)
	}

	// Writing a row that claims another tenant is refused by the WITH CHECK half.
	err := h.AppTx(first.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO document.object (tenant_id, object_key, classification, original_filename, content_type)
			VALUES ($1, $2, 'INTERNAL', 'kaçak.pdf', 'application/pdf')`,
			second.tenant, "rls/"+uuid.NewString())
		return err
	})
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "writing into another tenant")
}

// TestDocumentCompositeKeysStayInsideTheTenant: every foreign key in the schema is
// composite, so a version, a link, a verdict or a hold can never point at another tenant's
// document.
func TestDocumentCompositeKeysStayInsideTheTenant(t *testing.T) {
	h := dbtest.New(t)
	first := seedDocument(h, "DOC_FK_A")
	second := seedDocument(h, "DOC_FK_B")

	for _, c := range []struct {
		what string
		sql  string
		args []any
	}{
		{"a version pointing at another tenant's document", `
			INSERT INTO document.version (tenant_id, object_id, version_no, byte_size, content_type)
			VALUES ($1, $2, 1, 10, 'application/pdf')`, []any{first.tenant, second.object}},
		{"a link pointing at another tenant's document", `
			INSERT INTO document.link (tenant_id, object_id, aggregate_type, aggregate_id, document_type_code)
			VALUES ($1, $2, 'SERVICE_REQUEST', gen_random_uuid(), 'INVOICE')`,
			[]any{first.tenant, second.object}},
		{"a verdict pointing at another tenant's version", `
			INSERT INTO document.scan_result (tenant_id, object_id, version_id, engine, outcome)
			VALUES ($1, $2, $3, 'clamav', 'CLEAN')`,
			[]any{first.tenant, first.object, second.version}},
		{"a hold pointing at another tenant's document", `
			INSERT INTO document.legal_hold (tenant_id, object_id, reason)
			VALUES ($1, $2, 'dava')`, []any{first.tenant, second.object}},
		{"a document owned by another tenant's organization", `
			INSERT INTO document.object (tenant_id, object_key, classification, original_filename,
			                             content_type, owner_tenant_organization_id)
			VALUES ($1, 'fk/' || gen_random_uuid(), 'INTERNAL', 'x.pdf', 'application/pdf', $2)`,
			[]any{first.tenant, second.orgA}},
	} {
		dbtest.ExpectSQLState(t, h.AdminExecErr(c.sql, c.args...),
			dbtest.SQLStateForeignKeyViolation, c.what)
	}
}

// TestDocumentLinkIsUniquePerTarget: the same document attached twice to one record under
// one type is one link, not two.
func TestDocumentLinkIsUniquePerTarget(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()
	s := seedDocument(h, "DOC_LINK")

	var aggregate uuid.UUID
	if err := h.Admin.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&aggregate); err != nil {
		t.Fatalf("generate an aggregate id: %v", err)
	}
	h.AdminExec(`
		INSERT INTO document.link (tenant_id, object_id, aggregate_type, aggregate_id, document_type_code)
		VALUES ($1, $2, 'SERVICE_REQUEST', $3, 'INVOICE')`, s.tenant, s.object, aggregate)

	err := h.AdminExecErr(`
		INSERT INTO document.link (tenant_id, object_id, aggregate_type, aggregate_id, document_type_code)
		VALUES ($1, $2, 'SERVICE_REQUEST', $3, 'INVOICE')`, s.tenant, s.object, aggregate)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "the same document linked twice")

	// The same document under a different type on the same record is a different fact.
	h.AdminExec(`
		INSERT INTO document.link (tenant_id, object_id, aggregate_type, aggregate_id, document_type_code)
		VALUES ($1, $2, 'SERVICE_REQUEST', $3, 'REFERRAL')`, s.tenant, s.object, aggregate)

	// A permission the link requires has to look like a permission code.
	err = h.AdminExecErr(`
		INSERT INTO document.link (tenant_id, object_id, aggregate_type, aggregate_id,
		                           document_type_code, required_permission)
		VALUES ($1, $2, 'SERVICE_REQUEST', $3, 'REPORT', 'Health Clinical Read')`,
		s.tenant, s.object, aggregate)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a link requiring a malformed permission")
}
