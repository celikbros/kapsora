-- Preserve the conversion used when a service line reserved entitlement. Existing
-- authorizations reserved service quantity directly, so their historical factor is 1.
ALTER TABLE service.authorization_item
    ADD COLUMN entitlement_unit_factor numeric(20,6) NOT NULL DEFAULT 1
    CHECK (entitlement_unit_factor > 0);
