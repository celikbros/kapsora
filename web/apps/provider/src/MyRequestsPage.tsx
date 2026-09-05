import type { ServiceRequest, ServiceRequestStatus } from '@kapsora/api-client';
import { formatDate, useTranslation } from '@kapsora/i18n';
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
} from '@kapsora/ui';
import { Link } from '@tanstack/react-router';

import { useMyRequests, usePersonName, useServiceName } from './queries';
import { problemOf } from './problems';

/** Tone follows what the row asks of the desk: waiting on us is the one that matters. */
function tone(status: ServiceRequestStatus): BadgeTone {
  switch (status) {
    case 'PENDING_DOCUMENT':
      return 'warning';
    case 'APPROVED':
    case 'PARTIALLY_APPROVED':
      return 'success';
    case 'REJECTED':
    case 'ELIGIBILITY_FAILED':
      return 'danger';
    case 'PENDING_REVIEW':
      return 'info';
    default:
      return 'neutral';
  }
}

function PersonCell({ personId }: { personId: string }) {
  const name = usePersonName(personId);
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}
function ServiceCell({ request }: { request: ServiceRequest }) {
  const name = useServiceName(request.items[0]?.serviceDefinitionId);
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}

/**
 * The provider's own requests — the server scoped them, the screen adds no filter of its
 * own — and what each one is waiting for, in words a desk can act on. The verdict and
 * what it waits for sit next to the reference, so a narrow screen's scroller hides the
 * detail and never the answer.
 */
export function MyRequestsPage() {
  const { t } = useTranslation();
  const query = useMyRequests({ limit: 50 });
  const rows = query.data?.items ?? [];
  return (
    <>
      <PageHeader
        title={t('provider.myRequests.title')}
        description={t('provider.myRequests.intro')}
      />
      <ProblemAlert
        problem={query.error ? problemOf(query.error) : null}
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
        <EmptyState title={t('provider.myRequests.empty')} />
      ) : (
        <Table data-testid="my-requests-table">
          <THead>
            <TR>
              <TH>{t('requests.columns.reference')}</TH>
              <TH>{t('requests.columns.status')}</TH>
              <TH>{t('requests.columns.person')}</TH>
              <TH>{t('provider.newRequest.service')}</TH>
              <TH>{t('requests.columns.serviceDate')}</TH>
            </TR>
          </THead>
          <TBody>
            {rows.map((r) => (
              <TR key={r.id} data-status={r.status}>
                <TD>
                  <Link
                    to="/requests/$requestId"
                    params={{ requestId: r.id }}
                    className="font-mono text-xs font-medium underline-offset-2 hover:underline"
                  >
                    {r.reference}
                  </Link>
                </TD>
                <TD>
                  <Badge tone={tone(r.status)}>
                    {t(`provider.myRequests.waiting.${r.status}`)}
                  </Badge>
                </TD>
                <TD>
                  <PersonCell personId={r.personId} />
                </TD>
                <TD>
                  <ServiceCell request={r} />
                </TD>
                <TD>{formatDate(r.serviceDate)}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
    </>
  );
}
