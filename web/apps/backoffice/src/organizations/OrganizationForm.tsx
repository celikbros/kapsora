import type { ApiError } from '@kapsora/api-client';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import { Button, FormField, Input, ProblemAlert, Select } from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { useEffect } from 'react';
import { useFieldArray, useForm, type FieldErrors, type Path } from 'react-hook-form';
import {
  IDENTIFIER_TYPES,
  ORGANIZATION_KINDS,
  RELATIONSHIP_ROLES,
  emptyOrganizationForm,
  organizationFormSchema,
  parseIssueMessage,
  type OrganizationFormValues,
} from './schema';

export interface OrganizationFormProps {
  onSubmit: (values: OrganizationFormValues) => Promise<void>;
  onCancel: () => void;
  busy: boolean;
  /** Server problem (422 field errors are mapped onto the fields). */
  problem: ApiError['problem'] | null;
}

/** Create form with instant VKN/TCKN checksum feedback (server stays the authority). */
export function OrganizationForm({ onSubmit, onCancel, busy, problem }: OrganizationFormProps) {
  const { t } = useTranslation();
  const form = useForm<OrganizationFormValues>({
    resolver: zodResolver(organizationFormSchema),
    defaultValues: emptyOrganizationForm,
    mode: 'onBlur',
  });
  const identifiers = useFieldArray({ control: form.control, name: 'identifiers' });

  // Map server field errors ("identifiers[0].value") onto the form paths.
  useEffect(() => {
    if (!problem?.errors) return;
    for (const e of problem.errors) {
      const path = e.field.replace(/\[(\d+)\]/g, '.$1') as Path<OrganizationFormValues>;
      form.setError(path, {
        type: 'server',
        message: e.message ? `${e.code}|${e.message}` : e.code,
      });
    }
  }, [problem, form]);

  function message(err: { message?: string } | undefined): string | undefined {
    if (!err?.message) return undefined;
    const [codePart, serverMessage] = err.message.split('|');
    const { code, params } = parseIssueMessage(codePart ?? '');
    return fieldErrorMessage(t, code, serverMessage, params);
  }

  const errors: FieldErrors<OrganizationFormValues> = form.formState.errors;
  const identifiersError =
    errors.identifiers?.root?.message ??
    (errors.identifiers as { message?: string } | undefined)?.message;

  return (
    <form onSubmit={form.handleSubmit(onSubmit)} className="grid gap-5" noValidate>
      <ProblemAlert problem={problem} hideFieldErrors />
      <div className="grid gap-4 md:grid-cols-2">
        <FormField
          label={t('organizations.fields.legalName')}
          required
          requiredLabel={t('common.requiredMark')}
          error={message(errors.legalName)}
        >
          <Input {...form.register('legalName')} autoComplete="organization" />
        </FormField>
        <FormField
          label={t('organizations.fields.displayName')}
          required
          requiredLabel={t('common.requiredMark')}
          error={message(errors.displayName)}
        >
          <Input {...form.register('displayName')} />
        </FormField>
        <FormField
          label={t('organizations.fields.organizationKind')}
          required
          requiredLabel={t('common.requiredMark')}
          error={message(errors.organizationKind)}
        >
          <Select
            {...form.register('organizationKind')}
            options={ORGANIZATION_KINDS.map((k) => ({
              value: k,
              label: t(`organizations.kinds.${k}`),
            }))}
          />
        </FormField>
        <FormField
          label={t('organizations.fields.relationshipRole')}
          required
          requiredLabel={t('common.requiredMark')}
          error={message(errors.relationshipRole)}
        >
          <Select
            {...form.register('relationshipRole')}
            options={RELATIONSHIP_ROLES.map((r) => ({
              value: r,
              label: t(`organizations.roles.${r}`),
            }))}
          />
        </FormField>
        <FormField
          label={t('organizations.fields.countryCode')}
          required
          requiredLabel={t('common.requiredMark')}
          error={message(errors.countryCode)}
        >
          <Input {...form.register('countryCode')} maxLength={2} className="uppercase" />
        </FormField>
        <FormField
          label={t('organizations.fields.tenantCode')}
          hint={t('organizations.fields.tenantCodeHint')}
          error={message(errors.tenantCode)}
        >
          <Input {...form.register('tenantCode')} />
        </FormField>
      </div>

      <fieldset className="border-line rounded-md border p-4">
        <legend className="px-1 text-sm font-medium">
          {t('organizations.fields.identifiers')}
        </legend>
        <p className="text-fg-muted mb-3 text-xs">{t('organizations.identifierHint')}</p>
        {identifiersError ? (
          <p role="alert" className="text-danger mb-3 text-xs">
            {message({ message: identifiersError })}
          </p>
        ) : null}
        <div className="grid gap-3">
          {identifiers.fields.map((field, index) => (
            <div
              key={field.id}
              className="grid gap-2 md:grid-cols-[10rem_1fr_auto_auto] md:items-start"
            >
              <FormField
                label={t('organizations.fields.identifierType')}
                error={message(errors.identifiers?.[index]?.type)}
              >
                <Select
                  {...form.register(`identifiers.${index}.type`)}
                  options={IDENTIFIER_TYPES.map((k) => ({
                    value: k,
                    label: t(`organizations.identifierTypes.${k}`),
                  }))}
                />
              </FormField>
              <FormField
                label={t('organizations.fields.identifierValue')}
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
                {t('organizations.fields.primary')}
              </label>
              <Button
                variant="ghost"
                size="sm"
                className="mt-6"
                onClick={() => identifiers.remove(index)}
                disabled={identifiers.fields.length === 1}
              >
                {t('organizations.fields.removeIdentifier')}
              </Button>
            </div>
          ))}
        </div>
        <Button
          variant="secondary"
          size="sm"
          className="mt-3"
          onClick={() => identifiers.append({ type: 'MERSIS', value: '', primary: false })}
          disabled={identifiers.fields.length >= 10}
        >
          {t('organizations.fields.addIdentifier')}
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
