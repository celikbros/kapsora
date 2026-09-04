import type { PriceItem, PriceItemInput } from '@kapsora/api-client';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import { Button, Input, ProblemAlert, Select } from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { useFieldArray, useForm } from 'react-hook-form';

import { parseIssueMessage, type problemOf } from '../problems';
import {
  PRICING_METHODS,
  SCOPE_TYPES,
  SHARE_METHODS,
  UNIT_TYPES,
  emptyPriceItemRow,
  priceItemsFormSchema,
  type PriceItemRowValues,
  type PriceItemsFormValues,
} from './schema';

export interface Option {
  value: string;
  label: string;
}

export interface PriceItemsEditorProps {
  items: PriceItem[];
  definitions: Option[];
  categories: Option[];
  packages: Option[];
  locations: Option[];
  onSave: (items: PriceItemInput[]) => Promise<void>;
  saving: boolean;
  problem: ReturnType<typeof problemOf> | null;
}

/** A stored row as the form holds it: every amount stays the text the API sent. */
function toRow(item: PriceItem): PriceItemRowValues {
  const scopeType = item.packageDefinitionId
    ? 'PACKAGE'
    : item.serviceCategoryId
      ? 'CATEGORY'
      : 'DEFINITION';
  return {
    scopeType,
    serviceDefinitionId: item.serviceDefinitionId ?? '',
    serviceCategoryId: item.serviceCategoryId ?? '',
    packageDefinitionId: item.packageDefinitionId ?? '',
    locationId: item.locationId ?? '',
    unitType: item.unitType,
    pricingMethod: item.pricingMethod,
    amount: item.amount ?? '',
    percent: item.percent ?? '',
    formulaKey: item.formulaKey ?? '',
    minAmount: item.minAmount ?? '',
    maxAmount: item.maxAmount ?? '',
    memberShareMethod: item.memberShareMethod,
    memberShareAmount: item.memberShareAmount ?? '',
    memberSharePercent: item.memberSharePercent ?? '',
    validFrom: item.validFrom,
    validTo: item.validTo ?? '',
    priority: String(item.priority ?? 100),
  };
}

function toInput(row: PriceItemRowValues): PriceItemInput {
  const out: PriceItemInput = {
    unitType: row.unitType,
    pricingMethod: row.pricingMethod,
    validFrom: row.validFrom,
    priority: Number(row.priority),
    memberShareMethod: row.memberShareMethod,
  };
  if (row.scopeType === 'DEFINITION') out.serviceDefinitionId = row.serviceDefinitionId;
  if (row.scopeType === 'CATEGORY') out.serviceCategoryId = row.serviceCategoryId;
  if (row.scopeType === 'PACKAGE') out.packageDefinitionId = row.packageDefinitionId;
  if (row.locationId !== '') out.locationId = row.locationId;
  if (row.amount !== '') out.amount = row.amount;
  if (row.percent !== '') out.percent = row.percent;
  if (row.formulaKey !== '') out.formulaKey = row.formulaKey;
  if (row.minAmount !== '') out.minAmount = row.minAmount;
  if (row.maxAmount !== '') out.maxAmount = row.maxAmount;
  if (row.memberShareAmount !== '') out.memberShareAmount = row.memberShareAmount;
  if (row.memberSharePercent !== '') out.memberSharePercent = row.memberSharePercent;
  if (row.validTo !== '') out.validTo = row.validTo;
  return out;
}

/**
 * The price sheet: the densest screen in the product, and the one a contract manager
 * spends the most time in.
 *
 * It is a table rather than a row of cards, and it edits in place rather than opening a
 * modal per row, because the work is comparing thirty rows against each other — which
 * service, at which location, for how much — and a modal hides the very thing being
 * compared. Money is right-aligned and monospaced so the columns line up as digits, and
 * each row shows only the fields its own pricing method uses, so a FIXED row is not four
 * empty inputs wide.
 */
