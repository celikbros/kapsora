import type { EntitlementDefinition, EntitlementDefinitionInput } from '@kapsora/api-client';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import {
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

import { parseIssueMessage, type problemOf } from '../problems';
import {
  PERIOD_TYPES,
  ROLLOVER_POLICIES,
  UNIT_TYPES,
  definitionsFormSchema,
  emptyDefinitionRow,
  type DefinitionRowValues,
  type DefinitionsFormValues,
} from './schema';

export interface DefinitionsEditorProps {
  definitions: EntitlementDefinition[];
  /** Saves the whole set; the API replaces the list rather than patching rows. */
  onSave: (items: EntitlementDefinitionInput[]) => Promise<void>;
  saving: boolean;
  problem: ReturnType<typeof problemOf> | null;
}

/** An existing definition as the form holds it: every quantity stays a string. */
function toRow(definition: EntitlementDefinition): DefinitionRowValues {
  return {
    code: definition.code,
    name: definition.name,
    unitType: definition.unitType,
    currencyCode: definition.currencyCode ?? '',
    periodType: definition.periodType,
    periodLength: definition.periodLength == null ? '' : String(definition.periodLength),
    initialQuantity: definition.initialQuantity,
    rolloverPolicy: definition.rolloverPolicy ?? 'NONE',
    rolloverCap: definition.rolloverCap ?? '',
    allowOverdraft: definition.allowOverdraft ?? false,
    familyShared: definition.familyShared ?? false,
  };
}

/**
 * The entitlement definitions of a draft version, edited as a set. `initialQuantity` and
 * `rolloverCap` travel as decimal strings from the input straight to the wire; parsing
 * them into a JavaScript number would round money the ledger keeps exact.
 */
export function DefinitionsEditor({
  definitions,
  onSave,
  saving,
  problem,
}: DefinitionsEditorProps) {
  const { t } = useTranslation();
  const form = useForm<DefinitionsFormValues>({
    resolver: zodResolver(definitionsFormSchema),
    defaultValues: {
      items: definitions.length > 0 ? definitions.map(toRow) : [emptyDefinitionRow],
    },
    mode: 'onBlur',
  });
  const rows = useFieldArray({ control: form.control, name: 'items' });
  const watched = form.watch('items');

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function submit(values: DefinitionsFormValues) {
    const items: EntitlementDefinitionInput[] = values.items.map((row) => ({
      code: row.code,
      name: row.name,
      unitType: row.unitType,
      periodType: row.periodType,
      initialQuantity: row.initialQuantity,
      rolloverPolicy: row.rolloverPolicy,
      allowOverdraft: row.allowOverdraft,
      familyShared: row.familyShared,
      ...(row.currencyCode !== '' ? { currencyCode: row.currencyCode } : {}),
      ...(row.periodLength !== '' ? { periodLength: Number(row.periodLength) } : {}),
      ...(row.rolloverCap !== '' ? { rolloverCap: row.rolloverCap } : {}),
    }));
    await onSave(items);
  }

  return (
    <form
      onSubmit={form.handleSubmit(submit)}
      className="grid gap-4"
      noValidate
      data-testid="definitions-form"
    >
      <ProblemAlert problem={problem} />

      <div className="grid gap-4">
        {rows.fields.map((field, index) => {
          const row = watched[index];
          const errors = form.formState.errors.items?.[index];
          const isMoney = row?.unitType === 'MONEY';
          return (
            <fieldset key={field.id} className="border-line grid gap-3 rounded-md border p-4">
              <legend className="text-fg-muted px-1 text-xs font-medium">
                {row?.code || t('plans.definitions.columns.code')}
              </legend>
              <div className="grid gap-3 md:grid-cols-3">
                <FormField
                  label={t('plans.definitions.fields.code')}
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
                  label={t('plans.definitions.fields.name')}
                  required
                  requiredLabel={t('common.requiredMark')}
                  error={message(errors?.name)}
                >
                  <Input {...form.register(`items.${index}.name`)} />
                </FormField>
                <FormField label={t('plans.definitions.fields.unitType')}>
                  <Select
                    {...form.register(`items.${index}.unitType`)}
                    options={UNIT_TYPES.map((unit) => ({
                      value: unit,
                      label: t(`plans.definitions.units.${unit}`),
                    }))}
                  />
                </FormField>
                {isMoney ? (
                  <FormField
                    label={t('plans.definitions.fields.currencyCode')}
                    required
                    requiredLabel={t('common.requiredMark')}
                    error={message(errors?.currencyCode)}
                  >
                    <Input
                      {...form.register(`items.${index}.currencyCode`)}
                      className="font-mono"
                      maxLength={3}
                    />
                  </FormField>
                ) : null}
                <FormField
                  label={t('plans.definitions.fields.initialQuantity')}
                  required
                  requiredLabel={t('common.requiredMark')}
                  error={message(errors?.initialQuantity)}
                >
                  <Input
                    {...form.register(`items.${index}.initialQuantity`)}
                    inputMode="decimal"
                    autoComplete="off"
                    className="text-right font-mono"
                  />
                </FormField>
                <FormField label={t('plans.definitions.fields.periodType')}>
                  <Select
                    {...form.register(`items.${index}.periodType`)}
                    options={PERIOD_TYPES.map((periodType) => ({
                      value: periodType,
                      label: t(`plans.definitions.periods.${periodType}`),
                    }))}
                  />
                </FormField>
                {row?.periodType === 'ROLLING_DAYS' ? (
                  <FormField
                    label={t('plans.definitions.fields.periodLength')}
                    required
                    requiredLabel={t('common.requiredMark')}
                    error={message(errors?.periodLength)}
                  >
                    <Input
                      {...form.register(`items.${index}.periodLength`)}
                      inputMode="numeric"
                      className="text-right font-mono"
                    />
                  </FormField>
                ) : null}
                <FormField label={t('plans.definitions.fields.rolloverPolicy')}>
                  <Select
                    {...form.register(`items.${index}.rolloverPolicy`)}
                    options={ROLLOVER_POLICIES.map((policy) => ({
                      value: policy,
                      label: t(`plans.definitions.rollover.${policy}`),
                    }))}
                  />
                </FormField>
                {row?.rolloverPolicy === 'CAPPED' ? (
                  <FormField
                    label={t('plans.definitions.fields.rolloverCap')}
                    required
                    requiredLabel={t('common.requiredMark')}
                    error={message(errors?.rolloverCap)}
                  >
                    <Input
                      {...form.register(`items.${index}.rolloverCap`)}
                      inputMode="decimal"
                      className="text-right font-mono"
                    />
                  </FormField>
                ) : null}
              </div>
              <div className="flex flex-wrap items-center gap-6 text-sm">
                <label className="flex items-center gap-2">
                  <input type="checkbox" {...form.register(`items.${index}.familyShared`)} />
                  {t('plans.definitions.fields.familyShared')}
                </label>
                <label className="flex items-center gap-2">
                  <input type="checkbox" {...form.register(`items.${index}.allowOverdraft`)} />
                  {t('plans.definitions.fields.allowOverdraft')}
                </label>
                <Button
                  variant="ghost"
                  size="sm"
                  className="ml-auto"
                  onClick={() => rows.remove(index)}
                  disabled={rows.fields.length === 1}
                >
                  {t('plans.definitions.remove')}
                </Button>
              </div>
            </fieldset>
          );
        })}
      </div>

      <div className="flex justify-between gap-2">
        <Button variant="secondary" onClick={() => rows.append(emptyDefinitionRow)}>
          {t('plans.definitions.add')}
        </Button>
        <Button type="submit" loading={saving}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/** Read-only view of the definitions of a version nobody may change any more. */
export function DefinitionsTable({ definitions }: { definitions: EntitlementDefinition[] }) {
  const { t } = useTranslation();
  if (definitions.length === 0) {
    return <p className="text-fg-muted text-sm">{t('plans.definitions.empty')}</p>;
  }
  return (
    <Table data-testid="definition-table">
      <THead>
        <TR>
          <TH>{t('plans.definitions.columns.code')}</TH>
          <TH>{t('plans.definitions.columns.name')}</TH>
          <TH>{t('plans.definitions.columns.unitType')}</TH>
          <TH>{t('plans.definitions.columns.period')}</TH>
          <TH>{t('plans.definitions.columns.initialQuantity')}</TH>
          <TH>{t('plans.definitions.columns.flags')}</TH>
        </TR>
      </THead>
      <TBody>
        {definitions.map((definition) => (
          <TR key={definition.id}>
            <TD>
              <code className="font-mono text-xs">{definition.code}</code>
            </TD>
            <TD>{definition.name}</TD>
            <TD>
              {t(`plans.definitions.units.${definition.unitType}`)}
              {definition.currencyCode ? ` · ${definition.currencyCode}` : ''}
            </TD>
            <TD>
              {t(`plans.definitions.periods.${definition.periodType}`)}
              {definition.periodLength ? ` · ${definition.periodLength}` : ''}
            </TD>
            <TD className="text-right font-mono">{definition.initialQuantity}</TD>
            <TD className="text-fg-muted text-xs">
              {[
                definition.familyShared ? t('plans.definitions.fields.familyShared') : '',
                definition.allowOverdraft ? t('plans.definitions.fields.allowOverdraft') : '',
              ]
                .filter(Boolean)
                .join(' · ') || t('common.none')}
            </TD>
          </TR>
        ))}
      </TBody>
    </Table>
  );
}
