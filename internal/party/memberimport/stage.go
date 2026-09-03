package memberimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Field error codes of a staged row, stable across releases.
const (
	CodeRequired           = "REQUIRED"
	CodeFormat             = "FORMAT"
	CodeEnum               = "ENUM"
	CodeRange              = "RANGE"
	CodeIdentifierInvalid  = domain.CodeIdentifierInvalid
	CodeIdentifierUnknown  = domain.CodeIdentifierTypeUnknown
	CodePrincipalRequired  = domain.CodePrincipalRequired
	CodePrincipalUnknown   = "PRINCIPAL_UNKNOWN"
	CodeMembershipUnknown  = "MEMBERSHIP_TYPE_UNKNOWN"
	CodeRelationshipUnknwn = "RELATIONSHIP_TYPE_UNKNOWN"
	CodePlanUnknown        = "PLAN_UNKNOWN"
	CodePlanAmbiguous      = "PLAN_CODE_AMBIGUOUS"
	CodePlanNotPublished   = "PLAN_NOT_PUBLISHED"
)

var (
	sourceSystemPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	// identifierColumns maps a file column to the identifier type it carries.
	identifierColumns = []struct {
		Column string
		Type   string
	}{
		{ColTCKN, domain.TypeTCKN},
		{ColMemberNo, domain.TypeMemberNo},
		{ColEmployeeNo, domain.TypeEmployeeNo},
	}
	// principalTypePreference and dependantTypePreference map the file's PRINCIPAL and
	// DEPENDANT roles to a tenant membership type. The catalog is tenant data, so the
	// preference list is walked first and the flag decides otherwise.
	principalTypePreference = []string{"EMPLOYEE", "MEMBER", "INSURED", "CUSTOMER", "RETIREE", "STUDENT"}
	dependantTypePreference = []string{"FAMILY", "BENEFICIARY", "DEPENDENT"}
)

// UploadInput is the multipart command behind POST /api/v1/imports/members.
type UploadInput struct {
	SponsorOrganizationID uuid.UUID
	PlanID                uuid.UUID
	SourceSystem          string
	SourceVersion         string
	FileName              string
	Content               []byte
}