export function PriceItemsEditor({
  items,
  definitions,
  categories,
  packages,
  locations,
  onSave,
  saving,
  problem,
}: PriceItemsEditorProps) {
  const { t } = useTranslation();
  const form = useForm<PriceItemsFormValues>({
    resolver: zodResolver(priceItemsFormSchema),
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

  async function submit(values: PriceItemsFormValues) {
    await onSave(values.items.map(toInput));
  }

  const scopeOptions = (row: PriceItemRowValues | undefined) => {
    switch (row?.scopeType) {
      case 'CATEGORY':
        return categories;
      case 'PACKAGE':
        return packages;
      default:
        return definitions;
    }
  };

  const scopeField = (index: number, row: PriceItemRowValues | undefined) => {
    const name =
      row?.scopeType === 'CATEGORY'
        ? (`items.${index}.serviceCategoryId` as const)
        : row?.scopeType === 'PACKAGE'
          ? (`items.${index}.packageDefinitionId` as const)
          : (`items.${index}.serviceDefinitionId` as const);
    return <Select {...form.register(name)} options={scopeOptions(row)} placeholder="—" />;
  };

  return (
    <form
      onSubmit={form.handleSubmit(submit)}
      className="grid gap-4"
      noValidate
      data-testid="price-items-form"
    >
      <ProblemAlert problem={problem} />
      <p className="text-fg-muted text-sm">{t('priceItems.priorityHint')}</p>

      {rows.fields.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('priceItems.empty')}</p>
      ) : (
        <div className="border-line overflow-x-auto rounded-md border">
          <table className="w-full text-sm" data-testid="price-items-table">
            <thead className="bg-surface-sunken text-fg-muted text-xs">
              <tr>
                <th className="px-2 py-2 text-left font-medium">{t('priceItems.columns.scope')}</th>
                <th className="px-2 py-2 text-left font-medium">
                  {t('priceItems.columns.location')}
                </th>
                <th className="px-2 py-2 text-left font-medium">
                  {t('priceItems.columns.method')}
                </th>
                <th className="px-2 py-2 text-right font-medium">
                  {t('priceItems.columns.amount')}
                </th>
                <th className="px-2 py-2 text-right font-medium">
                  {t('priceItems.columns.bounds')}
                </th>
                <th className="px-2 py-2 text-left font-medium">
                  {t('priceItems.columns.memberShare')}
                </th>
                <th className="px-2 py-2 text-left font-medium">
                  {t('priceItems.columns.period')}
                </th>
                <th className="px-2 py-2 text-right font-medium">
                  {t('priceItems.columns.priority')}
                </th>
                <th className="px-2 py-2" aria-label={t('common.actions')} />
              </tr>
            </thead>
            <tbody>
              {rows.fields.map((field, index) => {
                const row = watched[index];
                const errors = form.formState.errors.items?.[index];
                const isPercent = row?.pricingMethod === 'PERCENT_OF_LIST';
                const isFormula = row?.pricingMethod === 'FORMULA';
                return (
                  <tr key={field.id} className="border-line border-t align-top">
                    <td className="px-2 py-2">
                      <div className="grid gap-1">
                        <Select
                          {...form.register(`items.${index}.scopeType`)}
                          options={SCOPE_TYPES.map((s) => ({
                            value: s,
                            label: t(`priceItems.scopes.${s}`),
                          }))}
                          className="w-28"
                          aria-label={t('priceItems.fields.scopeType')}
                        />
                        {scopeField(index, row)}
                        {(message(errors?.serviceDefinitionId) ??
                        message(errors?.serviceCategoryId) ??
                        message(errors?.packageDefinitionId)) ? (
                          <span className="text-danger text-xs">
                            {message(errors?.serviceDefinitionId) ??
                              message(errors?.serviceCategoryId) ??
                              message(errors?.packageDefinitionId)}
                          </span>
                        ) : null}
                      </div>
                    </td>
                    <td className="px-2 py-2">
                      <Select
                        {...form.register(`items.${index}.locationId`)}
                        options={locations}
                        placeholder={t('priceItems.anyLocation')}
                        className="w-40"
                        aria-label={t('priceItems.fields.location')}
                      />
                    </td>
                    <td className="px-2 py-2">
                      <div className="grid gap-1">
                        <Select
                          {...form.register(`items.${index}.pricingMethod`)}
                          options={PRICING_METHODS.map((m) => ({
                            value: m,
                            label: t(`priceItems.methods.${m}`),
                          }))}
                          className="w-36"
                          aria-label={t('priceItems.fields.method')}
                        />
                        <Select
                          {...form.register(`items.${index}.unitType`)}
                          options={UNIT_TYPES.map((u) => ({
                            value: u,
                            label: t(`plans.definitions.units.${u}`),
                          }))}
                          className="w-36"
                          aria-label={t('priceItems.fields.unitType')}
                        />
                      </div>
                    </td>
                    <td className="px-2 py-2">
                      <div className="grid gap-1">
                        {isFormula ? (
                          <Input
                            {...form.register(`items.${index}.formulaKey`)}
                            className="w-32 font-mono"
                            aria-label={t('priceItems.fields.formulaKey')}
                          />
                        ) : (
                          <Input
                            {...form.register(
                              isPercent ? `items.${index}.percent` : `items.${index}.amount`,
                            )}
                            inputMode="decimal"
                            className="w-32 text-right font-mono"
                            aria-label={
                              isPercent
                                ? t('priceItems.fields.percent')
                                : t('priceItems.fields.amount')
                            }
                          />
                        )}
                        {(message(errors?.amount) ??
                        message(errors?.percent) ??
                        message(errors?.formulaKey)) ? (
                          <span className="text-danger text-xs">
                            {message(errors?.amount) ??
                              message(errors?.percent) ??
                              message(errors?.formulaKey)}
                          </span>
                        ) : null}
                      </div>
                    </td>
                    <td className="px-2 py-2">
                      <div className="grid gap-1">
                        <Input
                          {...form.register(`items.${index}.minAmount`)}
                          inputMode="decimal"
                          className="w-24 text-right font-mono"
                          aria-label={t('priceItems.fields.minAmount')}
                        />
                        <Input
                          {...form.register(`items.${index}.maxAmount`)}
                          inputMode="decimal"
                          className="w-24 text-right font-mono"
                          aria-label={t('priceItems.fields.maxAmount')}
                        />
                      </div>
                    </td>
                    <td className="px-2 py-2">
                      <div className="grid gap-1">
                        <Select
                          {...form.register(`items.${index}.memberShareMethod`)}
                          options={SHARE_METHODS.map((m) => ({
                            value: m,
                            label: t(`priceItems.shareMethods.${m}`),
                          }))}
                          className="w-28"
                          aria-label={t('priceItems.fields.shareMethod')}
                        />
                        {row?.memberShareMethod === 'FIXED' ? (
                          <Input
                            {...form.register(`items.${index}.memberShareAmount`)}
                            inputMode="decimal"
                            className="w-28 text-right font-mono"
                            aria-label={t('priceItems.fields.shareAmount')}
                          />
                        ) : null}
                        {row?.memberShareMethod === 'PERCENT' ? (
                          <Input
                            {...form.register(`items.${index}.memberSharePercent`)}
                            inputMode="decimal"
                            className="w-28 text-right font-mono"
                            aria-label={t('priceItems.fields.sharePercent')}
                          />
                        ) : null}
                        {(message(errors?.memberShareAmount) ??
                        message(errors?.memberSharePercent)) ? (
                          <span className="text-danger text-xs">
                            {message(errors?.memberShareAmount) ??
                              message(errors?.memberSharePercent)}
                          </span>
                        ) : null}
                      </div>
                    </td>
                    <td className="px-2 py-2">
                      <div className="grid gap-1">
                        <Input
                          {...form.register(`items.${index}.validFrom`)}
                          type="date"
                          className="w-36"
                          aria-label={t('priceItems.fields.validFrom')}
                        />
                        <Input
                          {...form.register(`items.${index}.validTo`)}
                          type="date"
                          className="w-36"
                          aria-label={t('priceItems.fields.validTo')}
                        />
                        {(message(errors?.validFrom) ?? message(errors?.validTo)) ? (
                          <span className="text-danger text-xs">
                            {message(errors?.validFrom) ?? message(errors?.validTo)}
                          </span>
                        ) : null}
                      </div>
                    </td>
                    <td className="px-2 py-2">
                      <Input
                        {...form.register(`items.${index}.priority`)}
                        inputMode="numeric"
                        className="w-20 text-right font-mono"
                        aria-label={t('priceItems.fields.priority')}
                      />
                    </td>
                    <td className="px-2 py-2 text-right">
                      <Button variant="ghost" size="sm" onClick={() => rows.remove(index)}>
                        {t('priceItems.remove')}
                      </Button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <div className="flex justify-between gap-2">
        <Button variant="secondary" onClick={() => rows.append(emptyPriceItemRow)}>
          {t('priceItems.add')}
        </Button>
        <Button type="submit" loading={saving}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/** The same sheet for a version nobody may change any more. */
export function PriceItemsTable({
  items,
  labelFor,
}: {
  items: PriceItem[];
  labelFor: (item: PriceItem) => string;
}) {
  const { t } = useTranslation();
  if (items.length === 0) {
    return <p className="text-fg-muted text-sm">{t('priceItems.empty')}</p>;
  }
  return (
    <div className="border-line overflow-x-auto rounded-md border">
      <table className="w-full text-sm" data-testid="price-items-table">
        <thead className="bg-surface-sunken text-fg-muted text-xs">
          <tr>
            <th className="px-3 py-2 text-left font-medium">{t('priceItems.columns.scope')}</th>
            <th className="px-3 py-2 text-left font-medium">{t('priceItems.columns.method')}</th>
            <th className="px-3 py-2 text-right font-medium">{t('priceItems.columns.amount')}</th>
            <th className="px-3 py-2 text-left font-medium">
              {t('priceItems.columns.memberShare')}
            </th>
            <th className="px-3 py-2 text-left font-medium">{t('priceItems.columns.period')}</th>
            <th className="px-3 py-2 text-right font-medium">{t('priceItems.columns.priority')}</th>
          </tr>
        </thead>
        <tbody>
          {items.map((item) => (
            <tr key={item.id} className="border-line border-t">
              <td className="px-3 py-2">{labelFor(item)}</td>
              <td className="px-3 py-2">{t(`priceItems.methods.${item.pricingMethod}`)}</td>
              <td className="px-3 py-2 text-right font-mono">
                {item.pricingMethod === 'PERCENT_OF_LIST'
                  ? `%${item.percent ?? ''}`
                  : (item.amount ?? item.formulaKey ?? t('common.none'))}
              </td>
              <td className="px-3 py-2">
                {item.memberShareMethod === 'NONE'
                  ? t('priceItems.shareMethods.NONE')
                  : item.memberShareMethod === 'PERCENT'
                    ? `%${item.memberSharePercent ?? ''}`
                    : (item.memberShareAmount ?? '')}
              </td>
              <td className="px-3 py-2">
                {item.validFrom}
                {item.validTo ? ` – ${item.validTo}` : ''}
              </td>
              <td className="px-3 py-2 text-right font-mono">{item.priority ?? 100}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
