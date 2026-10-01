import type { HealthAccessEvent } from '@kapsora/api-client';
import { useSession } from '@kapsora/auth';
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
  useMinWidth,
} from '@kapsora/ui';
import { useState } from 'react';
import { useAccessLog } from '../claims/queries';
import { problemOf } from '../problems';
import { useActorLabel } from '../worklist/queries';

const PAGE_SIZE = 50;

/** Tenant-wide health data access events for staff with audit.read. */
export function SecurityPage() {
  const { t } = useTranslation();
  const tenantId = useSession((s) => s.activeTenant?.tenant.id ?? null);
  const actorId = useSession((s) => s.session?.actorId ?? null);
  const canAudit = useSession((s) => s.activeTenant?.permissions.includes('audit.read') ?? false);
  const contextKey = `${tenantId ?? ''}:${actorId ?? ''}:${canAudit}`;

  return (
    <section data-testid="health-access-log-page">
      <PageHeader title={t('securityAccess.title')} description={t('securityAccess.intro')} />
      {canAudit ? (
        <HealthAccessLogList key={contextKey} />
      ) : (
        <EmptyState title={t('problems.PERMISSION_DENIED')} />
      )}
    </section>
  );
}

function HealthAccessLogList() {
  const { t } = useTranslation();
  const wide = useMinWidth(768);
  const actorLabel = useActorLabel();
  const [cursors, setCursors] = useState<(string | null)[]>([null]);
  const [index, setIndex] = useState(0);
  const cursor = cursors[index] ?? null;
  const log = useAccessLog({ limit: PAGE_SIZE, ...(cursor ? { cursor } : {}) });
  const rows = log.data?.items ?? [];
  const nextCursor = log.data?.nextCursor ?? null;
  const canNext = !!nextCursor && !cursors.slice(0, index + 1).includes(nextCursor);

  function next() {
    if (!canNext || !nextCursor) return;
    setCursors((current) => [...current.slice(0, index + 1), nextCursor]);
    setIndex(index + 1);
  }

  function previous() {
    if (index > 0) setIndex(index - 1);
  }

  function outcome(event: HealthAccessEvent) {
    return (
      <Badge tone={event.outcome === 'DENIED' ? 'danger' : 'success'}>
        {t(`people.accessLog.outcome.${event.outcome}`)}
      </Badge>
    );
  }

  function purpose(event: HealthAccessEvent) {
    return event.purposeCode
      ? t(`review.purpose.codes.${event.purposeCode}`, { defaultValue: event.purposeCode })
      : '—';
  }

  return (
    <div className="grid gap-3">
      {log.isPending ? (
        <p className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </p>
      ) : log.isError ? (
        <div className="grid justify-items-start gap-2">
          <ProblemAlert problem={problemOf(log.error)} />
          <Button
            type="button"
            variant="secondary"
            onClick={() => void log.refetch()}
            data-testid="health-access-log-retry"
          >
            {t('securityAccess.retry')}
          </Button>
        </div>
      ) : rows.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('securityAccess.empty')}</p>
      ) : !wide ? (
        <ul className="grid gap-2">
          {rows.map((event) => (
            <li
              key={event.id}
              data-testid="health-access-log-row"
              className="bg-surface-raised border-line grid gap-2 rounded-lg border p-3 text-sm"
            >
              <div className="flex items-baseline justify-between gap-2">
                <span>{formatDateTime(event.occurredAt)}</span>
                {outcome(event)}
              </div>
              <p>
                {t('securityAccess.actor')}: {actorLabel(event.actorId)}
              </p>
              <p className="break-all">
                {t('securityAccess.person')}: {event.personId ?? '—'}
              </p>
              <p className="break-all">
                {t('securityAccess.resource')}: {event.resourceType} · {event.resourceId ?? '—'}
              </p>
              <p>
                {t('securityAccess.access')}: {t(`people.accessLog.access.${event.accessType}`)}
              </p>
              <p>
                {t('securityAccess.purpose')}: {purpose(event)}
              </p>
              <p className="break-words">
                {t('securityAccess.reason')}: {event.reasonText ?? '—'}
              </p>
            </li>
          ))}
        </ul>
      ) : (
        <div className="relative overflow-x-auto">
          <Table>
            <THead>
              <TR>
                <TH>{t('securityAccess.at')}</TH>
                <TH>{t('securityAccess.outcome')}</TH>
                <TH>{t('securityAccess.actor')}</TH>
                <TH>{t('securityAccess.person')}</TH>
                <TH>{t('securityAccess.resource')}</TH>
                <TH>{t('securityAccess.access')}</TH>
                <TH>{t('securityAccess.purpose')}</TH>
                <TH>{t('securityAccess.reason')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((event) => (
                <TR key={event.id} data-testid="health-access-log-row">
                  <TD className="whitespace-nowrap">{formatDateTime(event.occurredAt)}</TD>
                  <TD>{outcome(event)}</TD>
                  <TD className="font-mono text-xs">{actorLabel(event.actorId)}</TD>
                  <TD className="font-mono text-xs">{event.personId ?? '—'}</TD>
                  <TD className="font-mono text-xs">
                    {event.resourceType} · {event.resourceId ?? '—'}
                  </TD>
                  <TD>{t(`people.accessLog.access.${event.accessType}`)}</TD>
                  <TD>{purpose(event)}</TD>
                  <TD className="break-words">{event.reasonText ?? '—'}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
      {index > 0 || canNext ? (
        <div className="flex gap-2">
          <Button
            type="button"
            variant="secondary"
            disabled={index === 0 || log.isPending}
            onClick={previous}
            data-testid="health-access-log-previous"
          >
            {t('securityAccess.previous')}
          </Button>
          <Button
            type="button"
            variant="secondary"
            disabled={!canNext || log.isPending}
            onClick={next}
            data-testid="health-access-log-next"
          >
            {t('securityAccess.next')}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
