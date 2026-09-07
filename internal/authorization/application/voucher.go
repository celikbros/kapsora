package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// IssueVoucherInput is the issue command.
type IssueVoucherInput struct {
	AuthorizationID uuid.UUID
	ValidFrom       *time.Time
	ValidTo         *time.Time
	// Replace revokes whatever live voucher the authorization already has instead of
	// refusing. It is the reissue: a member who lost the code they were shown gets a new
	// one, and the old digest stops working in the same transaction.
	Replace bool
	// RevokeReasonCode is written on the voucher Replace withdrew. The column has a CHECK
	// requiring one, because "this voucher stopped working" with no reason is a support
	// call nobody can answer.
	RevokeReasonCode string
}

// RedeemVoucherInput is the redeem command. Token is the plaintext the member presented;
// it is hashed immediately and is never stored, logged or audited.
type RedeemVoucherInput struct {
	Token             string
	ProviderProfileID *uuid.UUID
	LocationID        *uuid.UUID
	PractitionerID    *uuid.UUID
	PerformedAt       time.Time
	Items             []domain.FulfilmentItemInput
}

// IssueVoucher generates the token the member shows at the counter and returns it once.
// What is stored is the SHA-256 digest and a masked tail; the plaintext leaves this
// function in the result and reaches no column, no log line and no audit row.
func (s *Service) IssueVoucher(ctx context.Context, rc identity.RequestContext,
	in IssueVoucherInput,
) (IssuedVoucher, error) {
	var out IssuedVoucher
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		issued, err := s.IssueVoucherInTx(ctx, tx, rc, in)
		out = issued
		return err
	})
	if err != nil {
		return IssuedVoucher{}, err
	}
	return out, nil
}

