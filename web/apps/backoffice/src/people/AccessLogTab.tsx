import { formatDateTime, useTranslation } from '@kapsora/i18n';
import { Badge, ProblemAlert, Spinner, TBody, TD, TH, THead, TR, Table } from '@kapsora/ui';

import { useAccessLog } from '../claims/queries';
import { problemOf } from '../problems';
import { useActorLabel } from '../worklist/queries';

/**
 * Who looked at this person's clinical data and why — and who tried. A refusal is a row
 * like a look, because "who tried" is as much of the record as "who saw".
 */
export function AccessLogTab({ personId }: { personId: string }) {
  const { t } = useTranslation();
  const query = useAccessLog({ personId, limit: 100 });
  const actorLabel = useActorLabel();

  if (query.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (query.error) return <ProblemAlert problem={problemOf(query.error)} />;
  const rows = query.data?.items ?? [];

  return (
    <div className="grid gap-3" data-testid="access-log-tab">
      <p className="text-fg-muted text-sm">{t('people.accessLog.intro')}</p>
      {rows.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('people.accessLog.empty')}</p>
      ) : (
        <Table data-testid="access-log">
          <THead>
            <TR>
              <TH>{t('people.accessLog.columns.at')}</TH>
              <TH>{t('people.accessLog.columns.outcome')}</TH>
              <TH>{t('people.accessLog.columns.actor')}</TH>
              <TH>{t('people.accessLog.columns.resource')}</TH>
              <TH>{t('people.accessLog.columns.access')}</TH>
              <TH>{t('people.accessLog.columns.purpose')}</TH>
              <TH>{t('people.accessLog.columns.reason')}</TH>
            </TR>
          </THead>
          <TBody>
            {rows.map((e) => (
              <TR key={e.id} data-outcome={e.outcome}>
                <TD>{formatDateTime(e.occurredAt)}</TD>
                <TD>
                  <Badge tone={e.outcome === 'DENIED' ? 'danger' : 'success'}>
                    {t(`people.accessLog.outcome.${e.outcome}`)}
                  </Badge>
                </TD>
                <TD>{actorLabel(e.actorId)}</TD>
                <TD className="font-mono">{e.resourceType}</TD>
                <TD>{t(`people.accessLog.access.${e.accessType}`)}</TD>
                <TD>
                  {e.purposeCode
                    ? t(`review.purpose.codes.${e.purposeCode}`, { defaultValue: e.purposeCode })
                    : '—'}
                </TD>
                <TD className="break-words">{e.reasonText ?? '—'}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
    </div>
  );
}
