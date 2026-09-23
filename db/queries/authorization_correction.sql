-- name: FindAuthorizationConsumption :one
-- A correction can undo only this authorization item's exact original consumption.
SELECT l.id, l.delta_consumed::text AS quantity,
       EXISTS (SELECT 1 FROM benefit.entitlement_ledger r
         WHERE r.tenant_id=l.tenant_id AND r.movement_type='REVERSE' AND r.reference_id=l.id) AS reversed
FROM benefit.entitlement_ledger l
WHERE l.tenant_id=sqlc.arg('tenant_id') AND l.reservation_id=sqlc.arg('reservation_id')
  AND l.reference_type='AUTHORIZATION'
  AND l.reference_id IN (SELECT ai.id FROM service.authorization_item ai
    WHERE ai.tenant_id=l.tenant_id AND ai.authorization_id=sqlc.arg('authorization_id')
      AND ai.entitlement_reservation_id=l.reservation_id)
  AND l.movement_type='CONSUME' AND l.idempotency_key=sqlc.arg('key')
  AND l.reason_code=sqlc.arg('reason_code');

-- name: RestoreAuthorizationConsumption :execrows
UPDATE service.authorization
SET consumed_total=consumed_total-sqlc.arg('quantity')::text::numeric,
    status=CASE
      WHEN status IN ('CANCELLED','EXPIRED') THEN status
      WHEN valid_to<=sqlc.arg('now')::timestamptz THEN 'EXPIRED'
      WHEN consumed_total-sqlc.arg('quantity')::text::numeric=0 THEN 'ACTIVE'
      ELSE 'PARTIALLY_USED' END,
    updated_by=sqlc.narg('actor_id')
WHERE tenant_id=sqlc.arg('tenant_id') AND id=sqlc.arg('id')
  AND consumed_total>=sqlc.arg('quantity')::text::numeric;
