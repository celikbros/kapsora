package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// SearchInput is the body of POST /api/v1/people/search-by-identifier.
type SearchInput struct {
	Type      string
	Value     string
	SponsorID uuid.UUID
}

// planIdentifiers turns submitted identifiers into rows ready to be written. It resolves
// the catalog entry, derives the scope key (D5), computes the tenant-salted blind index
// and the envelope, and collects the type codes whose existing rows must go first: a
// submitted `{type, value}` replaces the person's identifiers of that type, `{type,
// remove}` only removes them. Catalog problems are appended to ve instead of failing.
func (s *Service) planIdentifiers(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID,
	ids []domain.SubmittedIdentifier, ve *domain.ValidationError,
) (adds []NewIdentifier, removeTypes []string, err error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	sponsorID, sponsorLoaded := "", false
	for i, sub := range ids {
		field := fmt.Sprintf("identifiers[%d]", i)
		catalog, err := s.repo.GetIdentifierType(ctx, tx, tenantID, sub.Type)
		if errors.Is(err, ErrCatalogEntryNotFound) || (err == nil && catalog.Status != domain.StatusActive) {
			ve.Add(field+".type", domain.CodeIdentifierTypeUnknown, "tanımlayıcı türü tanımlı değil")
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		removeTypes = append(removeTypes, sub.Type)
		if sub.Remove {
			continue
		}
		if catalog.UniquenessScope == domain.ScopeSponsor && !sponsorLoaded {
			sponsorID, err = s.soleActiveSponsor(ctx, tx, tenantID, personID)
			if err != nil {
				return nil, nil, err
			}
			sponsorLoaded = true
		}
		rowID, err := uuid.NewV7()
		if err != nil {
			return nil, nil, fmt.Errorf("party: identifier id: %w", err)
		}
		scopeKey, ok := domain.ScopeKey(catalog.UniquenessScope, sponsorID, rowID.String())
		if !ok {
			ve.Add(field+".type", domain.CodeIdentifierScopeNeeded, "bu tanımlayıcı için tek bir aktif sponsor üyeliği gerekli")
			continue
		}
		hash, err := s.index.TenantIndex(ctx, tenantID, crypto.PurposePersonIdentifier, domain.BlindIndexInput(sub.Type, sub.Value))
		if err != nil {
			return nil, nil, fmt.Errorf("party: blind index: %w", err)
		}
		cipher, err := s.cipher.Encrypt(ctx, tenantID, crypto.PurposePersonIdentifier, []byte(sub.Value))
		if err != nil {
			return nil, nil, fmt.Errorf("party: encrypt identifier: %w", err)
		}
		adds = append(adds, NewIdentifier{
			ID: rowID, TenantID: tenantID, PersonID: personID, Type: sub.Type,
			Cipher: cipher, Hash: hash, MaskedValue: domain.MaskIdentifier(sub.Type, sub.Value),
			ScopeKey: scopeKey, Primary: sub.Primary,
		})
	}
	return adds, removeTypes, nil
}

// soleActiveSponsor returns the sponsor of the person's single active principal
// membership. An empty string means "no unambiguous sponsor", which makes a
// SPONSOR-scoped identifier a 422 IDENTIFIER_SCOPE_REQUIRED.
func (s *Service) soleActiveSponsor(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) (string, error) {
	if personID == uuid.Nil {
		return "", nil
	}
	sponsors, err := s.repo.ActiveSponsorOrganizations(ctx, tx, tenantID, personID)
	if err != nil {
		return "", err
	}
	if len(sponsors) != 1 {
		return "", nil
	}
	return sponsors[0].String(), nil
}

// SearchByIdentifier finds the single person carrying an identifier value through the
// tenant-salted blind index. The caller must hold member.identifier.search with a valid
// step-up; every call is written to the access audit with the identifier type only.
func (s *Service) SearchByIdentifier(ctx context.Context, rc identity.RequestContext, in SearchInput) (PersonSummary, error) {
	ve := &domain.ValidationError{}
	if !domain.ValidTypeCode(in.Type) {
		ve.Add("type", domain.CodeIdentifierTypeUnknown, "geçersiz tanımlayıcı türü")
		return PersonSummary{}, ve
	}
	value := domain.NormalizeIdentifier(in.Value)
	if value == "" {
		ve.Add("value", domain.CodeIdentifierInvalid, "tanımlayıcı boş olamaz")
		return PersonSummary{}, ve
	}

	var out PersonSummary
	var missing bool
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		catalog, err := s.repo.GetIdentifierType(ctx, tx, rc.TenantID, in.Type)
		if errors.Is(err, ErrCatalogEntryNotFound) || (err == nil && catalog.Status != domain.StatusActive) {
			ve.Add("type", domain.CodeIdentifierTypeUnknown, "tanımlayıcı türü tanımlı değil")
			return ve
		}
		if err != nil {
			return err
		}
		if err := domain.ValidateIdentifier(in.Type, value); err != nil {
			ve.Add("value", domain.CodeIdentifierInvalid, "tanımlayıcı doğrulanamadı")
			return ve
		}
		scopeKey, err := s.searchScope(ctx, tx, rc.TenantID, catalog, in.SponsorID, ve)
		if err != nil {
			return err
		}
		if ve.Len() > 0 {
			return ve
		}
		hash, err := s.index.TenantIndex(ctx, rc.TenantID, crypto.PurposePersonIdentifier, domain.BlindIndexInput(in.Type, value))
		if err != nil {
			return fmt.Errorf("party: blind index: %w", err)
		}
		personID, found, err := s.repo.FindPersonByIdentifierHash(ctx, tx, rc.TenantID, in.Type, scopeKey, hash)
		if err != nil {
			return err
		}
		if err := s.recordSearch(ctx, tx, rc, in.Type, personID, found); err != nil {
			return err
		}
		if !found {
			// The access event of a miss must commit too, so the miss is reported after
			// the transaction rather than by rolling it back.
			missing = true
			return nil
		}
		person, err := s.load(ctx, tx, rc.TenantID, personID)
		if err != nil {
			return err
		}
		out = PersonSummary{
			ID: person.ID, DisplayName: person.DisplayName, Status: person.Status,
			MaskedPrimaryIdentifier: person.MaskedPrimaryIdentifier,
		}
		return nil
	})
	if err != nil {
		return PersonSummary{}, err
	}
	if missing {
		return PersonSummary{}, ErrPersonNotFound
	}
	return out, nil
}

