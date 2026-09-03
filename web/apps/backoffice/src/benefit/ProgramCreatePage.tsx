import { randomId, type CreateProgramRequest } from '@kapsora/api-client';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import {
  Button,
  Card,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import { useOrganizationList } from '../organizations/queries';
import { parseIssueMessage, problemOf } from '../problems';
import { useCreateProgram } from './queries';
import {
  PROGRAM_TYPES,
  emptyProgramForm,
  programFormSchema,
  type ProgramFormValues,
} from './schema';

/**
 * A new program. It starts in DRAFT: nothing is enrolled and nothing is granted until a
 * plan under it has a published version, so creating one is safe and reversible.
 */
export function ProgramCreatePage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const create = useCreateProgram();
  const sponsors = useOrganizationList({ role: 'SPONSOR', limit: 100 });
  const payers = useOrganizationList({ role: 'PAYER', limit: 100 });
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  // One key per attempt at this form, so a retry after a network hiccup cannot double-create.
  const [idempotencyKey, setIdempotencyKey] = useState(randomId);

  const form = useForm<ProgramFormValues>({
    resolver: zodResolver(programFormSchema),
    defaultValues: emptyProgramForm,
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function submit(values: ProgramFormValues) {
    setProblem(null);
    const body: CreateProgramRequest = {
      code: values.code,
      name: values.name,
      programType: values.programType,
      sponsorOrganizationId: values.sponsorOrganizationId,
      payerOrganizationId: values.payerOrganizationId,
      ...(values.validFrom !== '' ? { validFrom: values.validFrom } : {}),
      ...(values.validTo !== '' ? { validTo: values.validTo } : {}),
    };
    try {
      const created = await create.mutateAsync({ body, idempotencyKey });
      toast.notify({ tone: 'success', title: t('programs.created') });
      await navigate({ to: '/programs/$programId', params: { programId: created.data.id } });
    } catch (err) {
      setProblem(problemOf(err));
      setIdempotencyKey(randomId());
    }
  }

  return (
    <>
      <PageHeader title={t('programs.createTitle')} />
      <Card>
        <form onSubmit={form.handleSubmit(submit)} className="grid gap-4" noValidate>
          <ProblemAlert problem={problem} />
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('programs.fields.code')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.code)}
            >
              <Input {...form.register('code')} autoComplete="off" className="font-mono" />
            </FormField>
            <FormField
              label={t('programs.fields.name')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.name)}
            >
              <Input {...form.register('name')} />
            </FormField>
            <FormField
              label={t('programs.fields.programType')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.programType)}
            >
              <Select
                {...form.register('programType')}
                options={PROGRAM_TYPES.map((type) => ({
                  value: type,
                  label: t(`programs.types.${type}`),
                }))}
              />
            </FormField>
            <FormField
              label={t('programs.fields.sponsorOrganization')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.sponsorOrganizationId)}
            >
              <Select
                {...form.register('sponsorOrganizationId')}
                placeholder={t('common.none')}
                options={(sponsors.data?.items ?? []).map((organization) => ({
                  value: organization.id,
                  label: organization.displayName,
                }))}
              />
            </FormField>
            <FormField
              label={t('programs.fields.payerOrganization')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.payerOrganizationId)}
            >
              <Select
                {...form.register('payerOrganizationId')}
                placeholder={t('common.none')}
                options={(payers.data?.items ?? []).map((organization) => ({
                  value: organization.id,
                  label: organization.displayName,
                }))}
              />
            </FormField>
            <FormField
              label={t('programs.fields.validFrom')}
              error={message(form.formState.errors.validFrom)}
            >
              <Input {...form.register('validFrom')} type="date" />
            </FormField>
            <FormField
              label={t('programs.fields.validTo')}
              error={message(form.formState.errors.validTo)}
            >
              <Input {...form.register('validTo')} type="date" />
            </FormField>
          </div>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => void navigate({ to: '/programs' })}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={create.isPending}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Card>
    </>
  );
}
