import type { ApiError, EligibilityCheckResult } from '@kapsora/api-client';
import { i18next, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  FormField,
  Input,
  ProblemAlert,
  Select,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
} from '@kapsora/ui';
import { useState } from 'react';

import { useEligibilityCheck, usePersonEntitlements, usePrograms } from '../benefit/queries';
import { problemOf } from '../problems';

/** One requested line of the check. */
interface Line {
  entitlementCode: string;
  quantity: string;
}

/** Turkish text for an explanation code, falling back to the server's message. */
function explanation(code: string, message: string): string {
  const key = `eligibility.codes.${code}`;
  return i18next.exists(key) ? i18next.t(key) : message;
}

function outcomeTone(outcome: EligibilityCheckResult['outcome']) {
  switch (outcome) {
    case 'ELIGIBLE':
      return 'success' as const;
    case 'PARTIALLY_ELIGIBLE':
      return 'warning' as const;
    case 'REVIEW_REQUIRED':
      return 'info' as const;
    default:
      return 'danger' as const;
  }
}

/** Runs an eligibility check for this person and explains the answer. */
export function EligibilityTab({ personId }: { personId: string }) {
  const { t } = useTranslation();
  const check = useEligibilityCheck();
  const [serviceDate, setServiceDate] = useState(new Date().toISOString().slice(0, 10));
  const [programId, setProgramId] = useState('');
  const [lines, setLines] = useState<Line[]>([{ entitlementCode: '', quantity: '1' }]);
  const [result, setResult] = useState<EligibilityCheckResult | null>(null);
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);
  const programs = usePrograms({ status: 'ACTIVE', limit: 200 });
  const accounts = usePersonEntitlements(personId, serviceDate);

  async function run() {
    setProblem(null);
    setResult(null);
    try {
      const answer = await check.mutateAsync({
        body: {
          personId,
          serviceDate,
          ...(programId ? { programId } : {}),
          serviceItems: lines.map((line) => ({
            // Service definitions arrive in I3; until then the entitlement code is the
            // hint that tells the server which balance the line draws from.
            serviceDefinitionId: '00000000-0000-4000-8000-000000000000',
            quantity: line.quantity,
          })),
          context: { entitlementCodes: lines.map((line) => line.entitlementCode) },
        },
      });
      setResult(answer);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  const codeOptions = (accounts.data ?? []).map((account) => ({
    value: account.definition.code,
    label: `${account.definition.name} (${account.definition.code})`,
  }));

  return (
    <div className="grid gap-4">
      <Card>
        <h2 className="text-base font-semibold">{t('eligibility.title')}</h2>
        <p className="text-fg-muted mt-1 max-w-prose text-sm">{t('eligibility.intro')}</p>

        <div className="mt-4 grid gap-4 md:grid-cols-2">
          <FormField
            label={t('eligibility.fields.serviceDate')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input
              type="date"
              value={serviceDate}
              onChange={(e) => setServiceDate(e.target.value)}
            />
          </FormField>
          <FormField label={t('eligibility.fields.program')}>
            <Select
              value={programId}
              onChange={(e) => setProgramId(e.target.value)}
              placeholder={t('common.none')}
              options={(programs.data?.items ?? []).map((program) => ({
                value: program.id,
                label: `${program.code} · ${program.name}`,
              }))}
            />
          </FormField>
        </div>

        <fieldset className="border-line mt-4 rounded-md border p-4">
          <legend className="px-1 text-sm font-medium">{t('eligibility.items')}</legend>
          <div className="grid gap-3">
            {lines.map((line, index) => (
              <div key={index} className="grid gap-2 md:grid-cols-[1fr_8rem_auto] md:items-start">
                <FormField label={t('eligibility.fields.entitlementCode')}>
                  <Select
                    value={line.entitlementCode}
                    onChange={(e) =>
                      setLines((current) =>
                        current.map((item, i) =>
                          i === index ? { ...item, entitlementCode: e.target.value } : item,
                        ),
                      )
                    }
                    placeholder={t('common.none')}
                    options={codeOptions}
                  />
                </FormField>
                <FormField label={t('eligibility.fields.quantity')}>
                  <Input
                    value={line.quantity}
                    inputMode="decimal"
                    onChange={(e) =>
                      setLines((current) =>
                        current.map((item, i) =>
                          i === index ? { ...item, quantity: e.target.value } : item,
                        ),
                      )
                    }
                  />
                </FormField>
                <Button
                  variant="ghost"
                  size="sm"
                  className="mt-6"
                  onClick={() => setLines((current) => current.filter((_, i) => i !== index))}
                  disabled={lines.length === 1}
                >
                  {t('eligibility.fields.removeItem')}
                </Button>
              </div>
            ))}
          </div>
          <Button
            variant="secondary"
            size="sm"
            className="mt-3"
            onClick={() =>
              setLines((current) => [...current, { entitlementCode: '', quantity: '1' }])
            }
          >
            {t('eligibility.fields.addItem')}
          </Button>
        </fieldset>

        <ProblemAlert problem={problem} className="mt-4" />

        <div className="mt-4 flex justify-end">
          <Button onClick={() => void run()} loading={check.isPending}>
            {check.isPending ? t('eligibility.running') : t('eligibility.run')}
          </Button>
        </div>
      </Card>

      {result === null ? (
        <EmptyState title={t('eligibility.empty')} />
      ) : (
        <Card>
          <div className="flex flex-wrap items-center gap-3">
            <Badge tone={outcomeTone(result.outcome)}>
              {t(`eligibility.outcomes.${result.outcome}`)}
            </Badge>
            <span className="text-fg-muted text-xs">
              {t('eligibility.evaluationId')}:{' '}
              <code className="font-mono">{result.evaluationId}</code>
            </span>
            {result.planVersionId ? (
              <span className="text-fg-muted text-xs">
                {t('eligibility.planVersion')}:{' '}
                <code className="font-mono">{result.planVersionId}</code>
              </span>
            ) : null}
          </div>

          <h3 className="mt-4 text-sm font-semibold">{t('eligibility.explanations')}</h3>
          <ul className="mt-2 grid gap-1 text-sm">
            {result.explanations.map((entry, index) => (
              <li key={`${entry.code}-${index}`} className="flex items-start gap-2">
                <Badge
                  tone={
                    entry.severity === 'ERROR'
                      ? 'danger'
                      : entry.severity === 'WARNING'
                        ? 'warning'
                        : 'neutral'
                  }
                >
                  {entry.severity}
                </Badge>
                <span>{explanation(entry.code, entry.message)}</span>
              </li>
            ))}
          </ul>

          {result.items && result.items.length > 0 ? (
            <>
              <h3 className="mt-4 text-sm font-semibold">{t('eligibility.items')}</h3>
              <div className="mt-2">
                <Table data-testid="eligibility-items">
                  <THead>
                    <TR>
                      <TH>{t('eligibility.fields.entitlementCode')}</TH>
                      <TH>{t('eligibility.fields.quantity')}</TH>
                      <TH>{t('entitlements.columns.available')}</TH>
                      <TH>{t('people.columns.status')}</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {result.items.map((item) => (
                      <TR key={item.index}>
                        <TD>
                          <code className="font-mono text-xs">
                            {item.entitlementCode ?? t('common.none')}
                          </code>
                        </TD>
                        <TD className="font-mono">{item.requestedQuantity ?? ''}</TD>
                        <TD className="font-mono">{item.availableQuantity ?? t('common.none')}</TD>
                        <TD>
                          <Badge
                            tone={
                              item.outcome === 'ELIGIBLE'
                                ? 'success'
                                : item.outcome === 'REVIEW_REQUIRED'
                                  ? 'info'
                                  : 'danger'
                            }
                          >
                            {t(`eligibility.itemOutcomes.${item.outcome}`)}
                          </Badge>
                        </TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              </div>
            </>
          ) : null}

          {result.balances && result.balances.length > 0 ? (
            <>
              <h3 className="mt-4 text-sm font-semibold">{t('eligibility.balances')}</h3>
              <ul className="mt-2 flex flex-wrap gap-2 text-sm">
                {result.balances.map((balance) => (
                  <li
                    key={balance.entitlementCode}
                    className="bg-surface-sunken rounded-md px-2 py-1"
                  >
                    <code className="font-mono text-xs">{balance.entitlementCode}</code>{' '}
                    <span className="font-mono">{balance.available}</span>{' '}
                    <span className="text-fg-muted text-xs">{balance.unit}</span>
                  </li>
                ))}
              </ul>
            </>
          ) : null}
        </Card>
      )}
    </div>
  );
}