// searchScope derives the scope_key filter of a search. A NONE-scoped type never collides,
// so the search matches any scope key.
func (s *Service) searchScope(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, catalog CatalogType,
	sponsorID uuid.UUID, ve *domain.ValidationError,
) (*string, error) {
	switch catalog.UniquenessScope {
	case domain.ScopeNone:
		return nil, nil
	case domain.ScopeSponsor:
		if sponsorID == uuid.Nil {
			ve.Add("sponsorOrganizationId", domain.CodeIdentifierScopeNeeded, "bu tanımlayıcı türü için sponsor kurum zorunlu")
			return nil, nil
		}
		if _, err := s.repo.GetSponsorOrganization(ctx, tx, tenantID, sponsorID); err != nil {
			if errors.Is(err, ErrNotFound) {
				ve.Add("sponsorOrganizationId", domain.CodeSponsorOrganizationBad, "sponsor kurum bulunamadı")
				return nil, nil
			}
			return nil, err
		}
		key := sponsorID.String()
		return &key, nil
	default:
		key := ""
		return &key, nil
	}
}

// recordSearch writes the SENSITIVE access event. It carries the identifier type code and
// never the value, so a search is auditable without becoming a second copy of the data.
func (s *Service) recordSearch(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, typeCode string, personID uuid.UUID, found bool) error {
	ev := audit.AccessEvent{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, MembershipID: nullUUID(rc.MembershipID),
		ResourceType: "person_identifier", AccessType: audit.AccessSearch,
		Classification: audit.ClassPersonal, PurposeCode: "MEMBER_LOOKUP",
		ReasonText: "identifier_type=" + typeCode, Outcome: audit.OutcomeSuccess,
	}
	if found {
		ev.PersonID = nullUUID(personID)
		ev.ResourceID = nullUUID(personID)
	}
	return s.audit.RecordAccess(ctx, tx, ev)
}
