-- Optional exact evidence for confirmed no-show entitlement settlement. Existing reports
-- retain NULL, including historical confirmations; undecided reports have no movement.
ALTER TABLE accommodation.no_show
    ADD COLUMN consumed_service_nights numeric(20,6),
    ADD COLUMN released_service_nights numeric(20,6),
    ADD COLUMN consumed_entitlement_units numeric(20,6),
    ADD COLUMN released_entitlement_units numeric(20,6),
    ADD CONSTRAINT ck_no_show_entitlement_effect_all_or_none CHECK (
        (consumed_service_nights IS NULL AND released_service_nights IS NULL
         AND consumed_entitlement_units IS NULL AND released_entitlement_units IS NULL)
        OR
        (status = 'CONFIRMED'
         AND consumed_service_nights IS NOT NULL AND released_service_nights IS NOT NULL
         AND consumed_entitlement_units IS NOT NULL AND released_entitlement_units IS NOT NULL
         AND consumed_service_nights >= 0 AND released_service_nights >= 0
         AND consumed_entitlement_units >= 0 AND released_entitlement_units >= 0)
    );