// Upload hashes the file, rejects a duplicate, parses it and stages every row with its
// identifiers normalised, blind indexed, masked and encrypted. Files up to the inline row
// limit are validated and matched before the call returns (queued=false, 201); larger
// ones answer 202 and are finished by the worker. The uploaded bytes are never stored.
func (s *Service) Upload(ctx context.Context, rc identity.RequestContext, in UploadInput) (batch Batch, queued bool, err error) {
	ve := &domain.ValidationError{}
	in.SourceSystem = strings.TrimSpace(in.SourceSystem)
	in.SourceVersion = strings.TrimSpace(in.SourceVersion)
	in.FileName = strings.TrimSpace(in.FileName)
	if !sourceSystemPattern.MatchString(in.SourceSystem) {
		ve.Add("sourceSystem", CodeFormat, "kaynak sistem kodu geçersiz")
	}
	if n := len(in.SourceVersion); n == 0 || n > 64 {
		ve.Add("sourceVersion", CodeFormat, "kaynak sürümü 1-64 karakter olmalı")
	}
	if in.FileName == "" {
		ve.Add("file", CodeRequired, "dosya adı zorunlu")
	}
	if in.SponsorOrganizationID == uuid.Nil {
		ve.Add("sponsorOrganizationId", CodeRequired, "sponsor kurum zorunlu")
	}
	if err := ve.OrNil(); err != nil {
		return Batch{}, false, err
	}

	records, err := ParseCSV(in.Content)
	if err != nil {
		return Batch{}, false, err
	}
	digest := sha256.Sum256(in.Content)

	var batchID uuid.UUID
	inline := len(records) <= s.inlineRowLimit
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		sponsor, err := q.GetSponsorOrganization(ctx, sqlcgen.GetSponsorOrganizationParams{
			TenantID: rc.TenantID, ID: in.SponsorOrganizationID,
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			ve.Add("sponsorOrganizationId", domain.CodeSponsorOrganizationBad, "sponsor kurum bulunamadı")
		case err != nil:
			return fmt.Errorf("memberimport: sponsor organization: %w", err)
		case !domain.Contains(domain.SponsorRoles, sponsor.RelationshipRole):
			ve.Add("sponsorOrganizationId", domain.CodeSponsorOrganizationBad, "kurum SPONSOR veya PAYER rolünde olmalı")
		}
		if in.PlanID != uuid.Nil {
			if _, err := q.GetPlan(ctx, sqlcgen.GetPlanParams{TenantID: rc.TenantID, ID: in.PlanID}); err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("memberimport: default plan: %w", err)
				}
				ve.Add("planId", CodePlanUnknown, "plan bulunamadı")
			}
		}
		if ve.Len() > 0 {
			return ve
		}

		cat, err := loadCatalogs(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}

		status := StatusReceived
		if !inline {
			status = StatusValidating
		}
		created, err := q.CreateImportBatch(ctx, sqlcgen.CreateImportBatchParams{
			TenantID: rc.TenantID, SponsorTenantOrganizationID: in.SponsorOrganizationID,
			PlanID: nullUUID(in.PlanID), SourceSystem: in.SourceSystem, SourceVersion: in.SourceVersion,
			FileName: in.FileName, FileSha256: digest[:], Format: Format,
			RowCount: int32(len(records)), //nolint:gosec // bounded by MaxRows
			Status:   status, CreatedBy: nullUUID(rc.Principal.ActorID),
		})
		if err != nil {
			if isUniqueViolation(err, "uq_import_batch_source") {
				return ErrDuplicate
			}
			return fmt.Errorf("memberimport: create batch: %w", err)
		}
		batchID = created.ID

		if err := s.stageRows(ctx, tx, rc.TenantID, batchID, in, cat, records); err != nil {
			return err
		}
		if !inline {
			if _, _, err := outbox.Publish(ctx, tx, outbox.Event{
				TenantID: nullUUID(rc.TenantID), AggregateType: aggregateType, AggregateID: batchID,
				Type:             StagedEvent,
				Payload:          map[string]any{"importBatchId": batchID},
				DeduplicationKey: StagedEvent + ":" + batchID.String(),
			}); err != nil {
				return err
			}
		}
		return s.record(ctx, tx, rc, "member_import.create", batchID, map[string]any{
			"row_count": len(records), "source_system": in.SourceSystem,
			"source_version": in.SourceVersion, "sponsor_organization_id": in.SponsorOrganizationID,
			"file_sha256": hex.EncodeToString(digest[:]),
		})
	})
	if err != nil {
		return Batch{}, false, err
	}

	if inline {
		if err := s.RunValidation(ctx, rc.TenantID, batchID); err != nil {
			return Batch{}, false, err
		}
	}
	out, err := s.Get(ctx, rc, batchID)
	return out, !inline, err
}

// stageRows turns parsed records into staging rows and writes them in pgx batches.
func (s *Service) stageRows(ctx context.Context, tx pgx.Tx, tenantID, batchID uuid.UUID,
	in UploadInput, cat catalogs, records []Record,
) error {
	today := s.now()
	params := make([]sqlcgen.InsertImportRowParams, 0, s.chunkSize)
	for i := range records {
		row, err := s.stageRecord(ctx, tenantID, batchID, in.SponsorOrganizationID, cat, records[i], today)
		if err != nil {
			return err
		}
		params = append(params, row)
		if len(params) == s.chunkSize {
			if err := insertRows(ctx, tx, params); err != nil {
				return err
			}
			params = params[:0]
		}
	}
	if len(params) > 0 {
		return insertRows(ctx, tx, params)
	}
	return nil
}

func insertRows(ctx context.Context, tx pgx.Tx, params []sqlcgen.InsertImportRowParams) error {
	var firstErr error
	results := sqlcgen.New(tx).InsertImportRow(ctx, params)
	results.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if firstErr != nil {
		return fmt.Errorf("memberimport: stage rows: %w", firstErr)
	}
	return nil
}

