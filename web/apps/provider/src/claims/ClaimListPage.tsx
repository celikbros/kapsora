import type { Claim } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
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
import { Link } from '@tanstack/react-router';
import { useState } from 'react';

import { usePersonName } from '../health/queries';
import { claimTone } from '../health/words';
import { problemOf } from '../problems';
import { useClaims } from './queries';

const STATUSES: Claim['status'][] = [
  'DRAFT',
  'PENDING_MEDICAL',
  'PENDING_FINANCIAL',
  'RETURNED',
  'APPROVED',
  'PARTIALLY_APPROVED',
  'REJECTED',
  'CANCELLED',
];

function NameCell({ personId }: { personId: string }) {
  const name = usePersonName(personId);
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}

/** The billing desk's list: the reference, where each claim stands, and what it covers. */
export function ClaimListPage() {
  const { t } = useTranslation();
  const canCreate = usePermission('claim.create');
  const [status, setStatus] = useState('');
  const query = useClaims(status);
  const rows = query.data?.items ?? [];

  return (
    <>
      <PageHeader
        title={t('claims.title')}
        description={t('claims.intro')}
        actions={
          canCreate ? (
            <Link to="/claims/new">
              <Button size="sm">{t('claims.new')}</Button>
            </Link>
          ) : undefined
        }
      />
      <div className="mb-3 flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('claims.filters.status')}</span>
          <Select
            name="status"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
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
              <TH>{t('claims.columns.serviceDates')}</TH>
              <TH className="text-right">{t('claims.columns.lines')}</TH>
              <TH className="text-right">{t('claims.columns.version')}</TH>
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
                  <NameCell personId={row.personId} />
                </TD>
                <TD>
                  {formatDate(row.serviceDateFrom)} – {formatDate(row.serviceDateTo)}
                </TD>
                <TD className="text-right font-mono">{row.lines.length}</TD>
                <TD className="text-right font-mono">v{row.currentVersionNo}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
    </>
  );
}
