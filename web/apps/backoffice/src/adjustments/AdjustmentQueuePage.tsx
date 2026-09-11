import { ApiError, type EntitlementAdjustment } from '@kapsora/api-client';
import { usePermission, useSelfPersonId, useSession } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  EmptyState,
  PageHeader,
  ProblemAlert,
  Spinner,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  type BadgeTone,
  OwnFileNotice,
} from '@kapsora/ui';
import { useState } from 'react';

import { AdjustmentDialog, type AdjustmentDecision } from './AdjustmentDialog';
import { useAdjustmentQueue, useEntitlementAccount } from './queries';
import { isNegativeDecimal, quantityText, signedQuantityText } from './quantity';

const PAGE_SIZE = 25;

const statusTones: Record<EntitlementAdjustment['status'], BadgeTone> = {
  PENDING: 'info',
  APPROVED: 'success',
  REJECTED: 'danger',
};

/** What the decision dialog is currently working on. */
interface Decision {
  decision: AdjustmentDecision;
  adjustment: EntitlementAdjustment;
}

/**
 * One queued adjustment. The account is fetched per row because the queue carries only
 * its id, and an approver has to see which entitlement and which balance the delta
 * lands on before deciding.
 */
function QueueRow({
  adjustment,
  mine,
  canDecide,
  onDecide,
}: {
  adjustment: EntitlementAdjustment;
  mine: boolean;
  canDecide: boolean;
  onDecide: (decision: Decision) => void;
}) {
  const { t } = useTranslation();
  const account = useEntitlementAccount(adjustment.accountId);
  const balances = account.data?.data;
  const definition = balances?.definition;
  const negative = isNegativeDecimal(adjustment.deltaQuantity);
  const self = useSelfPersonId();
  // A change to the reviewer's own balance is decided by somebody else.
  const own = self !== null && balances?.personId === self;

  return (
    <TR>
      <TD>
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{definition?.name ?? t('common.loading')}</span>
          {definition ? (
            <code className="text-fg-muted font-mono text-xs">{definition.code}</code>
          ) : null}
          {balances && balances.status !== 'OPEN' ? (
            <Badge tone="warning">{t(`entitlements.statuses.${balances.status}`)}</Badge>
          ) : null}
        </div>
        {balances ? (
          <p className="text-fg-muted text-xs">
            {t('entitlements.columns.available')}:{' '}
            <span className="font-mono">
              {quantityText(balances.available, definition?.unitType, definition?.currencyCode)}
            </span>
          </p>
        ) : null}
      </TD>
      <TD>
        <Badge tone={negative ? 'danger' : 'success'}>
          <span className="font-mono">
            {signedQuantityText(
              adjustment.deltaQuantity,
              definition?.unitType,
              definition?.currencyCode,
            )}
          </span>
        </Badge>
      </TD>
      <TD>
        <code className="font-mono text-xs">{adjustment.reasonCode}</code>
        {adjustment.reasonText ? (
          <p className="text-fg-muted text-xs">{adjustment.reasonText}</p>
        ) : null}
      </TD>
      <TD>
        <code className="font-mono text-xs">{adjustment.requestedBy}</code>
        <p className="text-fg-muted text-xs">{formatDateTime(adjustment.requestedAt)}</p>
      </TD>
      <TD>
        <Badge tone={statusTones[adjustment.status]}>
          {t(`entitlements.adjustments.statuses.${adjustment.status}`)}
        </Badge>
      </TD>
      <TD>
        {!canDecide ? null : mine ? (
          // Maker-checker: the requester never gets a control, and the reason says why.
          <p className="text-fg-muted max-w-prose text-xs">
            {t('entitlements.adjustments.sameActorBlocked')}
          </p>
        ) : own ? (
          <OwnFileNotice />
        ) : (
          // Both open a confirmation; the committing button lives there, so the row keeps
          // the quiet weight DESIGN.md asks of a list.
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              variant="secondary"
              onClick={() => onDecide({ decision: 'approve', adjustment })}
            >
              {t('entitlements.adjustments.approve')}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => onDecide({ decision: 'reject', adjustment })}
            >
              {t('entitlements.adjustments.reject')}
            </Button>
          </div>
        )}
      </TD>
    </TR>
  );
}

/**
 * The maker-checker queue of manual entitlement adjustments: what each one would move,
 * onto which account, why and who asked for it. Approving or rejecting takes a second
 * actor and a password re-entry; the server is the authority on both and this screen
 * renders its refusal when the list has gone stale.
 */
export function AdjustmentQueuePage() {
  const { t } = useTranslation();
  const canDecide = usePermission('entitlement.adjust');
  const actorId = useSession((s) => s.session?.actorId ?? null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [trail, setTrail] = useState<Array<string | null>>([]);
  const [decision, setDecision] = useState<Decision | null>(null);

  const query = useAdjustmentQueue({
    status: 'PENDING',
    limit: PAGE_SIZE,
    ...(cursor ? { cursor } : {}),
  });

  function goNext() {
    const next = query.data?.nextCursor;
    if (!next) return;
    setTrail((previous) => [...previous, cursor]);
    setCursor(next);
  }

  function goPrevious() {
    setCursor(trail[trail.length - 1] ?? null);
    setTrail((previous) => previous.slice(0, -1));
  }

  const rows = query.data?.items ?? [];
  const problem = query.error instanceof ApiError ? query.error.problem : null;

  return (
    <>
      <PageHeader title={t('entitlements.adjustments.queueTitle')} />

      <ProblemAlert
        page
        problem={problem}
        className="mb-4"
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />

      {query.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <EmptyState title={t('entitlements.adjustments.empty')} />
      ) : (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="adjustment-table">
            <THead>
              <TR>
                <TH>{t('entitlements.adjustments.columns.account')}</TH>
                <TH title={t('entitlements.adjustments.deltaHint')}>
                  {t('entitlements.adjustments.columns.delta')}
                </TH>
                <TH>{t('entitlements.adjustments.columns.reason')}</TH>
                <TH>{t('entitlements.adjustments.columns.requestedBy')}</TH>
                <TH>{t('entitlements.adjustments.columns.status')}</TH>
                <TH>{t('common.actions')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((adjustment) => (
                <QueueRow
                  key={adjustment.id}
                  adjustment={adjustment}
                  mine={adjustment.requestedBy === actorId}
                  canDecide={canDecide}
                  onDecide={setDecision}
                />
              ))}
            </TBody>
          </Table>
          <nav className="mt-3 flex items-center justify-between text-sm" aria-label="Sayfalama">
            <span className="text-fg-muted">
              {t('organizations.page', { n: trail.length + 1 })}
            </span>
            <div className="flex gap-2">
              <Button
                variant="secondary"
                size="sm"
                onClick={goPrevious}
                disabled={trail.length === 0}
              >
                {t('organizations.prevPage')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={goNext}
                disabled={!query.data?.nextCursor}
              >
                {t('organizations.nextPage')}
              </Button>
            </div>
          </nav>
        </>
      )}

      <AdjustmentDialog
        decision={decision?.decision ?? 'approve'}
        adjustment={decision?.adjustment ?? null}
        onClose={() => setDecision(null)}
      />
    </>
  );
}
