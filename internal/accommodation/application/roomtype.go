package application

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// NewRoomTypeInput is a room type as the API states it.
type NewRoomTypeInput struct {
	PropertyID          uuid.UUID
	Code                string
	Name                string
	MaxAdults           int
	MaxChildren         int
	MaxOccupancy        int
	Attributes          json.RawMessage
	ServiceDefinitionID uuid.UUID
	Status              string
}

// RoomTypePatchInput is the whole editable surface of a room type.
type RoomTypePatchInput struct {
	Name         string
	MaxAdults    int
	MaxChildren  int
	MaxOccupancy int
	Attributes   json.RawMessage
	Status       string
}

// ListRoomTypes returns the room types of one property.
func (s *Service) ListRoomTypes(ctx context.Context, rc identity.RequestContext,
	propertyID uuid.UUID, status string,
) ([]RoomTypeRecord, error) {
	if status != "" && !domain.InList(status, domain.Statuses) {
		return nil, fieldError("status", "ENUM", "ACTIVE veya INACTIVE olmalı")
	}
	var out []RoomTypeRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		scopes := scopeOf(rc)
		// The property is read first so a building this caller may not see answers 404
		// rather than an empty list, which would say the property exists and is empty.
		if _, err := s.repo.GetProperty(ctx, tx, rc.TenantID, propertyID, scopes); err != nil {
			return err
		}
		rows, err := s.repo.ListRoomTypes(ctx, tx, rc.TenantID, propertyID, status, scopes)
		if err != nil {
			return err
		}
		out = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CreateRoomType adds a sellable kind of room to a property.
//
// The service definition is checked here rather than left to the foreign key, and it is
// checked for more than existence: a room type is priced and entitled as its service, so a
// service whose unit is not NIGHT would produce a quote in the wrong unit and an
// entitlement drawn down in the wrong currency of counting.
func (s *Service) CreateRoomType(ctx context.Context, rc identity.RequestContext,
	in NewRoomTypeInput,
) (RoomTypeRecord, error) {
	attributes, err := validateNewRoomType(&in)
	if err != nil {
		return RoomTypeRecord{}, err
	}

	var out RoomTypeRecord
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		scopes := scopeOf(rc)
		if _, err := s.repo.GetProperty(ctx, tx, rc.TenantID, in.PropertyID, scopes); err != nil {
			return err
		}
		if err := s.checkRoomTypeService(ctx, tx, rc.TenantID, in.ServiceDefinitionID); err != nil {
			return err
		}
		record, err := s.repo.CreateRoomType(ctx, tx, rc.TenantID, NewRoomTypeRow{
			PropertyID: in.PropertyID, Code: in.Code, Name: in.Name,
			MaxAdults: in.MaxAdults, MaxChildren: in.MaxChildren, MaxOccupancy: in.MaxOccupancy,
			Attributes: attributes, ServiceDefinitionID: in.ServiceDefinitionID,
			Status: in.Status, ActorID: rc.Principal.ActorID,
		})
		if err != nil {
			return err
		}
		out = record
		return s.record(ctx, tx, rc, ActionRoomTypeCreate, ResourceRoomType, record.ID,
			map[string]any{
				"code": record.Code, "propertyId": record.PropertyID.String(),
				"serviceDefinitionId": record.ServiceDefinitionID.String(),
			})
	})
	if err != nil {
		return RoomTypeRecord{}, err
	}
	return out, nil
}

// PatchRoomType rewrites the editable half of a room type under an If-Match.
func (s *Service) PatchRoomType(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, in RoomTypePatchInput, expected int64,
) (RoomTypeRecord, error) {
	attributes, err := validateRoomTypePatch(&in)
	if err != nil {
		return RoomTypeRecord{}, err
	}

	var out RoomTypeRecord
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		scopes := scopeOf(rc)
		if _, err := s.repo.GetRoomType(ctx, tx, rc.TenantID, id, scopes); err != nil {
			return err
		}
		affected, err := s.repo.UpdateRoomType(ctx, tx, rc.TenantID, id, RoomTypeUpdateRow{
			Name: in.Name, MaxAdults: in.MaxAdults, MaxChildren: in.MaxChildren,
			MaxOccupancy: in.MaxOccupancy, Attributes: attributes, Status: in.Status,
			ActorID: rc.Principal.ActorID,
		}, expected, scopes)
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrVersionMismatch
		}
		updated, err := s.repo.GetRoomType(ctx, tx, rc.TenantID, id, scopes)
		if err != nil {
			return err
		}
		out = updated.RoomType
		return s.record(ctx, tx, rc, ActionRoomTypeUpdate, ResourceRoomType, id,
			map[string]any{"code": out.Code, "status": out.Status})
	})
	if err != nil {
		return RoomTypeRecord{}, err
	}
	return out, nil
}

