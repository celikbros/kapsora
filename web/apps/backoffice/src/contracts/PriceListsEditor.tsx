import type { PriceList, PriceListInput } from '@kapsora/api-client';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import { Button, Input, ProblemAlert } from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { useFieldArray, useForm } from 'react-hook-form';

import { parseIssueMessage, type problemOf } from '../problems';
import {
  emptyPriceListRow,
  priceListsFormSchema,
  type PriceListRowValues,
  type PriceListsFormValues,
} from './schema';

const WEEKDAYS = ['MON', 'TUE', 'WED', 'THU', 'FRI', 'SAT', 'SUN'] as const;

export interface PriceListsEditorProps {
  lists: PriceList[];
  onSave: (items: PriceListInput[]) => Promise<void>;
  saving: boolean;
  problem: ReturnType<typeof problemOf> | null;
}

function toRow(list: PriceList): PriceListRowValues {
  const mask = list.weekdayMask;
  return {
    code: list.code,
    name: list.name,
    priority: String(list.priority),
    seasonFrom: list.seasonFrom ?? '',
    seasonTo: list.seasonTo ?? '',
    // Bit 0 is Monday, matching the column the server stores.
    weekdayMask: WEEKDAYS.map((_, i) => (mask == null ? true : (mask & (1 << i)) !== 0)),
  };
}

function toInput(row: PriceListRowValues): PriceListInput {
  const out: PriceListInput = {
    code: row.code,
    name: row.name,
    priority: Number(row.priority),
  };
  if (row.seasonFrom !== '') out.seasonFrom = row.seasonFrom;
  if (row.seasonTo !== '') out.seasonTo = row.seasonTo;
  // Every day selected means no restriction, which the server stores as null rather than
  // a full mask; keeping the two the same would make "all days" two different values.
  if (row.weekdayMask.some((on) => !on)) {
    out.weekdayMask = row.weekdayMask.reduce((mask, on, i) => (on ? mask | (1 << i) : mask), 0);
  }
  return out;
}

/**
 * The price lists of a draft version. A list scopes its prices to a season and to days of
 * the week; a health contract leaves both open and a hotel contract does not.
 */
export function PriceListsEditor({ lists, onSave, saving, problem }: PriceListsEditorProps) {
  const { t } = useTranslation();
  const form = useForm<PriceListsFormValues>({
    resolver: zodResolver(priceListsFormSchema),
    defaultValues: { items: lists.map(toRow) },
    mode: 'onBlur',
  });
  const rows = useFieldArray({ control: form.control, name: 'items' });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function submit(values: PriceListsFormValues) {
    await onSave(values.items.map(toInput));
  }

  return (
    <form
      onSubmit={form.handleSubmit(submit)}
      className="grid gap-4"
      noValidate
      data-testid="price-lists-form"
    >
      <ProblemAlert problem={problem} />

      {rows.fields.length === 0 ? (
        <p className="text-fg-muted text-sm">{t('priceLists.empty')}</p>
      ) : (
        <div className="grid gap-3">
          {rows.fields.map((field, index) => {
            const errors = form.formState.errors.items?.[index];
            return (
              <fieldset key={field.id} className="border-line grid gap-3 rounded-md border p-4">
                <div className="grid gap-3 md:grid-cols-4">
                  <label className="grid gap-1 text-sm">
                    <span className="font-medium">{t('priceLists.fields.code')}</span>
                    <Input {...form.register(`items.${index}.code`)} className="font-mono" />
                    {message(errors?.code) ? (
                      <span className="text-danger text-xs">{message(errors?.code)}</span>
                    ) : null}
                  </label>
                  <label className="grid gap-1 text-sm">
                    <span className="font-medium">{t('priceLists.fields.name')}</span>
                    <Input {...form.register(`items.${index}.name`)} />
                    {message(errors?.name) ? (
                      <span className="text-danger text-xs">{message(errors?.name)}</span>
                    ) : null}
                  </label>
                  <label className="grid gap-1 text-sm">
                    <span className="font-medium">{t('priceLists.fields.seasonFrom')}</span>
                    <Input {...form.register(`items.${index}.seasonFrom`)} type="date" />
                  </label>
                  <label className="grid gap-1 text-sm">
                    <span className="font-medium">{t('priceLists.fields.seasonTo')}</span>
                    <Input {...form.register(`items.${index}.seasonTo`)} type="date" />
                    {message(errors?.seasonTo) ? (
                      <span className="text-danger text-xs">{message(errors?.seasonTo)}</span>
                    ) : null}
                  </label>
                </div>
                <div className="flex flex-wrap items-end gap-4">
                  <fieldset className="grid gap-1 text-sm">
                    <legend className="font-medium">{t('priceLists.fields.weekdays')}</legend>
                    <div className="flex flex-wrap gap-3">
                      {WEEKDAYS.map((day, dayIndex) => (
                        <label key={day} className="flex items-center gap-1 text-xs">
                          <input
                            type="checkbox"
                            {...form.register(`items.${index}.weekdayMask.${dayIndex}`)}
                          />
                          {t(`priceLists.weekdays.${day}`)}
                        </label>
                      ))}
                    </div>
                  </fieldset>
                  <label className="grid gap-1 text-sm">
                    <span className="font-medium">{t('priceLists.fields.priority')}</span>
                    <Input
                      {...form.register(`items.${index}.priority`)}
                      inputMode="numeric"
                      className="w-24 text-right font-mono"
                    />
                  </label>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="ml-auto"
                    onClick={() => rows.remove(index)}
                  >
                    {t('priceLists.remove')}
                  </Button>
                </div>
              </fieldset>
            );
          })}
        </div>
      )}

      <div className="flex justify-between gap-2">
        <Button variant="secondary" onClick={() => rows.append(emptyPriceListRow)}>
          {t('priceLists.add')}
        </Button>
        <Button type="submit" loading={saving}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}
