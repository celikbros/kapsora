import {
  randomId,
  type ServiceRequest,
  type ServiceRequestItems,
  type Versioned,
} from '@kapsora/api-client';
import { usePermission, useTenantId } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { Button, Card, FormField, Input, ProblemAlert, Select, useToast } from '@kapsora/ui';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { problemOf } from './problems';
import { useServiceDefinitions } from './queries';
import { useOps } from './services';

type Line = ServiceRequestItems['items'][number];
const editableLines = (request: ServiceRequest): Line[] =>
  request.items.map((item) => ({
    serviceDefinitionId: item.serviceDefinitionId,
    unitType: item.unitType,
    requestedQuantity: item.requestedQuantity,
    ...(item.requestedAmount != null ? { requestedAmount: item.requestedAmount } : {}),
    ...(item.currencyCode ? { currencyCode: item.currencyCode } : {}),
  }));

/** The editor retains the version it was opened on. Background reads cannot silently overwrite unsaved input. */
export function RequestCorrection({
  current,
  reload,
}: {
  current: Versioned<ServiceRequest>;
  reload: () => Promise<unknown>;
}) {
  const { t } = useTranslation();
  const tenantId = useTenantId();
  const ops = useOps();
  const client = useQueryClient();
  const toast = useToast();
  const canEdit = usePermission('service_request.create');
  const canSubmit = usePermission('service_request.submit');
  const definitions = useServiceDefinitions();
  const [snapshot, setSnapshot] = useState(current);
  const [date, setDate] = useState(snapshot.data.serviceDate);
  const [lines, setLines] = useState(() => editableLines(snapshot.data));
  const [submitKey, setSubmitKey] = useState(randomId);
  const [reloading, setReloading] = useState(false);
  const request = snapshot.data;
  const version = useQuery({
    queryKey: ['provider', tenantId, 'request-version', request.id, request.currentVersionNo],
    queryFn: () => ops.requests.getVersion(tenantId, request.id, request.currentVersionNo),
    enabled: Boolean(request.returnReasonCode),
    retry: false,
  });
  const changedLines = JSON.stringify(lines) !== JSON.stringify(editableLines(request));
  const dirty = date !== request.serviceDate || changedLines;
  const stale = current.etag !== snapshot.etag;
  const accept = async (result: Versioned<ServiceRequest>, message: string) => {
    setSnapshot(result);
    setDate(result.data.serviceDate);
    setLines(editableLines(result.data));
    setSubmitKey(randomId());
    client.setQueryData(['provider', tenantId, 'request', request.id], result);
    await client.invalidateQueries({ queryKey: ['provider', tenantId, 'requests'] });
    toast.notify({ tone: 'success', title: t(message) });
  };
  const save = useMutation({
    mutationFn: async () => {
      let result = snapshot;
      if (date !== request.serviceDate) {
        result = await ops.requests.patch(tenantId, request.id, result.etag, { serviceDate: date });
      }
      if (changedLines)
        result = await ops.requests.putItems(tenantId, request.id, result.etag, {
          items: lines.map((line) => {
            const body = { ...line };
            if (!body.requestedAmount) delete body.requestedAmount;
            return body;
          }),
        });
      return result;
    },
    onSuccess: (result) => accept(result, 'provider.correction.saved'),
  });
  const submit = useMutation({
    mutationFn: () => ops.requests.submit(tenantId, request.id, snapshot.etag, submitKey),
    onSuccess: (result) => accept(result, 'provider.correction.sent'),
  });
  const locked =
    stale || save.isPending || submit.isPending || Boolean(save.error || submit.error) || reloading;
  const update = (index: number, patch: Partial<Line>) =>
    setLines((all) => all.map((line, i) => (i === index ? { ...line, ...patch } : line)));
  const decimal = /^\d+(\.\d{1,6})?$/;
  const dateValid =
    /^\d{4}-\d{2}-\d{2}$/.test(date) &&
    !Number.isNaN(Date.parse(date)) &&
    new Date(date).toISOString().slice(0, 10) === date;
  const errors = lines.map((line) => ({
    service: !line.serviceDefinitionId,
    quantity: !decimal.test(line.requestedQuantity) || !/[1-9]/.test(line.requestedQuantity),
    amount: Boolean(line.requestedAmount && !decimal.test(line.requestedAmount)),
    currency: !/^[A-Z]{3}$/.test(line.currencyCode ?? 'TRY'),
  }));
  const valid =
    dateValid && lines.length > 0 && errors.every((line) => !Object.values(line).some(Boolean));
  function saveChanges(event: FormEvent) {
    event.preventDefault();
    if (canEdit && dirty && valid && !locked) save.mutate();
  }

  return (
    <Card>
      <h2 className="text-base font-semibold">
        {t(`provider.correction.${request.returnReasonCode ? 'title' : 'draftTitle'}`)}
      </h2>
      <p className="text-fg-muted mt-1 mb-4 text-sm">{t('provider.correction.intro')}</p>
      {version.data?.returnReasonText ? (
        <p className="mb-4 whitespace-pre-wrap break-words text-sm">
          {version.data.returnReasonText}
        </p>
      ) : null}
      <ProblemAlert problem={version.error ? problemOf(version.error) : null} />
      <ProblemAlert
        problem={save.error || submit.error ? problemOf(save.error ?? submit.error) : null}
      />
      {stale || save.error || submit.error ? (
        <div className="mb-4 grid gap-2 text-sm" role="status">
          <p>
            {t(
              submit.error && !stale
                ? 'provider.correction.retryHint'
                : 'provider.correction.reloadHint',
            )}
          </p>
          <Button
            variant="secondary"
            loading={reloading}
            onClick={async () => {
              setReloading(true);
              try {
                await reload();
              } finally {
                setReloading(false);
              }
            }}
          >
            {t('provider.correction.reload')}
          </Button>
        </div>
      ) : null}
      <form onSubmit={saveChanges} className="grid gap-4" data-testid="request-correction">
        <fieldset disabled={locked || !canEdit} className="grid min-w-0 gap-4">
          <FormField
            label={t('provider.newRequest.serviceDate')}
            error={!dateValid ? t('provider.correction.dateError') : undefined}
            required
            requiredLabel={t('common.requiredMark')}
          >
            <Input type="date" value={date} onChange={(e) => setDate(e.target.value)} />
          </FormField>
          {lines.map((line, index) => (
            <fieldset
              key={index}
              className="grid min-w-0 gap-3 border-t border-line pt-3 md:grid-cols-2"
            >
              <legend className="px-1 text-sm font-medium">
                {t('provider.correction.line', { number: index + 1 })}
              </legend>
              <FormField
                label={t('requests.items.service')}
                required
                requiredLabel={t('common.requiredMark')}
                error={errors[index]?.service ? t('provider.correction.serviceError') : undefined}
              >
                <Select
                  value={line.serviceDefinitionId}
                  options={definitions.data ?? []}
                  onChange={(e) => {
                    const picked = definitions.data?.find((d) => d.value === e.target.value);
                    update(index, {
                      serviceDefinitionId: e.target.value,
                      ...(picked ? { unitType: picked.unitType } : {}),
                    });
                  }}
                />
              </FormField>
              <FormField
                label={t('provider.newRequest.quantity')}
                required
                requiredLabel={t('common.requiredMark')}
                error={errors[index]?.quantity ? t('provider.correction.quantityError') : undefined}
                hint={t(`units.${line.unitType}`)}
              >
                <Input
                  value={line.requestedQuantity}
                  inputMode="decimal"
                  className="text-right font-mono"
                  onChange={(e) =>
                    update(index, { requestedQuantity: e.target.value.replace(',', '.') })
                  }
                />
              </FormField>
              <FormField
                label={t('provider.newRequest.amount')}
                error={errors[index]?.amount ? t('provider.correction.amountError') : undefined}
              >
                <Input
                  value={line.requestedAmount ?? ''}
                  inputMode="decimal"
                  className="text-right font-mono"
                  onChange={(e) =>
                    update(index, {
                      requestedAmount: e.target.value.replace(',', '.'),
                      currencyCode: line.currencyCode ?? 'TRY',
                    })
                  }
                />
              </FormField>
              <FormField
                label={t('provider.correction.currency')}
                error={errors[index]?.currency ? t('provider.correction.currencyError') : undefined}
              >
                <Input
                  value={line.currencyCode ?? 'TRY'}
                  maxLength={3}
                  onChange={(e) => update(index, { currencyCode: e.target.value.toUpperCase() })}
                />
              </FormField>
              {lines.length > 1 ? (
                <Button
                  variant="ghost"
                  onClick={() => setLines((all) => all.filter((_, i) => i !== index))}
                >
                  {t('requests.items.remove')}
                </Button>
              ) : null}
            </fieldset>
          ))}
          {canEdit && lines.length < 100 ? (
            <Button
              variant="secondary"
              onClick={() =>
                setLines((all) => [
                  ...all,
                  {
                    serviceDefinitionId: definitions.data?.[0]?.value ?? '',
                    unitType: definitions.data?.[0]?.unitType ?? 'SESSION',
                    requestedQuantity: '1',
                  },
                ])
              }
            >
              {t('requests.items.add')}
            </Button>
          ) : null}
        </fieldset>
        <div className="flex flex-wrap justify-end gap-2">
          {canEdit && dirty && !save.error && !submit.error && !stale ? (
            <Button type="submit" disabled={!valid} loading={save.isPending}>
              {t('provider.correction.save')}
            </Button>
          ) : null}
          {canSubmit && !dirty && !save.error && !stale ? (
            <Button
              type="button"
              loading={submit.isPending}
              onClick={() => {
                if (!submit.isPending && !reloading) submit.mutate();
              }}
            >
              {t('provider.correction.submit')}
            </Button>
          ) : null}
        </div>
      </form>
    </Card>
  );
}
