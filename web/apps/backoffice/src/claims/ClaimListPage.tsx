import type { ClaimStatus } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  EmptyState,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
} from '@kapsora/ui';
import { Link, useNavigate, useSearch } from '@tanstack/react-router';

import { problemOf } from '../problems';
import { useOrganizationName, usePersonName } from './names';
import { useClaims } from './queries';
import { claimTone } from './status';

export interface ClaimListSearch {
  status?: ClaimStatus;
  cursor?: string;
}

const STATUSES: ClaimStatus[] = [
  'PENDING_MEDICAL',
  'PENDING_FINANCIAL',
  'RETURNED',
  'APPROVED',
  'PARTIALLY_APPROVED',
  'REJECTED',
  'DRAFT',
  'CANCELLED',
];

function PersonCell({ personId }: { personId: string }) {
  const name = usePersonName(personId);
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}
function ProviderCell({ organizationId }: { organizationId: string }) {
  const name = useOrganizationName(organizationId);
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}

/** The claims of the tenant, densest first: the reference, where it stands, whose, from whom. */
export function ClaimListPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const search = useSearch({ from: '/app/claims' });
  const canReadOrganizations = usePermission('organization.read');
  const query = useClaims({
    ...(search.status ? { status: search.status } : {}),
    ...(search.cursor ? { cursor: search.cursor } : {}),
    limit: 50,
  });
  const rows = query.data?.items ?? [];

  return (
    <>
      <PageHeader title={t('claims.title')} description={t('claims.intro')} />
      <div className="mb-3 flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('claims.filters.status')}</span>
          <Select
            name="status"
            value={search.status ?? ''}
            onChange={(e) =>
              void navigate({
                to: '/claims',
                search: e.target.value ? { status: e.target.value as ClaimStatus } : {},
              })
            }
            options={[
              { value: '', label: t('claims.filters.all') },
              ...STATUSES.map((s) => ({ value: s, label: t(`claims.status.${s}`) })),
            ]}
          />
        </label>
      </div>
      {query.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : query.error ? (
        <ProblemAlert problem={problemOf(query.error)} />
      ) : rows.length === 0 ? (
        <EmptyState title={t('claims.empty')} />
      ) : (
        <Table data-testid="claim-table">
          <THead>
            <TR>
              <TH>{t('claims.columns.reference')}</TH>
              <TH>{t('claims.columns.status')}</TH>
              <TH>{t('claims.columns.member')}</TH>
              {canReadOrganizations ? <TH>{t('claims.columns.provider')}</TH> : null}
              <TH>{t('claims.columns.serviceDates')}</TH>
              <TH className="text-right">{t('claims.columns.lines')}</TH>
            </TR>
          </THead>
          <TBody>
            {rows.map((row) => (
              <TR key={row.id} data-status={row.status}>
                <TD>
                  <Link
                    to="/claims/$claimId"
                    params={{ claimId: row.id }}
                    className="font-mono underline-offset-2 hover:underline"
                  >
                    {row.reference}
                  </Link>
                </TD>
                <TD>
                  <Badge tone={claimTone(row.status)}>{t(`claims.status.${row.status}`)}</Badge>
                </TD>
                <TD>
                  <PersonCell personId={row.personId} />
                </TD>
                {canReadOrganizations ? (
                  <TD>
                    <ProviderCell organizationId={row.providerOrganizationId} />
                  </TD>
                ) : null}
                <TD>
                  {formatDate(row.serviceDateFrom)} – {formatDate(row.serviceDateTo)}
                </TD>
                <TD className="text-right font-mono">{row.lines.length}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
    </>
  );
}
