package tenantsettingspg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/accommodation/settings"
	"github.com/celikbros/kapsora/internal/audit"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	"github.com/celikbros/kapsora/internal/tenantsettings/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const operation = "accommodation.update"

var keys = []string{settings.KeyHoldMinutes, settings.KeyQuoteTTLMinutes}

func uuidNull(id uuid.UUID) uuid.NullUUID { return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil} }

type Repository struct {
	Pool      *pgxpool.Pool
	Authority identityapp.TenantSettingsAuthority
	Audit     audit.Recorder
}

func NewRepository(pool *pgxpool.Pool, authority identityapp.TenantSettingsAuthority, recorder ...audit.Recorder) *Repository {
	r := &Repository{Pool: pool, Authority: authority, Audit: auditpg.New()}
	if len(recorder) > 0 && recorder[0] != nil {
		r.Audit = recorder[0]
	}
	return r
}

var _ application.Repository = (*Repository)(nil)

type row struct {
	key       string
	raw       []byte
	sensitive bool
	version   int64
	updated   time.Time
}

func (r *Repository) transaction(ctx context.Context, rc identity.RequestContext, manage bool, fn func(context.Context, pgx.Tx, bool) error) error {
	return db.WithTenantTx(ctx, r.Pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		status, err := sqlcgen.New(tx).TenantSettingsLockParent(ctx, rc.TenantID)
		if err != nil {
			return identity.ErrPermissionDenied
		}
		if status != "ACTIVE" {
			return identity.ErrPermissionDenied
		}
		canManage, err := r.Authority.CheckTx(ctx, tx, rc, manage)
		if err != nil {
			return err
		}
		return fn(ctx, tx, canManage)
	})
}
func rows(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (map[string]row, error) {
	out := map[string]row{}
	all, err := sqlcgen.New(tx).TenantSettingsReadAdmitted(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, item := range all {
		if item.IsSensitive {
			return nil, application.ErrSensitive
		}
		out[item.SettingKey] = row{key: item.SettingKey, raw: item.ValueJson, sensitive: item.IsSensitive, version: item.RowVersion, updated: item.UpdatedAt}
	}
	return out, nil
}
func token(tenant string, found map[string]row) string {
	h := sha256.New()
	h.Write([]byte("kapsora-accommodation-tenant-settings-v1\x00"))
	h.Write([]byte(tenant))
	for _, key := range keys {
		h.Write([]byte{0})
		h.Write([]byte(key))
		if x, ok := found[key]; ok {
			h.Write([]byte(fmt.Sprintf(":%d", x.version)))
		} else {
			h.Write([]byte(":absent"))
		}
	}
	return `"` + hex.EncodeToString(h.Sum(nil)) + `"`
}
func field(found map[string]row, key string, def int) application.Integer {
	v := application.Integer{EffectiveValue: def, DefaultValue: def, Minimum: 1, Maximum: 1440, Source: "DEFAULT"}
	x, ok := found[key]
	if !ok {
		return v
	}
	v.UpdatedAt = &x.updated
	v.Source = "INVALID_FALLBACK"
	var s string
	if err := json.Unmarshal(x.raw, &s); err != nil {
		s = string(x.raw)
	}
	n, err := strconv.Atoi(s)
	if err == nil && n >= 1 && n <= 1440 {
		v.EffectiveValue = n
		v.Source = "CONFIGURED"
	}
	return v
}
func projection(found map[string]row, canManage bool) application.Accommodation {
	return application.Accommodation{HoldMinutes: field(found, settings.KeyHoldMinutes, settings.DefaultHoldMinutes), QuoteTTLMinutes: field(found, settings.KeyQuoteTTLMinutes, settings.DefaultQuoteTTLMinutes), CanManage: canManage}
}
func response(tenant string, found map[string]row, canManage bool) (application.Response, error) {
	body, err := json.Marshal(projection(found, canManage))
	if err != nil {
		return application.Response{}, err
	}
	body = append(body, '\n')
	return application.Response{Status: 200, ETag: token(tenant, found), Body: body}, nil
}
func (r *Repository) Read(ctx context.Context, rc identity.RequestContext) (application.Response, error) {
	var out application.Response
	err := r.transaction(ctx, rc, false, func(ctx context.Context, tx pgx.Tx, manage bool) error {
		found, err := rows(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}
		manage, err = r.Authority.CheckTx(ctx, tx, rc, false)
		if err != nil {
			return err
		}
		out, err = response(rc.TenantID.String(), found, manage)
		return err
	})
	return out, err
}
func hashes(cmd application.Command) ([32]byte, [32]byte) {
	key := sha256.Sum256([]byte(cmd.Key))
	h := sha256.New()
	h.Write([]byte("PUT\x00/api/v1/admin/tenant-settings/accommodation\x00"))
	h.Write([]byte(cmd.IfMatch))
	h.Write([]byte{0})
	h.Write(cmd.RawBody)
	var request [32]byte
	copy(request[:], h.Sum(nil))
	return key, request
}
func receipt(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, cmd application.Command) (*application.Response, error) {
	key, request := hashes(cmd)
	var prior []byte
	var out application.Response
	receipt, err := sqlcgen.New(tx).TenantSettingsReceiptGet(ctx, sqlcgen.TenantSettingsReceiptGetParams{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, KeyHash: key[:]})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	prior = receipt.RequestHash
	out.Status = int(receipt.ResponseStatus)
	out.ETag = receipt.ResponseEtag
	out.Body = receipt.ResponseBody
	if !bytes.Equal(prior, request[:]) {
		return nil, application.ErrKeyReused
	}
	out.Replayed = true
	return &out, nil
}
func (r *Repository) Preflight(ctx context.Context, rc identity.RequestContext, cmd application.Command) (*application.Response, error) {
	var out *application.Response
	err := r.transaction(ctx, rc, true, func(ctx context.Context, tx pgx.Tx, _ bool) error {
		var err error
		out, err = receipt(ctx, tx, rc, cmd)
		return err
	})
	return out, err
}
func (r *Repository) Save(ctx context.Context, rc identity.RequestContext, cmd application.Command) (application.Response, error) {
	var out application.Response
	err := r.transaction(ctx, rc, true, func(ctx context.Context, tx pgx.Tx, _ bool) error {
		prior, err := receipt(ctx, tx, rc, cmd)
		if err != nil {
			return err
		}
		if prior != nil {
			out = *prior
			return nil
		}
		found, err := rows(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}
		if _, err = r.Authority.CheckTx(ctx, tx, rc, true); err != nil {
			return err
		}
		if token(rc.TenantID.String(), found) != cmd.IfMatch {
			return application.ErrStale
		}
		before := projection(found, true)
		values := []int{cmd.HoldMinutes, cmd.QuoteTTLMinutes}
		changed := false
		for i, key := range keys {
			canonical := strconv.Itoa(values[i])
			x, present := found[key]
			if present && bytes.Equal(x.raw, []byte(canonical)) {
				continue
			}
			changed = true
			err = sqlcgen.New(tx).TenantSettingsInsertOrUpdate(ctx, sqlcgen.TenantSettingsInsertOrUpdateParams{TenantID: rc.TenantID, SettingKey: key, Column3: []byte(canonical)})
			if err != nil {
				return err
			}
		}
		found, err = rows(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}
		out, err = response(rc.TenantID.String(), found, true)
		if err != nil {
			return err
		}
		if changed {
			after := projection(found, true)
			detail := map[string]any{"hold_minutes_before": before.HoldMinutes.EffectiveValue, "hold_minutes_after": after.HoldMinutes.EffectiveValue, "quote_ttl_minutes_before": before.QuoteTTLMinutes.EffectiveValue, "quote_ttl_minutes_after": after.QuoteTTLMinutes.EffectiveValue, "hold_source_before": before.HoldMinutes.Source, "hold_source_after": after.HoldMinutes.Source, "quote_source_before": before.QuoteTTLMinutes.Source, "quote_source_after": after.QuoteTTLMinutes.Source, "previous_version": cmd.IfMatch, "current_version": out.ETag, "setting_codes": "accommodation.hold_minutes,accommodation.quote_ttl_minutes"}
			if err = r.Audit.Record(ctx, tx, audit.Event{TenantID: uuidNull(rc.TenantID), ActorID: uuidNull(rc.Principal.ActorID), MembershipID: uuidNull(rc.MembershipID), Category: audit.CategoryAdmin, ActionCode: "tenant_settings.accommodation.update", ResourceType: "tenant_settings.accommodation", Outcome: audit.OutcomeSuccess, ReasonCode: cmd.ReasonCode, Detail: detail}); err != nil {
				return err
			}
		}
		if _, err = r.Authority.CheckTx(ctx, tx, rc, true); err != nil {
			return err
		}
		key, request := hashes(cmd)
		err = sqlcgen.New(tx).TenantSettingsReceiptInsert(ctx, sqlcgen.TenantSettingsReceiptInsertParams{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, MembershipID: rc.MembershipID, KeyHash: key[:], RequestHash: request[:], ResponseStatus: int32(out.Status), ResponseEtag: out.ETag, ResponseBody: out.Body})
		return err
	})
	return out, err
}
