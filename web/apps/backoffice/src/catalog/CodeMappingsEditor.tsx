import type { Problem, ServiceCodeMapping, ServiceCodeMappingInput } from '@kapsora/api-client';
import { formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
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
import { zodResolver } from '@hookform/resolvers/zod';
import { useFieldArray, useForm } from 'react-hook-form';

import { todayIso, useFieldMessage, useServerFieldErrors } from './forms';
import { useCodeSystems } from './queries';
import {
  emptyMappingRow,
  mappingsFormSchema,
  type MappingRowValues,
  type MappingsFormValues,
} from './schema';

/** How many systems the picker loads; a tenant runs a handful, not hundreds. */
const SYSTEM_LIMIT = 200;

function toRow(mapping: ServiceCodeMapping): MappingRowValues {
  return {
    codeSystemId: mapping.codeSystemId,
    code: mapping.code,
    validFrom: mapping.validFrom,
    validTo: mapping.validTo ?? '',
    primary: mapping.primary,
  };
}

export interface CodeMappingsEditorProps {
  mappings: ServiceCodeMapping[];
  /** Saves the whole set; the API replaces the list rather than patching rows. */
  onSave: (items: ServiceCodeMappingInput[]) => Promise<void>;
  saving: boolean;
  problem: Problem | null;
}

/**
 * The external codes a service is reported under, edited as one set. Each row carries its
 * own period, because a code system reissues a code on a date and the claim from before
 * that date must still resolve to the old one. Two rows of the same system that overlap
 * are refused by the server as a conflict, which is the only place that rule can be
 * decided: the set being saved is not the only set in play.
 */
export function CodeMappingsEditor({ mappings, onSave, saving, problem }: CodeMappingsEditorProps) {
  const { t } = useTranslation();
  const systems = useCodeSystems({ limit: SYSTEM_LIMIT });

  const form = useForm<MappingsFormValues>({
    resolver: zodResolver(mappingsFormSchema),
    defaultValues: { items: mappings.map(toRow) },
    mode: 'onBlur',
  });
  const rows = useFieldArray({ control: form.control, name: 'items' });
  useServerFieldErrors(form, problem);
  const message = useFieldMessage();

  const systemOptions = (systems.data?.items ?? []).map((system) => ({
    value: system.id,
    label: `${system.code} ${system.version}`,
  }));

  async function submit(values: MappingsFormValues) {
    const items: ServiceCodeMappingInput[] = values.items.map((row) => ({
      codeSystemId: row.codeSystemId,
      code: row.code,
      validFrom: row.validFrom,
      primary: row.primary,
      ...(row.validTo !== '' ? { validTo: row.validTo } : {}),
    }));
    await onSave(items);
  }

  return (
    <form
      onSubmit={form.handleSubmit(submit)}
      className="grid gap-4"
      noValidate
      data-testid="mappings-form"
    >
      <ProblemAlert problem={problem} hideFieldErrors />

      {rows.fields.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('catalog.mappings.empty')}</p>
      ) : (
        <div className="grid gap-4">
          {rows.fields.map((field, index) => {
            const errors = form.formState.errors.items?.[index];
            return (
              <fieldset key={field.id} className="border-line grid gap-3 rounded-md border p-4">
                <legend className="text-fg-muted px-1 text-xs font-medium">
                  {t('catalog.mappings.columns.system')}
                </legend>
                <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-4">
                  <FormField
                    label={t('catalog.mappings.columns.system')}
                    required
                    requiredLabel={t('common.requiredMark')}
                    error={message(errors?.codeSystemId)}
                  >
                    <Select
                      {...form.register(`items.${index}.codeSystemId`)}
                      placeholder={t('common.none')}
                      options={systemOptions}
                    />
                  </FormField>
                  <FormField
                    label={t('catalog.mappings.columns.code')}
                    required
                    requiredLabel={t('common.requiredMark')}
                    error={message(errors?.code)}
                  >
                    <Input
                      {...form.register(`items.${index}.code`)}
                      autoComplete="off"
                      className="font-mono"
                    />
                  </FormField>
                  <FormField
                    label={t('codeSystems.fields.validFrom')}
                    required
                    requiredLabel={t('common.requiredMark')}
                    error={message(errors?.validFrom)}
                  >
                    <Input {...form.register(`items.${index}.validFrom`)} type="date" />
                  </FormField>
                  <FormField
                    label={t('codeSystems.fields.validTo')}
                    error={message(errors?.validTo)}
                  >
                    <Input {...form.register(`items.${index}.validTo`)} type="date" />
                  </FormField>
                </div>
                <div className="flex flex-wrap items-center gap-6 text-sm">
                  <label className="flex items-center gap-2">
                    <input type="checkbox" {...form.register(`items.${index}.primary`)} />
                    {t('catalog.mappings.columns.primary')}
                  </label>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="ml-auto"
                    onClick={() => rows.remove(index)}
                  >
                    {t('catalog.mappings.remove')}
                  </Button>
                </div>
              </fieldset>
            );
          })}
        </div>
      )}

      <div className="flex justify-between gap-2">
        <Button variant="secondary" onClick={() => rows.append(emptyMappingRow(todayIso()))}>
          {t('catalog.mappings.add')}
        </Button>
        <Button type="submit" loading={saving}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/** The same set for an operator who may read the catalog but not change it. */
export function CodeMappingsTable({ mappings }: { mappings: ServiceCodeMapping[] }) {
  const { t } = useTranslation();
  if (mappings.length === 0) {
    return <p className="text-fg-muted text-sm">{t('catalog.mappings.empty')}</p>;
  }
  return (
    <Table data-testid="mapping-table">
      <THead>
        <TR>
          <TH>{t('catalog.mappings.columns.system')}</TH>
          <TH>{t('catalog.mappings.columns.code')}</TH>
          <TH>{t('catalog.mappings.columns.period')}</TH>
          <TH>{t('catalog.mappings.columns.primary')}</TH>
        </TR>
      </THead>
      <TBody>
        {mappings.map((mapping) => (
          <TR key={mapping.id}>
            <TD>
              <code className="font-mono text-xs">
                {mapping.codeSystemCode} {mapping.codeSystemVersion ?? ''}
              </code>
            </TD>
            <TD>
              <code className="font-mono text-xs">{mapping.code}</code>
            </TD>
            <TD>
              {formatDate(mapping.validFrom)} – {formatDate(mapping.validTo) || t('common.none')}
            </TD>
            <TD>
              {mapping.primary ? (
                <Badge tone="info">{t('catalog.mappings.columns.primary')}</Badge>
              ) : (
                t('common.none')
              )}
            </TD>
          </TR>
        ))}
      </TBody>
    </Table>
  );
}
