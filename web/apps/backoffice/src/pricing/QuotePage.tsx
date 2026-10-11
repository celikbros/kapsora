import {
  randomId,
  type CreatePriceQuoteRequest,
  type PriceQuote,
  type PriceQuoteItem,
} from '@kapsora/api-client';
import { usePermission, useSession } from '@kapsora/auth';
import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  FormField,
  HelpHint,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  statusTone,
  type BadgeTone,
} from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { useRef, useState, type FormEvent } from 'react';

import { problemOf } from '../problems';
import {
  useCreateQuote,
  usePriceProviderOptions,
  usePriceServiceOptions,
  useQuotePeople,
} from './queries';

interface RequestLine {
  key: string;
  serviceDefinitionId: string;
  quantity: string;
}

function emptyLine(): RequestLine {
  return { key: randomId(), serviceDefinitionId: '', quantity: '1' };
}

function localDate(): string {
  const now = new Date();
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`;
}

function validDate(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const [year, month, day] = value.split('-').map(Number);
  const date = new Date(year!, month! - 1, day!);
  return date.getFullYear() === year && date.getMonth() + 1 === month && date.getDate() === day;
}

function validQuantity(value: string): boolean {
  return /^(?:0|[1-9]\d*)(?:\.\d{1,6})?$/.test(value) && !/^0(?:\.0+)?$/.test(value);
}

/** An outcome is a judgement, so it is coloured like one. */
function outcomeTone(outcome: string): BadgeTone {
  switch (outcome) {
    case 'QUOTED':
      return 'success';
    case 'PARTIAL':
      return 'info';
    case 'REVIEW_REQUIRED':
      return 'warning';
    default:
      return 'neutral';
  }
}

/**
 * The price quote.
 *
 * This screen is an explanation, not a receipt. The arithmetic has to be followable top to
 * bottom — what the contract says it costs, what the rules changed, what the balance
 * covered, what is left for the member — and every figure names the thing that produced
 * it. When the answer is REVIEW_REQUIRED there is deliberately no member figure: an
 * operator who is shown a number reads it as the answer, and here there is not one yet.
 */
export function QuotePage() {
  const { t } = useTranslation();
  const canQuote = usePermission('pricing.quote');
  const canReadMembers = usePermission('member.read');
  const context = useSession(
    (s) => `${s.session?.actorId ?? ''}:${JSON.stringify(s.activeTenant ?? null)}`,
  );
  if (!canQuote || !canReadMembers) {
    return (
      <>
        <PageHeader title={t('pricing.title')} description={t('pricing.intro')} />
        <EmptyState title={t('problems.PERMISSION_DENIED')} />
      </>
    );
  }
  return <QuoteWorkbench key={context} />;
}

function QuoteWorkbench() {
  const { t } = useTranslation();
  const quote = useCreateQuote();

  const [personQuery, setPersonQuery] = useState('');
  const [personId, setPersonId] = useState('');
  const [providerQuery, setProviderQuery] = useState('');
  const [serviceQuery, setServiceQuery] = useState('');
  const [providerProfileId, setProviderProfileId] = useState('');
  const [serviceDate, setServiceDate] = useState(localDate);
  const [lines, setLines] = useState<RequestLine[]>([emptyLine()]);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [result, setResult] = useState<PriceQuote | null>(null);
  const [validation, setValidation] = useState('');
  const revision = useRef(0);
  const lastAttempt = useRef<{ signature: string; key: string } | null>(null);
  const providers = usePriceProviderOptions(
    providerQuery.trim().length >= 2 ? providerQuery.trim() : '',
  );
  const services = usePriceServiceOptions(
    serviceQuery.trim().length >= 2 ? serviceQuery.trim() : '',
  );
  const providerOptions = [
    ...new Map(
      (providers.data?.pages ?? []).flatMap((page) =>
        page.items.map(
          (item) =>
            [
              item.providerProfileId,
              { value: item.providerProfileId, label: item.organizationName },
            ] as const,
        ),
      ),
    ).values(),
  ];
  const serviceOptions = [
    ...new Map(
      (services.data?.pages ?? []).flatMap((page) =>
        page.items.map(
          (item) =>
            [
              item.serviceDefinitionId,
              { value: item.serviceDefinitionId, label: `${item.code} · ${item.name}` },
            ] as const,
        ),
      ),
    ).values(),
  ];

  function changed() {
    revision.current += 1;
    lastAttempt.current = null;
    setResult(null);
    setProblem(null);
    setValidation('');
  }

  function updateLine(key: string, patch: Partial<RequestLine>) {
    changed();
    setLines((current) => current.map((l) => (l.key === key ? { ...l, ...patch } : l)));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (quote.isPending) return;
    if (
      !personId ||
      !providerProfileId ||
      !validDate(serviceDate) ||
      lines.length < 1 ||
      lines.length > 100 ||
      lines.some((line) => !line.serviceDefinitionId || !validQuantity(line.quantity))
    ) {
      setValidation(t('pricing.validation'));
      setResult(null);
      return;
    }
    const body: CreatePriceQuoteRequest = {
      personId,
      providerProfileId,
      serviceDate,
      items: lines.map((line) => ({
        serviceDefinitionId: line.serviceDefinitionId,
        quantity: line.quantity,
      })),
    };
    const signature = JSON.stringify(body);
    if (lastAttempt.current?.signature !== signature)
      lastAttempt.current = { signature, key: randomId() };
    const idempotencyKey = lastAttempt.current.key;
    const submittedRevision = revision.current;
    setProblem(null);
    setValidation('');
    try {
      const answer = await quote.mutateAsync({ body, idempotencyKey });
      if (submittedRevision === revision.current) {
        lastAttempt.current = null;
        setResult(answer);
      }
    } catch (err) {
      if (submittedRevision === revision.current) {
        setResult(null);
        setProblem(problemOf(err));
      }
    }
  }

  return (
    <>
      <PageHeader title={t('pricing.title')} description={t('pricing.intro')} />

      <Card className="mb-4">
        <form onSubmit={submit} className="grid gap-4" noValidate>
          <div className="grid gap-4 md:grid-cols-3">
            <PersonPicker
              query={personQuery}
              onQueryChange={(value) => {
                changed();
                setPersonId('');
                setPersonQuery(value);
              }}
              personId={personId}
              onPick={(value) => {
                changed();
                setPersonId(value);
              }}
            />
            <div className="grid gap-2">
              <FormField label={t('pricing.searchProviders')}>
                <Input
                  name="providerSearch"
                  maxLength={120}
                  value={providerQuery}
                  onChange={(e) => {
                    changed();
                    setProviderProfileId('');
                    setProviderQuery(e.target.value);
                  }}
                  autoComplete="off"
                />
              </FormField>
              <FormField
                label={t('pricing.fields.provider')}
                required
                requiredLabel={t('common.requiredMark')}
              >
                <Select
                  data-testid="price-provider-select"
                  name="providerProfileId"
                  value={providerProfileId}
                  onChange={(e) => {
                    changed();
                    setProviderProfileId(e.target.value);
                  }}
                  placeholder={t('common.none')}
                  options={providerOptions}
                />
              </FormField>
              <OptionStatus
                kind="providers"
                pending={providers.isPending}
                error={providers.isError}
                next={providers.hasNextPage}
                loadingNext={providers.isFetchingNextPage}
                empty={providerOptions.length === 0}
                retry={() =>
                  void (providers.isFetchNextPageError
                    ? providers.fetchNextPage()
                    : providers.refetch())
                }
                loadMore={() => void providers.fetchNextPage()}
              />
            </div>
            <FormField
              label={t('pricing.fields.serviceDate')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="serviceDate"
                type="date"
                value={serviceDate}
                onChange={(e) => {
                  changed();
                  setServiceDate(e.target.value);
                }}
              />
            </FormField>
          </div>

          <div className="grid gap-3">
            <div className="grid gap-1">
              <FormField label={t('pricing.searchServices')}>
                <Input
                  name="serviceSearch"
                  maxLength={120}
                  value={serviceQuery}
                  onChange={(e) => {
                    changed();
                    setServiceQuery(e.target.value);
                    setLines((current) =>
                      current.map((line) => ({ ...line, serviceDefinitionId: '' })),
                    );
                  }}
                  autoComplete="off"
                />
              </FormField>
              <OptionStatus
                kind="services"
                pending={services.isPending}
                error={services.isError}
                next={services.hasNextPage}
                loadingNext={services.isFetchingNextPage}
                empty={serviceOptions.length === 0}
                retry={() =>
                  void (services.isFetchNextPageError
                    ? services.fetchNextPage()
                    : services.refetch())
                }
                loadMore={() => void services.fetchNextPage()}
              />
            </div>
            {lines.map((line, index) => (
              <div key={line.key} className="flex flex-wrap items-end gap-3">
                <FormField
                  label={`${t('pricing.fields.service')} ${index + 1}`}
                  className="min-w-64 flex-1"
                >
                  <Select
                    name={`service-${line.key}`}
                    value={line.serviceDefinitionId}
                    onChange={(e) => updateLine(line.key, { serviceDefinitionId: e.target.value })}
                    placeholder={t('common.none')}
                    options={serviceOptions}
                  />
                </FormField>
                <FormField label={`${t('pricing.fields.quantity')} ${index + 1}`}>
                  <Input
                    name={`quantity-${line.key}`}
                    value={line.quantity}
                    onChange={(e) => updateLine(line.key, { quantity: e.target.value })}
                    inputMode="decimal"
                    className="w-24 text-right font-mono"
                  />
                </FormField>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    changed();
                    setLines((c) => c.filter((l) => l.key !== line.key));
                  }}
                  disabled={lines.length === 1}
                >
                  {t('pricing.fields.removeItem')}
                </Button>
              </div>
            ))}
          </div>

          <div className="flex justify-between gap-2">
            <Button
              type="button"
              variant="secondary"
              disabled={lines.length >= 100}
              onClick={() => {
                changed();
                setLines((c) => [...c, emptyLine()]);
              }}
            >
              {t('pricing.fields.addItem')}
            </Button>
            <Button type="submit" loading={quote.isPending} disabled={quote.isPending}>
              {t('pricing.run')}
            </Button>
          </div>
        </form>
      </Card>

      {validation ? (
        <p role="alert" className="text-danger mb-4 text-sm">
          {validation}
        </p>
      ) : null}
      <ProblemAlert problem={problem} className="mb-4" />

      {quote.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('pricing.running')}
        </div>
      ) : result ? (
        <QuoteResult quote={result} />
      ) : (
        <EmptyState title={t('pricing.empty')} />
      )}
    </>
  );
}

/**
 * Picks the person by name. The identifier of a member is not something an operator has
 * in their head, and asking for one turns a working screen into a demo; the same name
 * search the member list uses is what they already know how to do.
 */
function PersonPicker({
  query,
  onQueryChange,
  personId,
  onPick,
}: {
  query: string;
  onQueryChange: (value: string) => void;
  personId: string;
  onPick: (id: string) => void;
}) {
  const { t } = useTranslation();
  const trimmed = query.trim();
  const people = useQuotePeople(trimmed.length >= 2 ? trimmed : '');
  const options = (people.data?.items ?? []).map((person) => ({
    value: person.id,
    label: person.displayName,
  }));

  return (
    <>
      <FormField label={t('people.search')} hint={t('people.searchHint')}>
        <Input
          name="personSearch"
          value={query}
          onChange={(e) => onQueryChange(e.target.value)}
          autoComplete="off"
        />
      </FormField>
      <FormField
        label={t('pricing.fields.person')}
        required
        requiredLabel={t('common.requiredMark')}
      >
        <Select
          name="personId"
          value={personId}
          onChange={(e) => onPick(e.target.value)}
          placeholder={t('common.none')}
          options={options}
        />
      </FormField>
      {people.isPending ? (
        <p className="text-fg-muted text-xs" aria-busy="true">
          {t('common.loading')}
        </p>
      ) : null}
      {people.isError ? (
        <Button type="button" size="sm" variant="secondary" onClick={() => void people.refetch()}>
          {t('common.retry')}
        </Button>
      ) : null}
      {people.isSuccess && options.length === 0 ? (
        <p className="text-fg-muted text-xs">{t('pricing.noPeople')}</p>
      ) : null}
    </>
  );
}

function OptionStatus({
  kind,
  pending,
  error,
  next,
  loadingNext,
  empty,
  retry,
  loadMore,
}: {
  kind: 'providers' | 'services';
  pending: boolean;
  error: boolean;
  next: boolean;
  loadingNext: boolean;
  empty: boolean;
  retry: () => void;
  loadMore: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="mt-1 grid gap-1">
      {pending ? (
        <p className="text-fg-muted flex items-center gap-2 text-xs" aria-busy="true">
          <Spinner />
          {t('common.loading')}
        </p>
      ) : null}
      {error ? (
        <Button
          type="button"
          size="sm"
          variant="secondary"
          data-testid={`price-${kind}-retry`}
          onClick={retry}
        >
          {t('common.retry')}
        </Button>
      ) : null}
      {!pending && !error && empty ? (
        <p className="text-fg-muted text-xs">
          {t(kind === 'providers' ? 'pricing.noProviders' : 'pricing.noServices')}
        </p>
      ) : null}
      {next && !error ? (
        <Button
          type="button"
          size="sm"
          variant="secondary"
          data-testid={`price-${kind}-more`}
          loading={loadingNext}
          onClick={loadMore}
        >
          {t('pricing.loadMore')}
        </Button>
      ) : null}
    </div>
  );
}

function QuoteResult({ quote }: { quote: PriceQuote }) {
  const { t } = useTranslation();
  const canReadContract = usePermission('contract.read');
  const review = quote.outcome === 'REVIEW_REQUIRED';

  return (
    <div className="grid gap-4">
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <Badge tone={outcomeTone(quote.outcome)}>
              {t(`pricing.outcomes.${quote.outcome}`)}
            </Badge>
            <span className="text-fg-muted text-xs">
              {t('pricing.quoteId')}: <code className="font-mono">{quote.id}</code>
            </span>
          </div>
          {quote.expired ? (
            <p role="status" className="text-warning text-sm">
              {t('pricing.expired', { date: formatDate(quote.expiresAt) })}
            </p>
          ) : null}
        </div>

        {review ? (
          <p role="status" className="bg-warning-soft text-fg mt-3 rounded-md p-3 text-sm">
            {t('pricing.reviewHint')}
          </p>
        ) : null}

        <div className="mt-4 overflow-x-auto">
          <table className="w-full text-sm" data-testid="quote-table">
            <thead className="bg-surface-sunken text-fg-muted text-xs">
              <tr>
                <th className="px-3 py-2 text-left font-medium">{t('pricing.columns.line')}</th>
                <th className="px-3 py-2 text-right font-medium">
                  {t('pricing.columns.contract')}
                </th>
                <th className="px-3 py-2 text-right font-medium">
                  <span className="inline-flex items-center gap-1.5">
                    {t('pricing.columns.covered')}
                    <HelpHint
                      title="Kapsanan"
                      body="Sözleşme tutarının, planın kapsamına giren bölümüdür. Ödeyici ve hak sahibi payları bu tutarın içinden ayrılır; kapsam dışında kalan kısım doğrudan hak sahibine aittir."
                    />
                  </span>
                </th>
                <th className="px-3 py-2 text-right font-medium">{t('pricing.columns.payer')}</th>
                <th className="px-3 py-2 text-right font-medium">{t('pricing.columns.member')}</th>
              </tr>
            </thead>
            <tbody>
              {quote.items.map((item) => (
                <QuoteLine
                  key={item.lineNo}
                  item={item}
                  currency={quote.currencyCode}
                  review={review}
                />
              ))}
            </tbody>
            <tfoot className="border-line border-t-2">
              <tr className="font-medium">
                <td className="px-3 py-2">{t('pricing.totals')}</td>
                <td className="px-3 py-2 text-right font-mono">
                  {quote.contractAmount} {quote.currencyCode}
                </td>
                <td className="px-3 py-2 text-right font-mono">{quote.coveredAmount}</td>
                <td className="px-3 py-2 text-right font-mono">
                  {review ? '—' : quote.payerAmount}
                </td>
                <td className="px-3 py-2 text-right font-mono">
                  {review ? '—' : quote.memberAmount}
                </td>
              </tr>
            </tfoot>
          </table>
        </div>

        <p className="text-fg-muted mt-4 text-xs">{quote.disclaimer || t('pricing.disclaimer')}</p>
      </Card>

      <Card>
        <h2 className="text-base font-semibold">{t('pricing.sourcesTitle')}</h2>
        <dl className="mt-3 grid grid-cols-[max-content_minmax(0,1fr)] [&>dd]:min-w-0 [&>dd]:break-words gap-x-6 gap-y-2 text-sm">
          <dt className="text-fg-muted">{t('pricing.sources.contractVersion')}</dt>
          <dd>
            {quote.contractVersionId && canReadContract ? (
              <Link
                to="/contract-versions/$contractVersionId"
                params={{ contractVersionId: quote.contractVersionId }}
                className="font-mono text-xs underline-offset-2 hover:underline"
              >
                {quote.contractVersionId}
              </Link>
            ) : (
              (quote.contractVersionId ?? t('common.none'))
            )}
          </dd>
          <dt className="text-fg-muted">{t('pricing.sources.planVersion')}</dt>
          <dd className="font-mono text-xs">{quote.planVersionId ?? t('common.none')}</dd>
          <dt className="text-fg-muted">{t('pricing.sources.ruleVersions')}</dt>
          <dd className="font-mono text-xs">
            {quote.ruleSetVersionIds && quote.ruleSetVersionIds.length > 0
              ? quote.ruleSetVersionIds.join(', ')
              : t('common.none')}
          </dd>
          <dt className="text-fg-muted">{t('pricing.sources.evaluation')}</dt>
          <dd className="font-mono text-xs">{quote.eligibilityEvaluationId ?? t('common.none')}</dd>
        </dl>
      </Card>
    </div>
  );
}

/**
 * One line, with its explanations directly beneath it rather than collected at the bottom
 * of the page: the reason a figure is what it is belongs next to the figure.
 */
function QuoteLine({
  item,
  currency,
  review,
}: {
  item: PriceQuoteItem;
  currency: string;
  review: boolean;
}) {
  const { t } = useTranslation();
  return (
    <>
      <tr className="border-line border-t">
        <td className="px-3 py-2">
          <div className="flex items-center gap-2">
            <span>{item.lineNo}</span>
            <Badge tone={statusTone(item.outcome)}>{t(`pricing.outcomes.${item.outcome}`)}</Badge>
          </div>
        </td>
        <td className="px-3 py-2 text-right font-mono">
          {item.contractAmount} {currency}
        </td>
        <td className="px-3 py-2 text-right font-mono">{item.coveredAmount}</td>
        <td className="px-3 py-2 text-right font-mono">
          {review || item.outcome === 'REVIEW_REQUIRED' ? '—' : item.payerAmount}
        </td>
        <td className="px-3 py-2 text-right font-mono">
          {review || item.outcome === 'REVIEW_REQUIRED' ? '—' : item.memberAmount}
        </td>
      </tr>
      {(item.explanations ?? []).length > 0 ? (
        <tr>
          <td colSpan={5} className="px-3 pb-3">
            <ul className="text-fg-muted grid gap-1 text-xs">
              {(item.explanations ?? []).map((explanation, index) => (
                <li key={`${explanation.code}-${index}`}>
                  {t(`pricing.explanations.${explanation.code}`, {
                    defaultValue: explanation.code,
                  })}
                  {explanation.source ? (
                    <span className="ml-1 font-mono">({explanation.source})</span>
                  ) : null}
                </li>
              ))}
            </ul>
          </td>
        </tr>
      ) : null}
    </>
  );
}
