import { randomId, type PriceQuote, type PriceQuoteItem } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
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
import { useState, type FormEvent } from 'react';

import { useDefinitionOptions } from '../catalogOptions';
import { usePersonList } from '../people/queries';
import { problemOf } from '../problems';
import { useCreateQuote, useProviderOptions } from './queries';

interface RequestLine {
  key: string;
  serviceDefinitionId: string;
  quantity: string;
}

function emptyLine(): RequestLine {
  return { key: randomId(), serviceDefinitionId: '', quantity: '1' };
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
  const providers = useProviderOptions();
  const definitions = useDefinitionOptions();
  const quote = useCreateQuote();

  const [personQuery, setPersonQuery] = useState('');
  const [personId, setPersonId] = useState('');
  const [providerProfileId, setProviderProfileId] = useState('');
  const [serviceDate, setServiceDate] = useState(() => new Date().toISOString().slice(0, 10));
  const [lines, setLines] = useState<RequestLine[]>([emptyLine()]);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [result, setResult] = useState<PriceQuote | null>(null);

  if (!canQuote) {
    return (
      <>
        <PageHeader title={t('pricing.title')} description={t('pricing.intro')} />
        <EmptyState title={t('problems.PERMISSION_DENIED')} />
      </>
    );
  }

  function updateLine(key: string, patch: Partial<RequestLine>) {
    setLines((current) => current.map((l) => (l.key === key ? { ...l, ...patch } : l)));
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setProblem(null);
    try {
      const answer = await quote.mutateAsync({
        personId,
        providerProfileId,
        serviceDate,
        items: lines
          .filter((l) => l.serviceDefinitionId !== '')
          .map((l) => ({ serviceDefinitionId: l.serviceDefinitionId, quantity: l.quantity })),
      });
      setResult(answer);
    } catch (err) {
      setResult(null);
      setProblem(problemOf(err));
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
              onQueryChange={setPersonQuery}
              personId={personId}
              onPick={setPersonId}
            />
            <FormField
              label={t('pricing.fields.provider')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Select
                name="providerProfileId"
                value={providerProfileId}
                onChange={(e) => setProviderProfileId(e.target.value)}
                placeholder={t('common.none')}
                options={providers.data ?? []}
              />
            </FormField>
            <FormField
              label={t('pricing.fields.serviceDate')}
              required
              requiredLabel={t('common.requiredMark')}
            >
              <Input
                name="serviceDate"
                type="date"
                value={serviceDate}
                onChange={(e) => setServiceDate(e.target.value)}
              />
            </FormField>
          </div>

          <div className="grid gap-3">
            {lines.map((line) => (
              <div key={line.key} className="flex flex-wrap items-end gap-3">
                <FormField label={t('pricing.fields.service')} className="min-w-64 flex-1">
                  <Select
                    name={`service-${line.key}`}
                    value={line.serviceDefinitionId}
                    onChange={(e) => updateLine(line.key, { serviceDefinitionId: e.target.value })}
                    placeholder={t('common.none')}
                    options={definitions.data ?? []}
                  />
                </FormField>
                <FormField label={t('pricing.fields.quantity')}>
                  <Input
                    name={`quantity-${line.key}`}
                    value={line.quantity}
                    onChange={(e) => updateLine(line.key, { quantity: e.target.value })}
                    inputMode="decimal"
                    className="w-24 text-right font-mono"
                  />
                </FormField>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setLines((c) => c.filter((l) => l.key !== line.key))}
                  disabled={lines.length === 1}
                >
                  {t('pricing.fields.removeItem')}
                </Button>
              </div>
            ))}
          </div>

          <div className="flex justify-between gap-2">
            <Button variant="secondary" onClick={() => setLines((c) => [...c, emptyLine()])}>
              {t('pricing.fields.addItem')}
            </Button>
            <Button type="submit" loading={quote.isPending}>
              {t('pricing.run')}
            </Button>
          </div>
        </form>
      </Card>

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
  const people = usePersonList(
    trimmed.length >= 2 ? { q: trimmed, status: 'ACTIVE', limit: 20 } : { limit: 20 },
  );
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
    </>
  );
}

function QuoteResult({ quote }: { quote: PriceQuote }) {
  const { t } = useTranslation();
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
                <QuoteLine key={item.lineNo} item={item} currency={quote.currencyCode} />
              ))}
            </tbody>
            <tfoot className="border-line border-t-2">
              <tr className="font-medium">
                <td className="px-3 py-2">{t('pricing.totals')}</td>
                <td className="px-3 py-2 text-right font-mono">
                  {quote.contractAmount} {quote.currencyCode}
                </td>
                <td className="px-3 py-2 text-right font-mono">{quote.coveredAmount}</td>
                <td className="px-3 py-2 text-right font-mono">{quote.payerAmount}</td>
                <td className="px-3 py-2 text-right font-mono">{quote.memberAmount}</td>
              </tr>
            </tfoot>
          </table>
        </div>

        <p className="text-fg-muted mt-4 text-xs">{t('pricing.disclaimer')}</p>
      </Card>

      <Card>
        <h2 className="text-base font-semibold">{t('pricing.sourcesTitle')}</h2>
        <dl className="mt-3 grid grid-cols-[max-content_minmax(0,1fr)] [&>dd]:min-w-0 [&>dd]:break-words gap-x-6 gap-y-2 text-sm">
          <dt className="text-fg-muted">{t('pricing.sources.contractVersion')}</dt>
          <dd>
            {quote.contractVersionId ? (
              <Link
                to="/contract-versions/$contractVersionId"
                params={{ contractVersionId: quote.contractVersionId }}
                className="font-mono text-xs underline-offset-2 hover:underline"
              >
                {quote.contractVersionId}
              </Link>
            ) : (
              t('common.none')
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
function QuoteLine({ item, currency }: { item: PriceQuoteItem; currency: string }) {
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
        <td className="px-3 py-2 text-right font-mono">{item.payerAmount}</td>
        <td className="px-3 py-2 text-right font-mono">{item.memberAmount}</td>
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