// stageRecord validates the text of one row and derives everything the staging table is
// allowed to hold. The identifier plaintext lives only inside this function: it leaves as
// a blind index, a mask and one tenant-encrypted envelope.
func (s *Service) stageRecord(ctx context.Context, tenantID, batchID, sponsorID uuid.UUID,
	cat catalogs, rec Record, today time.Time,
) (sqlcgen.InsertImportRowParams, error) {
	rowErrors := make([]RowError, 0, 4)
	add := func(field, code, message string) {
		rowErrors = append(rowErrors, RowError{Field: field, Code: code, Message: message})
	}

	if rec.SourceRecordID == "" {
		add(ColSourceRecordID, CodeRequired, "kayıt numarası zorunlu")
	}

	person := domain.NewPerson{
		FirstName: rec.FirstName, MiddleName: rec.MiddleName, LastName: rec.LastName,
		SexAtBirth: rec.SexAtBirth,
	}
	if rec.BirthDate != "" {
		if d, ok := parseDate(rec.BirthDate); ok {
			person.BirthDate = &d
		} else {
			add(ColBirthDate, CodeFormat, "tarih YYYY-AA-GG biçiminde olmalı")
		}
	}
	// The identifier columns go through the same normalisation, checksum and masking
	// rules as a manually registered person (WP-I2-01 section 3.2).
	valueColumns := make([]string, 0, len(identifierColumns))
	for _, col := range identifierColumns {
		value := recordColumn(rec, col.Column)
		if value == "" {
			continue
		}
		if _, ok := cat.identifierScopes[col.Type]; !ok {
			add(col.Column, CodeIdentifierUnknown, "bu tanımlayıcı türü kurumda tanımlı değil")
			continue
		}
		person.Identifiers = append(person.Identifiers, domain.SubmittedIdentifier{
			Type: col.Type, Value: value, Primary: len(valueColumns) == 0,
		})
		valueColumns = append(valueColumns, col.Column)
	}
	if err := domain.ValidateNew(&person, today); err != nil {
		var ve *domain.ValidationError
		if !errors.As(err, &ve) {
			return sqlcgen.InsertImportRowParams{}, err
		}
		for _, f := range ve.Fields {
			add(columnOf(f.Field, valueColumns), f.Code, f.Message)
		}
	}

	payload := rowPayload{
		LineNo: rec.Line, FirstName: person.FirstName, MiddleName: person.MiddleName,
		LastName: person.LastName, SexAtBirth: person.SexAtBirth, Role: rec.MembershipType,
		Relationship: rec.Relationship, PlanCode: rec.PlanCode,
	}
	if person.BirthDate != nil {
		payload.BirthDate = person.BirthDate.Format(time.DateOnly)
	}

	switch rec.MembershipType {
	case RolePrincipal:
		payload.MembershipType = cat.principalType
		if cat.principalType == "" {
			add(ColMembershipType, CodeMembershipUnknown, "asıl üyelik için uygun üyelik türü tanımlı değil")
		}
	case RoleDependant:
		payload.MembershipType = cat.dependantType
		if cat.dependantType == "" {
			add(ColMembershipType, CodeMembershipUnknown, "bağımlı üyelik için uygun üyelik türü tanımlı değil")
		}
		if rec.PrincipalMemberNo == "" {
			add(ColPrincipalMemberNo, CodePrincipalRequired, "bağımlı satır için asıl üye numarası zorunlu")
		}
	case "":
		add(ColMembershipType, CodeRequired, "üyelik türü zorunlu")
	default:
		add(ColMembershipType, CodeEnum, "PRINCIPAL veya DEPENDANT olmalı")
	}

	if rec.Relationship != "" {
		if _, ok := cat.relationshipDirectional[rec.Relationship]; !ok {
			add(ColRelationship, CodeRelationshipUnknwn, "ilişki türü tanımlı değil")
		}
	}

	validFrom, hasFrom := parseDate(rec.ValidFrom)
	switch {
	case rec.ValidFrom == "":
		add(ColValidFrom, CodeRequired, "başlangıç tarihi zorunlu")
	case !hasFrom:
		add(ColValidFrom, CodeFormat, "tarih YYYY-AA-GG biçiminde olmalı")
	default:
		payload.ValidFrom = validFrom.Format(time.DateOnly)
	}
	if rec.ValidTo != "" {
		validTo, ok := parseDate(rec.ValidTo)
		switch {
		case !ok:
			add(ColValidTo, CodeFormat, "tarih YYYY-AA-GG biçiminde olmalı")
		case hasFrom && !validTo.After(validFrom):
			add(ColValidTo, CodeRange, "bitiş tarihi başlangıçtan sonra olmalı")
		default:
			payload.ValidTo = validTo.Format(time.DateOnly)
		}
	}

	staged := make([]stagedIdentifier, 0, len(person.Identifiers))
	plaintext := make(map[string]string, len(person.Identifiers))
	for _, sub := range person.Identifiers {
		if sub.Value == "" {
			continue
		}
		if err := domain.ValidateIdentifier(sub.Type, sub.Value); err != nil {
			continue // already reported by ValidateNew
		}
		hash, err := s.index.TenantIndex(ctx, tenantID, identifierPurpose, domain.BlindIndexInput(sub.Type, sub.Value))
		if err != nil {
			return sqlcgen.InsertImportRowParams{}, fmt.Errorf("memberimport: blind index: %w", err)
		}
		scope := cat.identifierScopes[sub.Type]
		scopeKey := ""
		if scope == domain.ScopeSponsor {
			scopeKey = sponsorID.String()
		}
		staged = append(staged, stagedIdentifier{
			Type: sub.Type, ScopeKey: scopeKey, Hash: hex.EncodeToString(hash),
			Masked: domain.MaskIdentifier(sub.Type, sub.Value), Primary: sub.Primary,
		})
		plaintext[sub.Type] = sub.Value
	}

	// The principal reference is a member number too, so it is stored the same way: a
	// blind index the batch can join on and a mask an operator can read.
	if ref := domain.NormalizeIdentifier(rec.PrincipalMemberNo); ref != "" {
		if err := domain.ValidateIdentifier(domain.TypeMemberNo, ref); err != nil {
			add(ColPrincipalMemberNo, CodeIdentifierInvalid, "asıl üye numarası doğrulanamadı")
		} else {
			hash, err := s.index.TenantIndex(ctx, tenantID, identifierPurpose,
				domain.BlindIndexInput(domain.TypeMemberNo, ref))
			if err != nil {
				return sqlcgen.InsertImportRowParams{}, fmt.Errorf("memberimport: blind index: %w", err)
			}
			payload.PrincipalHash = hex.EncodeToString(hash)
			payload.PrincipalMasked = domain.MaskIdentifier(domain.TypeMemberNo, ref)
		}
	}

	var cipher []byte
	if len(plaintext) > 0 {
		envelope, err := json.Marshal(plaintext)
		if err != nil {
			return sqlcgen.InsertImportRowParams{}, fmt.Errorf("memberimport: encode identifiers: %w", err)
		}
		cipher, err = s.cipher.Encrypt(ctx, tenantID, identifierPurpose, envelope)
		if err != nil {
			return sqlcgen.InsertImportRowParams{}, fmt.Errorf("memberimport: encrypt identifiers: %w", err)
		}
	}

	payloadJSON, err := encodeJSON(payload)
	if err != nil {
		return sqlcgen.InsertImportRowParams{}, fmt.Errorf("memberimport: encode payload: %w", err)
	}
	identifiersJSON, err := encodeJSON(staged)
	if err != nil {
		return sqlcgen.InsertImportRowParams{}, fmt.Errorf("memberimport: encode identifiers: %w", err)
	}
	errorsJSON, err := encodeJSON(rowErrors)
	if err != nil {
		return sqlcgen.InsertImportRowParams{}, fmt.Errorf("memberimport: encode errors: %w", err)
	}
	status := RowPending
	if len(rowErrors) > 0 {
		status = RowInvalid
	}
	return sqlcgen.InsertImportRowParams{
		TenantID: tenantID, BatchID: batchID,
		RowNo:          int32(rec.RowNo), //nolint:gosec // bounded by MaxRows
		SourceRecordID: rec.SourceRecordID,
		Payload:        payloadJSON, Identifiers: identifiersJSON, IdentifierCipher: cipher,
		Status: status, Errors: errorsJSON,
	}, nil
}

