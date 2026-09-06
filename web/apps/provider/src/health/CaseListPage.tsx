import type { HealthCase } from '@kapsora/api-client';
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

import { problemOf } from '../problems';
import { usePersonName, useCases } from './queries';
import { caseTone } from './words';

/** The person's name for a row that carries only the id: "…" while it loads, "—" when nobody has it. */
function NameCell({ personId }: { personId: string }) {
  const name = usePersonName(personId);
  return <>{name === undefined ? '…' : (name ?? '—')}</>;
}

/**
 * The provider's cases. A case has no reference of its own; the row is the person, the
 * kind of care and when it opened, which is how a desk remembers it.
 */
export function CaseListPage() {
  const { t } = useTranslation();
  const [status, setStatus] = useState('OPEN');
  const query = useCases(status);
  const rows: HealthCase[] = query.data?.items ?? [];

  return (
    <>
      <PageHeader
        title={t('health.cases.title')}
        description={t('health.cases.intro')}
        actions={
          <Link to="/cases/new">
            <Button size="sm">{t('health.cases.open')}</Button>
          </Link>
        }
      />
      <div className="mb-3 flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('health.cases.filters.status')}</span>
          <Select
            name="status"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            options={[
              { value: '', label: t('health.cases.filters.all') },
              { value: 'OPEN', label: t('health.caseStatus.OPEN') },
              { value: 'CLOSED', label: t('health.caseStatus.CLOSED') },
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
        <EmptyState title={t('health.cases.empty')} />
      ) : (
        <Table data-testid="case-table">
          <THead>
            <TR>
              <TH>{t('health.cases.columns.member')}</TH>
              <TH>{t('health.cases.columns.status')}</TH>
              <TH>{t('health.cases.columns.type')}</TH>
              <TH>{t('health.cases.columns.openedAt')}</TH>
              <TH className="text-right">{t('health.cases.columns.encounters')}</TH>
            </TR>
          </THead>
          <TBody>
            {rows.map((row) => (
              <TR key={row.id} data-status={row.status}>
                <TD>
                  <Link
                    to="/cases/$caseId"
                    params={{ caseId: row.id }}
                    className="font-medium underline-offset-2 hover:underline"
                  >
                    <NameCell personId={row.personId} />
                  </Link>
                </TD>
                <TD>
                  <Badge tone={caseTone(row.status)}>{t(`health.caseStatus.${row.status}`)}</Badge>
                </TD>
                <TD>{t(`health.caseType.${row.caseType}`)}</TD>
                <TD>{formatDate(row.openedAt)}</TD>
                <TD className="text-right font-mono">{row.encounters.length}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
    </>
  );
}
