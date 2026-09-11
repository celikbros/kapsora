import type { Program, ProgramStatus } from '@kapsora/api-client';
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
import { usePrograms } from './queries';

export interface ProgramListSearch {
  q?: string;
  status?: ProgramStatus;
  cursor?: string;
}

const PAGE_SIZE = 50;
const STATUSES: ProgramStatus[] = ['DRAFT', 'ACTIVE', 'SUSPENDED', 'CLOSED'];

/** Programs: the top of the benefit hierarchy, one per sponsor arrangement. */
export function ProgramListPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const search = useSearch({ from: '/app/programs' });
  const canManage = usePermission('program.manage');
  const [q, setQ] = useState(search.q ?? '');
  const [trail, setTrail] = useState<string[]>([]);

  const query = usePrograms({
    ...(search.q ? { q: search.q } : {}),
    ...(search.status ? { status: search.status } : {}),
    ...(search.cursor ? { cursor: search.cursor } : {}),
    limit: PAGE_SIZE,
  });

  function applyFilters(event: FormEvent) {
    event.preventDefault();
    setTrail([]);
    void navigate({
      to: '/programs',
      search: {
        ...(q.trim() ? { q: q.trim() } : {}),
        ...(search.status ? { status: search.status } : {}),
      },
    });
  }

  function setStatus(status: string) {
    setTrail([]);
    void navigate({
      to: '/programs',
      search: {
        ...(search.q ? { q: search.q } : {}),
        ...(status ? { status: status as ProgramStatus } : {}),
      },
    });
  }

  function goNext() {
    const next = query.data?.nextCursor;
    if (!next) return;
    setTrail((previous) => [...previous, search.cursor ?? '']);
    void navigate({ to: '/programs', search: { ...search, cursor: next } });
  }

  function goPrevious() {
    const previous = trail[trail.length - 1];
    setTrail((rest) => rest.slice(0, -1));
    const { cursor: _cursor, ...others } = search;
    void navigate({
      to: '/programs',
      search: previous ? { ...others, cursor: previous } : others,
    });
  }

  const rows: Program[] = query.data?.items ?? [];

  return (
    <>
      <PageHeader
        title={t('programs.title')}
        description={t('programs.intro')}
        actions={
          canManage ? (
            <Link
              to="/programs/new"
              className="bg-primary text-primary-fg hover:bg-primary-strong inline-flex h-10 items-center rounded-md px-4 text-sm font-medium"
            >
              {t('programs.new')}
            </Link>
          ) : null
        }
      />

      <form onSubmit={applyFilters} className="mb-4 flex flex-wrap items-end gap-2" role="search">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('programs.search')}</span>
          <Input name="q" value={q} onChange={(e) => setQ(e.target.value)} className="w-64" />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('programs.statusFilter')}</span>
          <Select
            name="status"
            value={search.status ?? ''}
            onChange={(e) => setStatus(e.target.value)}
            placeholder={t('programs.allStatuses')}
            options={STATUSES.map((status) => ({
              value: status,
              label: t(`programs.statuses.${status}`),
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
              void navigate({ to: '/programs', search: {} });
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
        <EmptyState title={t('programs.empty')} description={t('programs.emptyHint')} />
      ) : (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="program-table">
            <THead>
              <TR>
                <TH>{t('programs.columns.code')}</TH>
                <TH>{t('programs.columns.name')}</TH>
                <TH>{t('programs.columns.sponsor')}</TH>
                <TH>{t('programs.columns.payer')}</TH>
                <TH>{t('programs.columns.plans')}</TH>
                <TH>{t('programs.columns.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((program) => (
                <TR key={program.id}>
                  <TD>
                    <code className="font-mono text-xs">{program.code}</code>
                  </TD>
                  <TD>
                    <Link
                      to="/programs/$programId"
                      params={{ programId: program.id }}
                      className="font-medium underline-offset-2 hover:underline"
                    >
                      {program.name}
                    </Link>
                  </TD>
                  <TD>{program.sponsorDisplayName ?? t('common.none')}</TD>
                  <TD>{program.payerDisplayName ?? t('common.none')}</TD>
                  <TD>{program.planCount ?? 0}</TD>
                  <TD>
                    <Badge tone={statusTone(program.status)}>
                      {t(`programs.statuses.${program.status}`)}
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