// catalogs are the tenant type catalogs the pipeline consults once per batch.
type catalogs struct {
	identifierScopes        map[string]string
	relationshipDirectional map[string]bool
	principalType           string
	dependantType           string
}

func loadCatalogs(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (catalogs, error) {
	q := sqlcgen.New(tx)
	out := catalogs{identifierScopes: map[string]string{}, relationshipDirectional: map[string]bool{}}

	idTypes, err := q.ListIdentifierTypes(ctx, tenantID)
	if err != nil {
		return catalogs{}, fmt.Errorf("memberimport: identifier types: %w", err)
	}
	for _, t := range idTypes {
		if t.Status == domain.StatusActive {
			out.identifierScopes[t.Code] = t.UniquenessScope
		}
	}
	relTypes, err := q.ListRelationshipTypes(ctx, tenantID)
	if err != nil {
		return catalogs{}, fmt.Errorf("memberimport: relationship types: %w", err)
	}
	for _, t := range relTypes {
		if t.Status == domain.StatusActive {
			out.relationshipDirectional[t.Code] = t.IsDirectional
		}
	}
	memTypes, err := q.ListMembershipTypes(ctx, tenantID)
	if err != nil {
		return catalogs{}, fmt.Errorf("memberimport: membership types: %w", err)
	}
	out.principalType = pickMembershipType(memTypes, principalTypePreference, false)
	out.dependantType = pickMembershipType(memTypes, dependantTypePreference, true)
	return out, nil
}

// pickMembershipType maps the file's PRINCIPAL/DEPENDANT role to a tenant membership
// type: the first preferred ACTIVE code, otherwise the first ACTIVE code with the right
// requires_principal flag (the catalog is ordered by code, so the choice is stable).
func pickMembershipType(rows []sqlcgen.ListMembershipTypesRow, preference []string, requiresPrincipal bool) string {
	active := make(map[string]bool, len(rows))
	for _, r := range rows {
		if r.Status == domain.StatusActive && r.RequiresPrincipal == requiresPrincipal {
			active[r.Code] = true
		}
	}
	for _, code := range preference {
		if active[code] {
			return code
		}
	}
	for _, r := range rows {
		if active[r.Code] {
			return r.Code
		}
	}
	return ""
}

// recordColumn reads one identifier column of a record by its file name.
func recordColumn(rec Record, column string) string {
	switch column {
	case ColTCKN:
		return rec.TCKN
	case ColMemberNo:
		return rec.MemberNo
	case ColEmployeeNo:
		return rec.EmployeeNo
	default:
		return ""
	}
}

// columnOf translates a party domain field path into the file column it came from, so an
// operator reads "tckn" rather than "identifiers[0].value".
func columnOf(field string, valueColumns []string) string {
	switch field {
	case "firstName":
		return ColFirstName
	case "middleName":
		return ColMiddleName
	case "lastName":
		return ColLastName
	case "birthDate":
		return ColBirthDate
	case "sexAtBirth":
		return ColSexAtBirth
	}
	if !strings.HasPrefix(field, "identifiers[") {
		return field
	}
	var index int
	if _, err := fmt.Sscanf(field, "identifiers[%d]", &index); err != nil {
		return field
	}
	if index < 0 || index >= len(valueColumns) {
		return field
	}
	return valueColumns[index]
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && (constraint == "" || pgErr.ConstraintName == constraint)
}
