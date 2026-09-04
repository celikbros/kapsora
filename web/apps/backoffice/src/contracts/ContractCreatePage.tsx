import { randomId, type CreateContractRequest } from '@kapsora/api-client';
import { useTenantId } from '@kapsora/auth';
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
import { useQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import { useOps } from '../api';
import { useOrganizationList } from '../organizations/queries';
import { parseIssueMessage, problemOf } from '../problems';
import { useCreateContract } from './queries';
import {
  SERVICE_DOMAINS,
  contractFormSchema,
  emptyContractForm,
  type ContractFormValues,
} from './schema';

/** A new contract. It opens in DRAFT: nothing is priced until a version is published. */
export function ContractCreatePage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const tenantId = useTenantId();
  const ops = useOps();
  const create = useCreateContract();
  const payers = useOrganizationList({ role: 'PAYER', limit: 100 });
  const sponsors = useOrganizationList({ role: 'SPONSOR', limit: 100 });
  const providers = useQuery({
    queryKey: ['contracts', tenantId, 'providerOptions'],
    queryFn: async () => {
      const page = await ops.providers.list(tenantId, { status: 'ACTIVE', limit: 100 });
      return page.items.map((provider) => ({
        value: provider.id,
        label: provider.organizationName,
      }));
    },
  });
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [idempotencyKey, setIdempotencyKey] = useState(randomId);

  const form = useForm<ContractFormValues>({
    resolver: zodResolver(contractFormSchema),
    defaultValues: emptyContractForm,
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function submit(values: ContractFormValues) {
    setProblem(null);
    const body: CreateContractRequest = {
      code: values.code,
      name: values.name,
      payerOrganizationId: values.payerOrganizationId,
      providerProfileId: values.providerProfileId,
      domainCode: values.domainCode,
      ...(values.sponsorOrganizationId !== ''
        ? { sponsorOrganizationId: values.sponsorOrganizationId }
        : {}),
    };
    try {
      const created = await create.mutateAsync({ body, idempotencyKey });
      toast.notify({ tone: 'success', title: t('contracts.created') });
      await navigate({ to: '/contracts/$contractId', params: { contractId: created.data.id } });
    } catch (err) {
      setProblem(problemOf(err));
      setIdempotencyKey(randomId());
    }
  }

  return (
    <>
      <PageHeader title={t('contracts.createTitle')} />
      <Card>
        <form onSubmit={form.handleSubmit(submit)} className="grid gap-4" noValidate>
          <ProblemAlert problem={problem} />
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('contracts.fields.code')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.code)}
            >
              <Input {...form.register('code')} autoComplete="off" className="font-mono" />
            </FormField>
            <FormField
              label={t('contracts.fields.name')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.name)}
            >
              <Input {...form.register('name')} />
            </FormField>
            <FormField
              label={t('contracts.fields.provider')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.providerProfileId)}
            >
              <Select
                {...form.register('providerProfileId')}
                placeholder={t('common.none')}
                options={providers.data ?? []}
              />
            </FormField>
            <FormField
              label={t('contracts.fields.payer')}
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
            <FormField label={t('contracts.fields.sponsor')}>
              <Select
                {...form.register('sponsorOrganizationId')}
                placeholder={t('common.none')}
                options={(sponsors.data?.items ?? []).map((organization) => ({
                  value: organization.id,
                  label: organization.displayName,
                }))}
              />
            </FormField>
            <FormField label={t('contracts.fields.domain')}>
              <Select
                {...form.register('domainCode')}
                options={SERVICE_DOMAINS.map((domain) => ({
                  value: domain,
                  label: t(`catalog.domains.${domain}`),
                }))}
              />
            </FormField>
          </div>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => void navigate({ to: '/contracts' })}>
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
