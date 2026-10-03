import type { components } from '@kapsora/api-client';
import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Breadcrumb,
  Button,
  Card,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
} from '@kapsora/ui';
import { Link, useNavigate, useSearch } from '@tanstack/react-router';
import { useRef, useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import { claimDecimal, validClaimDecimal } from './numbers';
import { useCaseSource, useCaseSources, useCreateFromCase } from './queries';

type Detail = components['schemas']['ClaimCaseSourceDetail'];
type Body = components['schemas']['CreateClaimFromCase'];

/** Financial handoff: service choices come from the scoped episode, not the clinical API. */
export function ClaimNewPage() {
  const { t } = useTranslation();
  const search = useSearch({ from: '/app/claims/new' });
  const [selected, setSelected] = useState(search.caseId ?? '');
  const [locked, setLocked] = useState(false);
  const sources = useCaseSources();
  const source = useCaseSource(selected);
  const rows = sources.data?.pages.flatMap((p) => p.items) ?? [];
  const selectedSource = source.data?.data.source;
  const choices =
    selectedSource && !rows.some((r) => r.caseId === selectedSource.caseId)
      ? [selectedSource, ...rows]
      : rows;
  return (
    <>
      <PageHeader
        title={t('claims.create.title')}
        description={t('claims.handoff.intro')}
        breadcrumb={
          <Breadcrumb
            items={[
              { label: t('claims.title'), render: (label) => <Link to="/claims">{label}</Link> },
              { label: t('claims.create.title') },
            ]}
          />
        }
      />
      <div className="grid max-w-4xl gap-4">
        <Card>
          <FormField label={t('claims.handoff.case')} hint={t('claims.handoff.caseHint')}>
            <Select
              name="caseSource"
              value={selected}
              disabled={locked || sources.isPending}
              onChange={(e) => setSelected(e.target.value)}
              placeholder={t('claims.handoff.choose')}
              options={choices.map((r) => ({
                value: r.caseId,
                label: `${r.personDisplayName} · ${r.requestReference} · ${formatDate(r.serviceDate)}`,
              }))}
            />
          </FormField>
          {sources.isPending ? (
            <p role="status" className="mt-3 flex items-center gap-2 text-sm">
              <Spinner />
              {t('common.loading')}
            </p>
          ) : null}
          {sources.error ? (
            <div className="mt-3">
              <ProblemAlert problem={problemOf(sources.error)} />
              <Button variant="secondary" onClick={() => void sources.refetch()}>
                {t('common.retry')}
              </Button>
            </div>
          ) : null}
          {sources.hasNextPage ? (
            <Button
              variant="ghost"
              size="sm"
              disabled={locked}
              loading={sources.isFetchingNextPage}
              onClick={() => void sources.fetchNextPage()}
            >
              {t('claims.handoff.more')}
            </Button>
          ) : null}
        </Card>
        {!sources.isPending && !sources.error && rows.length === 0 && !selected ? (
          <EmptyState
            title={t('claims.handoff.empty')}
            description={t('claims.handoff.emptyHint')}
          />
        ) : null}
        {selected && source.isPending ? (
          <p role="status" className="flex items-center gap-2 text-sm">
            <Spinner />
            {t('common.loading')}
          </p>
        ) : null}
        {selected && source.error ? (
          <Card>
            <ProblemAlert problem={problemOf(source.error)} />
            <Button variant="secondary" onClick={() => void source.refetch()}>
              {t('common.retry')}
            </Button>
          </Card>
        ) : null}
        {source.data && !source.error ? (
          <Charges
            key={`${selected}:${source.data.etag}`}
            detail={source.data.data}
            etag={source.data.etag}
            onLock={setLocked}
            onReload={async () => {
              await source.refetch();
              setLocked(false);
            }}
          />
        ) : null}
      </div>
    </>
  );
}

function Charges({
  detail,
  etag,
  onLock,
  onReload,
}: {
  detail: Detail;
  etag: string;
  onLock: (locked: boolean) => void;
  onReload: () => Promise<void>;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const create = useCreateFromCase();
  const [lines, setLines] = useState<Body['lines']>(() =>
    detail.lines.map((line) => ({
      serviceDefinitionId: line.serviceDefinitionId,
      quantity: line.quantity,
      lineAmount: '',
    })),
  );
  const attempt = useRef<{ body: Body; etag: string; key: string } | null>(null);
  const pending = useRef(false);
  const [frozen, setFrozen] = useState(false);
  const valid =
    lines.length > 0 &&
    lines.every(
      (line) => validClaimDecimal(line.quantity, true) && validClaimDecimal(line.lineAmount),
    );
  const issue = create.error ? problemOf(create.error) : null;
  const definiteRefusal =
    issue &&
    issue.status >= 400 &&
    issue.status < 500 &&
    ![408, 429].includes(issue.status) &&
    issue.code !== 'IDEMPOTENCY_IN_PROGRESS';
  async function submit(event: FormEvent) {
    event.preventDefault();
    if (pending.current || (!valid && !attempt.current)) return;
    attempt.current ??= {
      body: {
        lines: lines.map((line) => ({
          ...line,
          quantity: claimDecimal(line.quantity),
          lineAmount: claimDecimal(line.lineAmount),
        })),
      },
      etag,
      key: crypto.randomUUID(),
    };
    pending.current = true;
    setFrozen(true);
    onLock(true);
    try {
      const created = await create.mutateAsync({
        caseId: detail.source.caseId,
        ...attempt.current,
      });
      await navigate({ to: '/claims/$claimId', params: { claimId: created.data.id } });
    } catch {
      // Uncertain retries retain the exact body, version and key in this form only.
    } finally {
      pending.current = false;
    }
  }
  async function reopen() {
    await onReload();
    attempt.current = null;
    setFrozen(false);
    onLock(false);
    create.reset();
  }
  return (
    <form onSubmit={(e) => void submit(e)} className="grid gap-4" data-testid="case-claim-form">
      <Card>
        <h2 className="text-base font-semibold">{t('claims.handoff.charges')}</h2>
        <p className="text-fg-muted mb-4 mt-1 text-sm">
          {t('claims.handoff.date', { date: formatDate(detail.source.serviceDate) })}
        </p>
        <div className="grid gap-4">
          {detail.lines.map((line, index) => (
            <div
              key={line.serviceDefinitionId}
              className="grid gap-3 border-t pt-4 sm:grid-cols-[minmax(0,1fr)_8rem_12rem]"
            >
              <div>
                <p className="font-medium">{line.serviceName}</p>
                <p className="text-fg-muted mt-1 text-sm">
                  {t('claims.handoff.reserved', {
                    quantity: line.quantity,
                    unit: t(`units.${line.unitType}`),
                  })}
                </p>
              </div>
              <FormField
                label={t('claims.lines.quantity')}
                error={
                  !validClaimDecimal(lines[index]!.quantity, true)
                    ? t('claims.handoff.quantityError')
                    : undefined
                }
                required
                requiredLabel={t('common.requiredMark')}
              >
                <Input
                  name={`lines.${index}.quantity`}
                  value={lines[index]!.quantity}
                  disabled={frozen}
                  inputMode="decimal"
                  className="text-right font-mono tabular-nums"
                  onChange={(e) =>
                    setLines((all) =>
                      all.map((l, i) => (i === index ? { ...l, quantity: e.target.value } : l)),
                    )
                  }
                />
              </FormField>
              <FormField
                label={t('claims.handoff.amount')}
                error={
                  !validClaimDecimal(lines[index]!.lineAmount)
                    ? t('claims.handoff.amountError')
                    : undefined
                }
                required
                requiredLabel={t('common.requiredMark')}
              >
                <Input
                  name={`lines.${index}.lineAmount`}
                  value={lines[index]!.lineAmount}
                  disabled={frozen}
                  inputMode="decimal"
                  className="text-right font-mono tabular-nums"
                  onChange={(e) =>
                    setLines((all) =>
                      all.map((l, i) => (i === index ? { ...l, lineAmount: e.target.value } : l)),
                    )
                  }
                />
              </FormField>
            </div>
          ))}
        </div>
        <p className="text-fg-muted mt-4 text-sm">{t('claims.handoff.links')}</p>
      </Card>
      <ProblemAlert problem={issue} />
      {frozen && issue && !definiteRefusal ? (
        <p role="status" className="text-sm">
          {t('claims.handoff.uncertain')}
        </p>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button
          type="submit"
          disabled={(!valid && !frozen) || Boolean(definiteRefusal)}
          loading={create.isPending}
        >
          {frozen && issue ? t('common.retry') : t('claims.create.submit')}
        </Button>
        {definiteRefusal ? (
          <Button variant="secondary" onClick={() => void reopen()}>
            {t('claims.handoff.edit')}
          </Button>
        ) : null}
      </div>
    </form>
  );
}