// IssueVoucherInTx is the body of IssueVoucher inside a transaction the caller owns, so a
// module that mints a voucher as part of its own command (WP-I6-02's booking) commits the
// voucher and its own row together or not at all.
//
// `Replace` is what separates the two callers. Issuing refuses a second live voucher,
// because a double-clicked issue would otherwise leave the member holding two usable tokens
// for one promise. Reissuing is the member saying they lost the first one: the live voucher
// is revoked here, in the same transaction, so the old digest stops working at exactly the
// moment the new one starts.
func (s *Service) IssueVoucherInTx(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in IssueVoucherInput,
) (IssuedVoucher, error) {
	var out IssuedVoucher
	err := func() error {
		authorization, err := s.repo.GetAuthorization(ctx, tx, rc.TenantID, in.AuthorizationID, scopeOf(rc))
		if err != nil {
			return err
		}
		if !domain.Open(authorization.Status) {
			return ErrAuthorizationNotActive
		}
		// Issuing carries no Idempotency-Key: the middleware persists the response body,
		// and this response is the only place the plaintext token exists. So the second
		// arrival of a double-clicked issue has to be refused here instead, or the member
		// walks away holding two usable tokens for one promise.
		// uq_voucher_one_live_per_authorization is the guarantee; this is the message.
		live, err := s.repo.CountLiveVouchers(ctx, tx, rc.TenantID, authorization.ID)
		if err != nil {
			return err
		}
		if live > 0 {
			if !in.Replace {
				return ErrVoucherAlreadyIssued
			}
			if err := s.revokeLiveVouchers(ctx, tx, rc, authorization.ID, in.RevokeReasonCode); err != nil {
				return err
			}
		}
		validFrom, validTo := authorization.ValidFrom, authorization.ValidTo
		if in.ValidFrom != nil {
			validFrom = in.ValidFrom.UTC()
		}
		if in.ValidTo != nil {
			validTo = in.ValidTo.UTC()
		}
		if err := domain.ValidateVoucherWindow(validFrom, validTo,
			authorization.ValidFrom, authorization.ValidTo); err != nil {
			return err
		}

		token, err := domain.NewToken()
		if err != nil {
			return err
		}
		masked := domain.MaskToken(token)
		voucher, err := s.repo.CreateVoucher(ctx, tx, rc.TenantID, NewVoucherRow{
			AuthorizationID: authorization.ID, TokenHash: domain.TokenHash(token),
			TokenMasked: masked, ValidFrom: validFrom, ValidTo: validTo,
			ActorID: actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		// The voucher is identified by the row's resource id, which is enough: anything
		// resembling the token itself would turn the audit log into a list of usable
		// vouchers. A "masked_token" detail was written here and silently dropped —
		// audit.SanitizeDetail refuses every key containing "token" — so the comment
		// claiming the masked tail was recorded described something that never happened.
		if err := s.record(ctx, tx, rc, "voucher.issue", "voucher", voucher.ID, map[string]any{
			"authorization_id": authorization.ID.String(),
			"valid_to":         validTo.Format(time.RFC3339),
		}); err != nil {
			return err
		}
		out = IssuedVoucher{Voucher: voucher, Token: token}
		return nil
	}()
	if err != nil {
		return IssuedVoucher{}, err
	}
	return out, nil
}

// revokeLiveVouchers withdraws whatever is still usable on this promise, so the reissue
// below can mint a replacement. Every revocation is its own audit row: a voucher that
// stopped working with nothing recorded is a support call nobody can answer.
func (s *Service) revokeLiveVouchers(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	authorizationID uuid.UUID, reasonCode string,
) error {
	vouchers, err := s.repo.ListVouchers(ctx, tx, rc.TenantID, authorizationID)
	if err != nil {
		return err
	}
	for _, voucher := range vouchers {
		if voucher.Status != domain.VoucherIssued {
			continue
		}
		revoked, err := s.repo.MarkVoucherRevoked(ctx, tx, rc.TenantID, voucher.ID, reasonCode)
		if err != nil {
			return err
		}
		if !revoked {
			continue
		}
		if err := s.record(ctx, tx, rc, "voucher.revoke", "voucher", voucher.ID, map[string]any{
			"authorization_id": authorizationID.String(), "reason_code": reasonCode,
		}); err != nil {
			return err
		}
	}
	return nil
}

// RedeemVoucher marks the voucher used and records the fulfilment it was presented for,
// in one transaction. That is the whole point of the command: a fulfilment that fails
// rolls the redemption back with it, so a member is never left holding a voucher the
// system believes was spent, and a second redemption is refused.
func (s *Service) RedeemVoucher(ctx context.Context, rc identity.RequestContext,
	in RedeemVoucherInput,
) (FulfilmentView, error) {
	if in.Token == "" {
		return FulfilmentView{}, fieldError("token", "REQUIRED", "kupon kodu zorunlu")
	}
	if err := domain.ValidateNewFulfilment(domain.NewFulfilment{
		PerformedAt: in.PerformedAt, Items: in.Items,
	}); err != nil {
		return FulfilmentView{}, err
	}

	var out FulfilmentView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		scope := scopeOf(rc)
		// The lookup argument is the digest, never the token: a statement log that kept
		// query parameters would otherwise be a list of vouchers somebody could spend.
		voucher, err := s.repo.LockVoucherByTokenHash(ctx, tx, rc.TenantID,
			domain.TokenHash(in.Token), scope)
		if err != nil {
			return err
		}
		if err := redeemable(voucher, s.now().UTC()); err != nil {
			return err
		}
		authorization, err := s.repo.LockAuthorization(ctx, tx, rc.TenantID, voucher.AuthorizationID, scope)
		if err != nil {
			return err
		}
		record, err := s.recordFulfilment(ctx, tx, rc, authorization, NewFulfilmentInput{
			AuthorizationID: authorization.ID, ProviderProfileID: in.ProviderProfileID,
			LocationID: in.LocationID, PractitionerID: in.PractitionerID,
			PerformedAt: in.PerformedAt, Items: in.Items,
		})
		if err != nil {
			return err
		}
		redeemed, err := s.repo.MarkVoucherRedeemed(ctx, tx, rc.TenantID, voucher.ID,
			s.now().UTC(), actorPtr(rc.Principal.ActorID))
		if err != nil {
			return err
		}
		// The row was ISSUED when it was locked, so a write that changed nothing means
		// somebody else redeemed it between the lock and here — which cannot happen with
		// the lock held, and is therefore worth refusing loudly rather than ignoring.
		if !redeemed {
			return ErrVoucherAlreadyRedeemed
		}
		if err := s.record(ctx, tx, rc, "voucher.redeem", "voucher", voucher.ID, map[string]any{
			"authorization_id": authorization.ID.String(), "fulfilment_id": record.ID.String(),
		}); err != nil {
			return err
		}
		out, err = s.reloadFulfilment(ctx, tx, rc.TenantID, record.ID, scope)
		return err
	})
	return out, err
}

// redeemable refuses a voucher that has already been spent, been withdrawn, or is being
// presented outside its window. Each refusal has its own code, because "this does not
// work" tells the person at the counter nothing they can act on.
func redeemable(v VoucherRecord, now time.Time) error {
	switch v.Status {
	case domain.VoucherRedeemed:
		return ErrVoucherAlreadyRedeemed
	case domain.VoucherRevoked:
		return ErrVoucherRevoked
	case domain.VoucherExpired:
		return ErrVoucherExpired
	}
	if now.Before(v.ValidFrom) || !now.Before(v.ValidTo) {
		return ErrVoucherExpired
	}
	return nil
}
