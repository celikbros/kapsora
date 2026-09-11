import type { Contract, ContractStatus } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  EmptyState,
  Input,
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
  statusTone,
} from '@kapsora/ui';
import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { useContracts } from './queries';

export interface ContractListSearch {
  q?: string;
  status?: ContractStatus;
  cursor?: string;
}

const PAGE_SIZE = 50;
const STATUSES: ContractStatus[] = ['DRAFT', 'ACTIVE', 'SUSPENDED', 'CLOSED'];

/** Contracts by their two parties: who pays and who delivers. */
export function ContractListPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const search = useSearch({ from: '/app/contracts' });
  const canManage = usePermission('contract.manage');
  const [q, setQ] = useState(search.q ?? '');
  const [trail, setTrail] = useState<string[]>([]);

  const query = useContracts({
    ...(search.q ? { q: search.q } : {}),
    ...(search.status ? { status: search.status } : {}),
    ...(search.cursor ? { cursor: search.cursor } : {}),
    limit: PAGE_SIZE,
  });

  function applyFilters(event: FormEvent) {
    event.preventDefault();
    setTrail([]);
    void navigate({
      to: '/contracts',
      search: {
        ...(q.trim() ? { q: q.trim() } : {}),
        ...(search.status ? { status: search.status } : {}),
      },
    });
  }

  function setStatus(status: string) {
    setTrail([]);
    void navigate({
      to: '/contracts',
      search: {
        ...(search.q ? { q: search.q } : {}),
        ...(status ? { status: status as ContractStatus } : {}),
      },
    });
  }

  function goNext() {
    const next = query.data?.nextCursor;
    if (!next) return;
    setTrail((previous) => [...previous, search.cursor ?? '']);
    void navigate({ to: '/contracts', search: { ...search, cursor: next } });
  }

  function goPrevious() {
    const previous = trail[trail.length - 1];
    setTrail((rest) => rest.slice(0, -1));
    const { cursor: _cursor, ...others } = search;
    void navigate({
      to: '/contracts',
      search: previous ? { ...others, cursor: previous } : others,
    });
  }

  const rows: Contract[] = query.data?.items ?? [];

  return (
    <>
      <PageHeader
        title={t('contracts.title')}
        description={t('contracts.intro')}
        actions={
          canManage ? (
            <Link
              to="/contracts/new"
              className="bg-primary text-primary-fg hover:bg-primary-strong inline-flex h-10 items-center rounded-md px-4 text-sm font-medium"
            >
              {t('contracts.new')}
            </Link>
          ) : null
        }
      />

      <form onSubmit={applyFilters} className="mb-4 flex flex-wrap items-end gap-2" role="search">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('contracts.search')}</span>
          <Input name="q" value={q} onChange={(e) => setQ(e.target.value)} className="w-64" />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('contracts.statusFilter')}</span>
          <Select
            name="status"
            value={search.status ?? ''}
            onChange={(e) => setStatus(e.target.value)}
            placeholder={t('contracts.allStatuses')}
            options={STATUSES.map((status) => ({
              value: status,
              label: t(`contracts.statuses.${status}`),
            }))}
            className="w-48"
          />
        </label>
        <Button type="submit" variant="secondary">
          {t('common.search')}
        </Button>
        {search.q || search.status ? (
          <Button
            variant="ghost"
            onClick={() => {
              setQ('');
              setTrail([]);
              void navigate({ to: '/contracts', search: {} });
            }}
          >
            {t('common.clear')}
          </Button>
        ) : null}
      </form>

      <ProblemAlert
        page
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
        <EmptyState title={t('contracts.empty')} description={t('contracts.emptyHint')} />
      ) : (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="contract-table">
            <THead>
              <TR>
                <TH>{t('contracts.columns.code')}</TH>
                <TH>{t('contracts.columns.name')}</TH>
                <TH>{t('contracts.columns.provider')}</TH>
                <TH>{t('contracts.columns.payer')}</TH>
                <TH>{t('contracts.columns.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((contract) => (
                <TR key={contract.id}>
                  <TD>
                    <code className="font-mono text-xs">{contract.code}</code>
                  </TD>
                  <TD>
                    <Link
                      to="/contracts/$contractId"
                      params={{ contractId: contract.id }}
                      className="font-medium underline-offset-2 hover:underline"
                    >
                      {contract.name}
                    </Link>
                  </TD>
                  <TD>{contract.providerName ?? t('common.none')}</TD>
                  <TD>{contract.payerName ?? t('common.none')}</TD>
                  <TD>
                    <Badge tone={statusTone(contract.status)}>
                      {t(`contracts.statuses.${contract.status}`)}
                    </Badge>
                  </TD>
                </TR>
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
    </>
  );
}
