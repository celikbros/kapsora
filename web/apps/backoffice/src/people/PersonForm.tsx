import type { ApiError, PartyCatalogEntry } from '@kapsora/api-client';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import { Button, FormField, Input, ProblemAlert, Select } from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { useEffect } from 'react';
import { useFieldArray, useForm, type FieldErrors, type Path } from 'react-hook-form';

import {
  IDENTIFIER_TYPES,
  SEXES,
  emptyPersonForm,
  parseIssueMessage,
  personFormSchema,
  type PersonFormValues,
} from './schema';

export interface PersonFormProps {
  onSubmit: (values: PersonFormValues) => Promise<void>;
  onCancel: () => void;
  busy: boolean;
  problem: ApiError['problem'] | null;
  /** Identifier types of the tenant; falls back to the baseline set while loading. */
  identifierTypes?: PartyCatalogEntry[];
}

/**
 * The create form. TCKN and member numbers are checked as the operator types, so a
 * mistyped digit is caught before the request; the server revalidates and its field
 * errors are mapped back onto the same inputs.
 */
export function PersonForm({
  onSubmit,
  onCancel,
  busy,
  problem,
  identifierTypes,
}: PersonFormProps) {
  const { t } = useTranslation();
  const form = useForm<PersonFormValues>({
    resolver: zodResolver(personFormSchema),
    defaultValues: emptyPersonForm,
    mode: 'onBlur',
  });
  const identifiers = useFieldArray({ control: form.control, name: 'identifiers' });

  // Server field errors arrive as identifiers[0].value; map them onto the form paths.
  useEffect(() => {
    if (!problem?.errors) return;
    for (const error of problem.errors) {
      const path = error.field.replace(/\[(\d+)\]/g, '.$1') as Path<PersonFormValues>;
      form.setError(path, {
        type: 'server',
        message: error.message ? `${error.code}|${error.message}` : error.code,
      });
    }
  }, [problem, form]);

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const [codePart, serverMessage] = error.message.split('|');
    const { code, params } = parseIssueMessage(codePart ?? '');
    return fieldErrorMessage(t, code, serverMessage, params);
  }

  const errors: FieldErrors<PersonFormValues> = form.formState.errors;
  const typeOptions = (
    identifierTypes && identifierTypes.length > 0
      ? identifierTypes.filter((entry) => entry.status === 'ACTIVE')
      : IDENTIFIER_TYPES.map((code) => ({ code, displayName: code }))
  ).map((entry) => ({ value: entry.code, label: entry.displayName }));

  return (
    <form onSubmit={form.handleSubmit(onSubmit)} className="grid gap-5" noValidate>
      <ProblemAlert problem={problem} hideFieldErrors />
      <div className="grid gap-4 md:grid-cols-2">
        <FormField
          label={t('people.fields.firstName')}
          required
          requiredLabel={t('common.requiredMark')}
          error={message(errors.firstName)}
        >
          <Input {...form.register('firstName')} autoComplete="given-name" />
        </FormField>
        <FormField label={t('people.fields.middleName')} error={message(errors.middleName)}>
          <Input {...form.register('middleName')} autoComplete="additional-name" />
        </FormField>
        <FormField
          label={t('people.fields.lastName')}
          required
          requiredLabel={t('common.requiredMark')}
          error={message(errors.lastName)}
        >
          <Input {...form.register('lastName')} autoComplete="family-name" />
        </FormField>
        <FormField label={t('people.fields.birthDate')} error={message(errors.birthDate)}>
          <Input {...form.register('birthDate')} type="date" />
        </FormField>
        <FormField label={t('people.fields.sexAtBirth')} error={message(errors.sexAtBirth)}>
          <Select
            {...form.register('sexAtBirth')}
            placeholder={t('common.none')}
            options={SEXES.map((sex) => ({ value: sex, label: t(`people.sex.${sex}`) }))}
          />
        </FormField>
      </div>

      <fieldset className="border-line rounded-md border p-4">
        <legend className="px-1 text-sm font-medium">{t('people.fields.identifiers')}</legend>
        <p className="text-fg-muted mb-3 text-xs">{t('people.identifierHint')}</p>
        <div className="grid gap-3">
          {identifiers.fields.map((field, index) => (
            <div
              key={field.id}
              className="grid gap-2 md:grid-cols-[12rem_1fr_auto_auto] md:items-start"
            >
              <FormField
                label={t('people.fields.identifierType')}
                error={message(errors.identifiers?.[index]?.type)}
              >
                <Select {...form.register(`identifiers.${index}.type`)} options={typeOptions} />
              </FormField>
              <FormField
                label={t('people.fields.identifierValue')}
                required
                requiredLabel={t('common.requiredMark')}
                error={message(errors.identifiers?.[index]?.value)}
              >
                <Input
                  {...form.register(`identifiers.${index}.value`)}
                  inputMode="numeric"
                  autoComplete="off"
                />
              </FormField>
              <label className="flex items-center gap-2 pt-7 text-sm">
                <input type="checkbox" {...form.register(`identifiers.${index}.primary`)} />
                {t('people.fields.primary')}
              </label>
              <Button
                variant="ghost"
                size="sm"
                className="mt-6"
                onClick={() => identifiers.remove(index)}
                disabled={identifiers.fields.length === 1}
              >
                {t('people.fields.removeIdentifier')}
              </Button>
            </div>
          ))}
        </div>
        <Button
          variant="secondary"
          size="sm"
          className="mt-3"
          onClick={() => identifiers.append({ type: 'MEMBER_NO', value: '', primary: false })}
          disabled={identifiers.fields.length >= 10}
        >
          {t('people.fields.addIdentifier')}
        </Button>
      </fieldset>

      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={onCancel} disabled={busy}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" loading={busy}>
          {busy ? t('common.saving') : t('common.save')}
        </Button>
      </div>
    </form>
  );
}
