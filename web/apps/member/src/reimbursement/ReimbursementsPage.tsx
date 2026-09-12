import { formatDate, useTranslation } from '@kapsora/i18n';
import { Badge, Card, HelpHint, ProblemAlert } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';

import { Loading } from '../lodging/Loading';
import { money } from '../lodging/words';
import { problemOf } from '../problems';
import { useMyReimbursements } from './queries';
import { reimbursementTone } from './words';

/** A link that reads as the page's one action; the classes are the primary button's. */
const BUTTON_LINK =
  'bg-primary text-primary-fg hover:bg-primary-hover inline-flex items-center justify-center rounded-md px-4 py-2 text-sm font-medium';

/** Geri ödemelerim: every request, newest first, with what was asked and what was agreed. */
export function ReimbursementsPage() {
  const { t } = useTranslation();
  const rows = useMyReimbursements();

  return (
    <div className="grid gap-4 p-4">
      <Link to="/" className="text-primary text-sm underline-offset-4 hover:underline">
        ← {t('billing.member.back')}
      </Link>
      <div className="flex items-center gap-1.5">
        <h1 className="text-xl font-semibold">{t('billing.member.title')}</h1>
        <HelpHint term="geriOdeme" />
      </div>
      <p className="text-fg-muted text-sm">{t('billing.member.intro')}</p>
      <Link
        to="/reimbursements/new"
        className={`${BUTTON_LINK} justify-self-start`}
        data-testid="new-reimbursement"
      >
        {t('billing.member.new')}
      </Link>
      {rows.isPending ? (
        <Loading />
      ) : rows.isError ? (
        <ProblemAlert problem={problemOf(rows.error)} />
      ) : rows.data.items.length === 0 ? (
        <Card>
          <p className="text-sm">{t('billing.member.empty')}</p>
        </Card>
      ) : (
        <ul className="grid gap-2" data-testid="reimbursement-list">
          {rows.data.items.map((r) => (
            <li key={r.id}>
              <Link
                to="/reimbursements/$reimbursementId"
                params={{ reimbursementId: r.id }}
                className="bg-surface-raised border-line block rounded-lg border p-3 focus-visible:outline-2"
                data-testid="reimbursement-row"
              >
                <span className="flex items-baseline justify-between gap-3">
                  <span className="font-mono text-xs">{r.reference}</span>
                  <Badge tone={reimbursementTone(r.status)}>
                    {t(`billing.reimbursementStatus.${r.status}`)}
                  </Badge>
                </span>
                <span className="text-fg-muted mt-1 flex justify-between text-sm">
                  <span>{formatDate(r.serviceDate)}</span>
                  <span className="text-fg font-mono tabular-nums">
                    {r.approvedAmount
                      ? money(r.approvedAmount, r.currencyCode)
                      : money(r.requestedAmount, r.currencyCode)}
                  </span>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
