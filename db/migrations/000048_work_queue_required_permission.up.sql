-- 000048: the permission a queue's work takes.
--
-- The worklist showed every item in the tenant to anybody who could read a worklist, and the
-- only narrowing was a WORK_QUEUE grant nobody is given by default. A financial reviewer
-- therefore saw the medical review queue, could open a report they may not read, and — worse —
-- could claim the item, taking it off every doctor's list without being able to decide it.
--
-- A queue now names the permission its work takes. The worklist reads and the claim both apply
-- it, so the default for a new queue is "whoever can do the work sees it" rather than
-- "everybody sees it". `worklist.read` as the default keeps that meaning for a queue nobody has
-- configured: it is the permission the worklist itself takes, so such a queue is exactly as
-- visible as it was before.
--
-- The five queues the demo and every implementation so far raise work into are given the
-- permission that work actually takes; a tenant that has invented its own queues keeps the
-- default until somebody sets one.
ALTER TABLE workflow.work_queue
    ADD COLUMN required_permission text NOT NULL DEFAULT 'worklist.read'
        REFERENCES iam.permission(code) ON DELETE RESTRICT;

UPDATE workflow.work_queue q
   SET required_permission = v.permission
  FROM (VALUES
            ('MEDICAL_REVIEW',            'health.medical_report.review'),
            ('FINANCIAL_REVIEW',          'claim.financial.review'),
            ('BATCH_REVIEW',              'batch.review'),
            ('RECONCILIATION_DIFFERENCE', 'settlement.read'),
            ('RESERVATION_REVIEW',        'accommodation.booking.manage')
       ) AS v(code, permission)
 WHERE q.code = v.code;

COMMENT ON COLUMN workflow.work_queue.required_permission IS
    'The permission this queue''s work takes; the worklist and the claim apply it.';
