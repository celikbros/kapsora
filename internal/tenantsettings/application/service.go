package application

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/celikbros/kapsora/internal/identity"
)

var (
	ErrInvalid   = errors.New("tenantsettings: invalid request")
	ErrSensitive = errors.New("tenantsettings: sensitive setting conflict")
	ErrStale     = errors.New("tenantsettings: stale version")
	ErrKeyReused = errors.New("tenantsettings: idempotency key reused")
)

type Integer struct {
	EffectiveValue int        `json:"effectiveValue"`
	DefaultValue   int        `json:"defaultValue"`
	Minimum        int        `json:"minimum"`
	Maximum        int        `json:"maximum"`
	Source         string     `json:"source"`
	UpdatedAt      *time.Time `json:"updatedAt"`
}
type Accommodation struct {
	HoldMinutes     Integer `json:"holdMinutes"`
	QuoteTTLMinutes Integer `json:"quoteTtlMinutes"`
	CanManage       bool    `json:"canManage"`
}
type Response struct {
	Status   int
	ETag     string
	Body     []byte
	Replayed bool
}
type Command struct {
	HoldMinutes, QuoteTTLMinutes int
	ReasonCode, IfMatch, Key     string
	RawBody                      []byte
}
type Repository interface {
	Read(context.Context, identity.RequestContext) (Response, error)
	Save(context.Context, identity.RequestContext, Command) (Response, error)
	Preflight(context.Context, identity.RequestContext, Command) (*Response, error)
}
type Service struct{ Repo Repository }

func NewService(repo Repository) *Service { return &Service{Repo: repo} }
func (s *Service) Read(ctx context.Context, rc identity.RequestContext) (Response, error) {
	return s.Repo.Read(ctx, rc)
}
func (s *Service) Save(ctx context.Context, rc identity.RequestContext, c Command) (Response, error) {
	if err := validateCommand(c); err != nil {
		return Response{}, err
	}
	return s.Repo.Save(ctx, rc, c)
}
func validateCommand(c Command) error {
	if c.HoldMinutes < 1 || c.HoldMinutes > 1440 || c.QuoteTTLMinutes < 1 || c.QuoteTTLMinutes > 1440 ||
		(c.ReasonCode != "OPERATING_REQUIREMENT" && c.ReasonCode != "CORRECT_CONFIGURATION") ||
		len(c.Key) < 16 || len(c.Key) > 128 || len(c.IfMatch) != 66 || c.IfMatch[0] != '"' || c.IfMatch[65] != '"' || len(c.RawBody) == 0 {
		return ErrInvalid
	}
	if _, err := hex.DecodeString(c.IfMatch[1:65]); err != nil {
		return ErrInvalid
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(c.RawBody, &raw); err != nil || len(raw) != 3 {
		return ErrInvalid
	}
	var body struct {
		HoldMinutes     int    `json:"holdMinutes"`
		QuoteTTLMinutes int    `json:"quoteTtlMinutes"`
		ReasonCode      string `json:"reasonCode"`
	}
	if err := json.Unmarshal(c.RawBody, &body); err != nil || body.HoldMinutes != c.HoldMinutes || body.QuoteTTLMinutes != c.QuoteTTLMinutes || body.ReasonCode != c.ReasonCode {
		return ErrInvalid
	}
	return nil
}
func (s *Service) Preflight(ctx context.Context, rc identity.RequestContext, c Command) (*Response, error) {
	if err := validateCommand(c); err != nil {
		return nil, err
	}
	return s.Repo.Preflight(ctx, rc, c)
}
