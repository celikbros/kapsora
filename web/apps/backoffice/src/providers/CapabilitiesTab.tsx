import type {
  ProviderCapability,
  ProviderCapabilityInput,
  ProviderLocation,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { fieldErrorMessage, formatDate, useTranslation } from '@kapsora/i18n';
import {
  Button,
  Card,
  EmptyState,
  FormField,
  Input,
  ProblemAlert,
  Select,
  Spinner,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { useState } from 'react';
import { useFieldArray, useForm } from 'react-hook-form';

import { problemOf } from '../problems';
import {
  useCapabilities,
  useProviderLocations,
  useReplaceCapabilities,
  useServiceOptions,
} from './queries';
import {
  CAPABILITY_TARGETS,
  capabilitiesFormSchema,
  emptyCapabilityRow,
  parseIssueMessage,
  type CapabilitiesFormValues,
  type CapabilityRowValues,
} from './schema';

/**
 * What a location can deliver. Capabilities are written as a set: the editor holds the
 * whole list and one PUT replaces it, so a removed row is genuinely gone rather than
 * lingering as a row nobody patched.
 */
export function CapabilitiesTab({ providerId }: { providerId: string }) {
  const { t } = useTranslation();
  const locations = useProviderLocations(providerId, { limit: 200 });
  const [locationId, setLocationId] = useState('');

  const rows: ProviderLocation[] = locations.data?.items ?? [];
  const selected = rows.find((location) => location.id === locationId) ?? rows[0];

  if (locations.isPending) {
    return (
      <Card>
        <div className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      </Card>
    );
  }

  if (!selected) {
    return (
      <Card>
        <EmptyState title={t('locations.empty')} />
      </Card>
    );
  }

  return (
    <Card>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold">{t('capabilities.title')}</h2>
          <p className="text-fg-muted mt-1 max-w-prose text-sm">{t('capabilities.intro')}</p>
        </div>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('locations.title')}</span>
          <Select
            value={selected.id}
            onChange={(e) => setLocationId(e.target.value)}
            options={rows.map((location) => ({
              value: location.id,
              label: `${location.code} · ${location.name}`,
            }))}
            className="w-64"
          />
        </label>
      </div>

      <CapabilitySet key={selected.id} location={selected} />
    </Card>
  );
}

/** The set of one location, loaded then edited or shown read-only. */
function CapabilitySet({ location }: { location: ProviderLocation }) {
  const { t } = useTranslation();
  const canManage = usePermission('provider.manage');
  const capabilities = useCapabilities(location.id);

  if (capabilities.isPending) {
    return (
      <div className="text-fg-muted mt-4 flex items-center gap-2 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (capabilities.error || !capabilities.data) {
    return (
      <ProblemAlert
        page
        problem={problemOf(capabilities.error)}
        className="mt-4"
        actions={
          <Button size="sm" variant="secondary" onClick={() => void capabilities.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />
    );
  }

  if (!canManage) {
    return <CapabilityTable items={capabilities.data} />;
  }
  return <CapabilityEditor location={location} items={capabilities.data} />;
}

function toRow(capability: ProviderCapability): CapabilityRowValues {
  return {
    targetType: capability.serviceDefinitionId ? 'DEFINITION' : 'CATEGORY',
    serviceDefinitionId: capability.serviceDefinitionId ?? '',
    serviceCategoryId: capability.serviceCategoryId ?? '',
    validFrom: capability.validFrom,
    validTo: capability.validTo ?? '',
    notes: capability.notes ?? '',
  };
}

function CapabilityEditor({
  location,
  items,
}: {
  location: ProviderLocation;
  items: ProviderCapability[];
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const options = useServiceOptions();
  const save = useReplaceCapabilities(location.id);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);

  const form = useForm<CapabilitiesFormValues>({
    resolver: zodResolver(capabilitiesFormSchema),
    defaultValues: { items: items.map(toRow) },
    mode: 'onBlur',
  });
  const rows = useFieldArray({ control: form.control, name: 'items' });
  const watched = form.watch('items');

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function submit(values: CapabilitiesFormValues) {
    setProblem(null);
    const payload: ProviderCapabilityInput[] = values.items.map((row) => ({
      validFrom: row.validFrom,
      ...(row.targetType === 'DEFINITION'
        ? { serviceDefinitionId: row.serviceDefinitionId }
        : { serviceCategoryId: row.serviceCategoryId }),
      ...(row.validTo ? { validTo: row.validTo } : {}),
      ...(row.notes ? { notes: row.notes } : {}),
    }));
    try {
      await save.mutateAsync({ etag: `"${location.rowVersion}"`, items: payload });
      toast.notify({ tone: 'success', title: t('capabilities.saved') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  return (
    <form
      onSubmit={form.handleSubmit(submit)}
      className="mt-4 grid gap-4"
      noValidate
      data-testid="capability-form"
    >
      <ProblemAlert problem={problem} />

      {rows.fields.length === 0 ? (
        <EmptyState title={t('capabilities.empty')} />
      ) : (
        <div className="grid gap-4">
          {rows.fields.map((field, index) => {
            const row = watched[index];
            const errors = form.formState.errors.items?.[index];
            const isDefinition = row?.targetType !== 'CATEGORY';
            return (
              <fieldset key={field.id} className="border-line grid gap-3 rounded-md border p-4">
                <legend className="text-fg-muted px-1 text-xs font-medium">
                  {t(`capabilities.targets.${row?.targetType ?? 'DEFINITION'}`)}
                </legend>
                <div className="grid gap-3 md:grid-cols-3">
                  <FormField label={t('capabilities.fields.targetType')}>
                    <Select
                      {...form.register(`items.${index}.targetType`)}
                      options={CAPABILITY_TARGETS.map((target) => ({
                        value: target,
                        label: t(`capabilities.targets.${target}`),
                      }))}
                    />
                  </FormField>
                  {isDefinition ? (
                    <FormField
                      label={t('capabilities.fields.definition')}
                      required
                      requiredLabel={t('common.requiredMark')}
                      error={message(errors?.serviceDefinitionId)}
                    >
                      <Select
                        {...form.register(`items.${index}.serviceDefinitionId`)}
                        placeholder={t('common.none')}
                        options={(options.data?.definitions ?? []).map((definition) => ({
                          value: definition.id,
                          label: `${definition.code} · ${definition.name}`,
                        }))}
                      />
                    </FormField>
                  ) : (
                    <FormField
                      label={t('capabilities.fields.category')}
                      required
                      requiredLabel={t('common.requiredMark')}
                      error={message(errors?.serviceCategoryId)}
                    >
                      <Select
                        {...form.register(`items.${index}.serviceCategoryId`)}
                        placeholder={t('common.none')}
                        options={(options.data?.categories ?? []).map((category) => ({
                          value: category.id,
                          label: `${category.code} · ${category.name}`,
                        }))}
                      />
                    </FormField>
                  )}
                  <FormField label={t('capabilities.columns.notes')} error={message(errors?.notes)}>
                    <Input {...form.register(`items.${index}.notes`)} />
                  </FormField>
                  <FormField
                    label={t('capabilities.fields.validFrom')}
                    required
                    requiredLabel={t('common.requiredMark')}
                    error={message(errors?.validFrom)}
                  >
                    <Input {...form.register(`items.${index}.validFrom`)} type="date" />
                  </FormField>
                  <FormField
                    label={t('capabilities.fields.validTo')}
                    error={message(errors?.validTo)}
                  >
                    <Input {...form.register(`items.${index}.validTo`)} type="date" />
                  </FormField>
                </div>
                <div className="flex justify-end">
                  <Button variant="ghost" size="sm" onClick={() => rows.remove(index)}>
                    {t('capabilities.remove')}
                  </Button>
                </div>
              </fieldset>
            );
          })}
        </div>
      )}

      <div className="flex justify-between gap-2">
        <Button variant="secondary" onClick={() => rows.append(emptyCapabilityRow)}>
          {t('capabilities.add')}
        </Button>
        <Button type="submit" loading={save.isPending}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/** What an operator who may only read sees. */
function CapabilityTable({ items }: { items: ProviderCapability[] }) {
  const { t } = useTranslation();
  if (items.length === 0) {
    return (
      <div className="mt-4">
        <EmptyState title={t('capabilities.empty')} />
      </div>
    );
  }
  return (
    <div className="mt-4">
      <Table data-testid="capability-table">
        <THead>
          <TR>
            <TH>{t('capabilities.columns.target')}</TH>
            <TH>{t('capabilities.columns.period')}</TH>
            <TH>{t('capabilities.columns.notes')}</TH>
          </TR>
        </THead>
        <TBody>
          {items.map((capability) => (
            <TR key={capability.id}>
              <TD>
                <span className="text-fg-muted text-xs">
                  {t(
                    `capabilities.targets.${capability.serviceDefinitionId ? 'DEFINITION' : 'CATEGORY'}`,
                  )}
                </span>{' '}
                <code className="font-mono text-xs">
                  {capability.serviceDefinitionCode ??
                    capability.serviceCategoryCode ??
                    t('common.none')}
                </code>
              </TD>
              <TD>
                {formatDate(capability.validFrom)}
                {capability.validTo ? ` – ${formatDate(capability.validTo)}` : ''}
              </TD>
              <TD className="text-fg-muted text-xs">{capability.notes ?? t('common.none')}</TD>
            </TR>
          ))}
        </TBody>
      </Table>
    </div>
  );
}