// GetRoomTypeContext reads a room type with the property behind it, which is what every
// inventory command needs before it touches a night.
func (s *Service) GetRoomTypeContext(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) (RoomTypeContext, error) {
	var out RoomTypeContext
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.GetRoomType(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		out = record
		return nil
	})
	if err != nil {
		return RoomTypeContext{}, err
	}
	return out, nil
}

// checkRoomTypeService refuses a service definition this tenant does not have, one that
// has been deactivated, and one whose unit is not NIGHT.
func (s *Service) checkRoomTypeService(ctx context.Context, tx pgx.Tx, tenantID,
	definitionID uuid.UUID,
) error {
	unit, active, found, err := s.repo.ServiceDefinitionUnit(ctx, tx, tenantID, definitionID)
	if err != nil {
		return err
	}
	switch {
	case !found:
		return fieldError("serviceDefinitionId", "NOT_FOUND", "hizmet tanımı bulunamadı")
	case !active:
		return fieldError("serviceDefinitionId", "STATE", "hizmet tanımı pasif")
	case unit != domain.UnitNight:
		return fieldError("serviceDefinitionId", "STATE",
			"oda tipinin hizmet tanımı NIGHT birimli olmalı")
	}
	return nil
}

func validateNewRoomType(in *NewRoomTypeInput) ([]byte, error) {
	ve := &domain.ValidationError{}
	if in.PropertyID == uuid.Nil {
		ve.Add("propertyId", "REQUIRED", "tesis zorunlu")
	}
	if in.ServiceDefinitionID == uuid.Nil {
		ve.Add("serviceDefinitionId", "REQUIRED", "hizmet tanımı zorunlu")
	}
	in.Code = strings.TrimSpace(in.Code)
	if !validCode(in.Code) {
		ve.Add("code", "FORMAT", "A-Z, 0-9, _ ve - içeren en fazla 40 karakter olmalı")
	}
	attributes := validateRoomTypeBody(ve, &in.Name, in.MaxAdults, in.MaxChildren,
		in.MaxOccupancy, in.Attributes, &in.Status)
	return attributes, ve.OrNil()
}

func validateRoomTypePatch(in *RoomTypePatchInput) ([]byte, error) {
	ve := &domain.ValidationError{}
	attributes := validateRoomTypeBody(ve, &in.Name, in.MaxAdults, in.MaxChildren,
		in.MaxOccupancy, in.Attributes, &in.Status)
	return attributes, ve.OrNil()
}

// validateRoomTypeBody holds the rules a create and a patch share. The occupancy triple is
// checked here as well as by the CHECK, because "sleeps two adults and three people in
// total" is a typo a clerk can fix and a constraint violation is not something they can
// read.
func validateRoomTypeBody(ve *domain.ValidationError, name *string,
	maxAdults, maxChildren, maxOccupancy int, attributes json.RawMessage, status *string,
) []byte {
	*name = strings.TrimSpace(*name)
	if *name == "" || len([]rune(*name)) > 200 {
		ve.Add("name", "RANGE", "1-200 karakter olmalı")
	}
	if maxAdults < 1 || maxAdults > 20 {
		ve.Add("maxAdults", "RANGE", "1-20 arasında olmalı")
	}
	if maxChildren < 0 || maxChildren > 20 {
		ve.Add("maxChildren", "RANGE", "0-20 arasında olmalı")
	}
	if maxOccupancy < 1 || maxOccupancy > 40 {
		ve.Add("maxOccupancy", "RANGE", "1-40 arasında olmalı")
	}
	if maxOccupancy < maxAdults {
		ve.Add("maxOccupancy", "RANGE", "en az yetişkin kapasitesi kadar olmalı")
	}
	if !domain.InList(*status, domain.Statuses) {
		ve.Add("status", "ENUM", "ACTIVE veya INACTIVE olmalı")
	}
	return attributesOrEmpty("attributes", attributes, ve)
}
