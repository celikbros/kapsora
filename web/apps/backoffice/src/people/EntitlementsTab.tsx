import { ApiError, randomId, type EntitlementAccount } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { formatDate, formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Card,
  Dialog,
  EmptyState,
  FormField,
  HelpHint,
  Input,
  ProblemAlert,
  Spinner,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  statusTone,
  useToast,
} from '@kapsora/ui';
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import { useAdjustmentCommands, useLedger, usePersonEntitlements } from '../benefit/queries';
import { problemOf } from '../problems';

/** Formats a decimal string for display without ever turning it into a number. */
function quantity(value: string, unit: string, currencyCode?: string | null): string {
  const trimmed = value.replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '');
  return unit === 'MONEY' && currencyCode ? `${trimmed} ${currencyCode}` : trimmed;
}

/** Entitlement balances of the person, with the ledger behind each account. */
export function EntitlementsTab({ personId }: { personId: string }) {
  const { t } = useTranslation();
  const toast = useToast();
  const canAdjust = usePermission('entitlement.adjust');
  const [asOf, setAsOf] = useState(new Date().toISOString().slice(0, 10));
  const accounts = usePersonEntitlements(personId, asOf);
  const [ledgerFor, setLedgerFor] = useState<EntitlementAccount | null>(null);
  const [adjusting, setAdjusting] = useState<EntitlementAccount | null>(null);
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);
  const commands = useAdjustmentCommands();
  const ledger = useLedger(ledgerFor?.id ?? '', { limit: 20 });

  const form = useForm<{ deltaQuantity: string; reasonCode: string; reasonText: string }>({
    defaultValues: { deltaQuantity: '', reasonCode: 'CORRECTION', reasonText: '' },
  });

  async function submitAdjustment(values: {
    deltaQuantity: string;
    reasonCode: string;
    reasonText: string;
  }) {
    if (!adjusting) return;
    setProblem(null);
    try {
      await commands.create.mutateAsync({
        accountId: adjusting.id,
        body: {
          deltaQuantity: values.deltaQuantity,
          reasonCode: values.reasonCode,
          ...(values.reasonText ? { reasonText: values.reasonText } : {}),
        },
        idempotencyKey: randomId(),
      });
      toast.notify({ tone: 'success', title: t('entitlements.adjustments.requested') });
      form.reset();
      setAdjusting(null);
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  const rows = accounts.data ?? [];

  return (
    <Card>
      <div className="flex flex-wrap items-end justify-between gap-3">
        {/* The mark sits beside the heading, not inside it: it is not part of the name. */}
        <div className="flex items-baseline gap-1.5">
          <h2 className="text-base font-semibold">{t('entitlements.title')}</h2>
          <HelpHint term="hakCuzdani" />
        </div>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('entitlements.asOf')}</span>
          <Input
            type="date"
            value={asOf}
            onChange={(e) => setAsOf(e.target.value)}
            className="w-44"
          />
        </label>
      </div>

      <ProblemAlert
        problem={problem ?? (accounts.error instanceof ApiError ? accounts.error.problem : null)}
        className="mt-4"
      />

      {accounts.isPending ? (
        <div className="text-fg-muted mt-4 flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <div className="mt-4">
          <EmptyState title={t('entitlements.empty')} />
        </div>
      ) : (
        <div className="mt-4 grid gap-3">
          <Table data-testid="entitlement-table">
            <THead>
              <TR>
                <TH>{t('entitlements.columns.definition')}</TH>
                <TH>{t('entitlements.columns.period')}</TH>
                <TH>{t('entitlements.columns.available')}</TH>
                <TH>
                  <span className="inline-flex items-center gap-1.5">
                    {t('entitlements.columns.reserved')}
                    <HelpHint
                      title="Bloke"
                      body="Ön onay ya da rezervasyonla ayrılmış, henüz kullanılmamış miktardır. Kullanılabilir bakiyeden düşülmüştür: işlem gerçekleşince kullanılana geçer, iptal edilirse bakiyeye döner."
                    />
                  </span>
                </TH>
                <TH>{t('entitlements.columns.consumed')}</TH>
                <TH>{t('entitlements.columns.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((account) => (
                <TR key={account.id}>
                  <TD>
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{account.definition.name}</span>
                      <code className="text-fg-muted font-mono text-xs">
                        {account.definition.code}
                      </code>
                      {account.shared ? (
                        <Badge tone="info" title={t('entitlements.sharedHint')}>
                          {t('entitlements.shared')}
                        </Badge>
                      ) : null}
                    </div>
                  </TD>
                  <TD>
                    {formatDate(account.benefitPeriodFrom)}
                    {account.benefitPeriodTo ? ` – ${formatDate(account.benefitPeriodTo)}` : ''}
                  </TD>
                  <TD className="font-mono">
                    {quantity(
                      account.available,
                      account.definition.unitType,
                      account.definition.currencyCode,
                    )}
                  </TD>
                  <TD className="font-mono">
                    {quantity(account.reserved, account.definition.unitType)}
                  </TD>
                  <TD className="font-mono">
                    {quantity(account.consumed, account.definition.unitType)}
                  </TD>
                  <TD>
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge
                        tone={statusTone(account.status === 'OPEN' ? 'ACTIVE' : account.status)}
                      >
                        {t(`entitlements.statuses.${account.status}`)}
                      </Badge>
                      <Button size="sm" variant="ghost" onClick={() => setLedgerFor(account)}>
                        {t('entitlements.ledger.title')}
                      </Button>
                      {canAdjust ? (
                        <Button size="sm" variant="ghost" onClick={() => setAdjusting(account)}>
                          {t('entitlements.adjustments.new')}
                        </Button>
                      ) : null}
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
          {rows.some((account) => account.status === 'FROZEN') ? (
            <p role="status" className="text-warning text-sm">
              {t('entitlements.frozen')}
            </p>
          ) : null}
        </div>
      )}

      <Dialog
        open={ledgerFor !== null}
        onOpenChange={(open) => {
          if (!open) setLedgerFor(null);
        }}
        title={t('entitlements.ledger.title')}
        description={ledgerFor?.definition.name}
        className="w-[min(92vw,52rem)]"
      >
        {ledger.isPending ? (
          <div className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
            <Spinner /> {t('common.loading')}
          </div>
        ) : (ledger.data?.items ?? []).length === 0 ? (
          <EmptyState title={t('entitlements.ledger.empty')} />
        ) : (
          <Table data-testid="ledger-table">
            <THead>
              <TR>
                <TH>{t('entitlements.ledger.columns.movement')}</TH>
                <TH>{t('entitlements.ledger.columns.effectiveAt')}</TH>
                <TH>{t('entitlements.ledger.columns.quantity')}</TH>
                <TH>{t('entitlements.ledger.columns.reason')}</TH>
              </TR>
            </THead>
            <TBody>
              {(ledger.data?.items ?? []).map((entry) => (
                <TR key={entry.id}>
                  <TD>{t(`entitlements.ledger.movements.${entry.movementType}`)}</TD>
                  <TD>{formatDateTime(entry.effectiveAt)}</TD>
                  <TD className="font-mono">{entry.deltaTotal}</TD>
                  <TD>{entry.reasonCode ?? t('common.none')}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Dialog>

      <Dialog
        open={adjusting !== null}
        onOpenChange={(open) => {
          if (!open) setAdjusting(null);
        }}
        title={t('entitlements.adjustments.createTitle')}
        description={adjusting?.definition.name}
      >
        <form onSubmit={form.handleSubmit(submitAdjustment)} className="grid gap-4" noValidate>
          <FormField
            label={t('entitlements.adjustments.fields.deltaQuantity')}
            hint={t('entitlements.adjustments.deltaHint')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input {...form.register('deltaQuantity')} inputMode="decimal" required />
          </FormField>
          <FormField
            label={t('entitlements.adjustments.fields.reasonCode')}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input {...form.register('reasonCode')} required />
          </FormField>
          <FormField label={t('entitlements.adjustments.fields.reasonText')}>
            <Input {...form.register('reasonText')} />
          </FormField>
          <ProblemAlert problem={problem} hideFieldErrors />
          <div className="flex justify-end gap-2">
            <Button
              variant="secondary"
              onClick={() => setAdjusting(null)}
              disabled={commands.create.isPending}
            >
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={commands.create.isPending}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Dialog>
    </Card>
  );
}
