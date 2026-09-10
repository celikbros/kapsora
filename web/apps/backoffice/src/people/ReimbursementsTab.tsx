import { formatDate, formatMoney, useTranslation } from '@kapsora/i18n';
import { Badge, ProblemAlert, Spinner, TBody, TD, TH, THead, TR, Table } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';

import { useReimbursements } from '../billing/queries';
import { reimbursementTone } from '../billing/status';
import { problemOf } from '../problems';

/** Geri ödemeler: the person's reimbursement requests, as they are. */
export function ReimbursementsTab({ personId }: { personId: string }) {
  const { t } = useTranslation();
  const rows = useReimbursements({ personId });

  return (
    <div className="grid min-w-0 gap-4" data-testid="reimbursements-tab">
      <p className="text-fg-muted text-sm">{t('billing.office.reimbursementsIntro')}</p>
      {rows.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.isError ? (
        <ProblemAlert problem={problemOf(rows.error)} />
      ) : rows.data.items.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('billing.office.reimbursementsEmpty')}</p>
      ) : (
        <div className="relative overflow-x-auto">
          <Table>
            <THead>
              <TR>
                <TH>{t('billing.office.reference')}</TH>
                <TH>{t('billing.office.serviceDate')}</TH>
                <TH>{t('billing.office.status')}</TH>
                <TH className="text-right">{t('billing.office.requested')}</TH>
                <TH className="text-right">{t('billing.office.approved')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.data.items.map((r) => (
                <TR key={r.id}>
                  <TD>
                    <Link
                      to="/billing/reimbursements/$reimbursementId"
                      params={{ reimbursementId: r.id }}
                      className="text-primary font-mono text-xs underline-offset-4 hover:underline"
                    >
                      {r.reference}
                    </Link>
                  </TD>
                  <TD className="whitespace-nowrap">{formatDate(r.serviceDate)}</TD>
                  <TD>
                    <Badge tone={reimbursementTone(r.status)}>
                      {t(`billing.reimbursementStatus.${r.status}`)}
                    </Badge>
                  </TD>
                  <TD className="text-right font-mono tabular-nums">
                    {formatMoney(r.requestedAmount, r.currencyCode)}
                  </TD>
                  <TD className="text-right font-mono tabular-nums">
                    {r.approvedAmount ? formatMoney(r.approvedAmount, r.currencyCode) : '—'}
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
    </div>
  );
}
