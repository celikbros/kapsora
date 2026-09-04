import { ApiError, randomId, type CreateProviderRequest } from '@kapsora/api-client';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import {
  Breadcrumb,
  Button,
  Card,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  Textarea,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import { useOrganizationList } from '../organizations/queries';
import { problemOf } from '../problems';
import { useCreateProvider } from './queries';
import { ProviderLink, useProviderNavigate } from './routes';
import {
  PROVIDER_TYPES,
  emptyProviderForm,
  parseIssueMessage,
  providerFormSchema,
  type ProviderFormValues,
} from './schema';

/**
 * A provider profile hangs off an existing PROVIDER relationship, so the form picks one
 * rather than creating an organization. A new profile always starts PENDING; the operator
 * activates it from the detail screen once the paperwork is in.
 */
export function ProviderCreatePage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useProviderNavigate();
  const organizations = useOrganizationList({ role: 'PROVIDER', limit: 200 });
  const create = useCreateProvider();
  // One key per attempt: a retried submit replays instead of creating a twin.
  const [idempotencyKey, setIdempotencyKey] = useState(randomId);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);

  const form = useForm<ProviderFormValues>({
    resolver: zodResolver(providerFormSchema),
    defaultValues: emptyProviderForm,
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function submit(values: ProviderFormValues) {
    setProblem(null);
    const body: CreateProviderRequest = {
      tenantOrganizationId: values.tenantOrganizationId,
      providerType: values.providerType,
      ...(values.networkTier ? { networkTier: values.networkTier } : {}),
      ...(values.contractedFrom ? { contractedFrom: values.contractedFrom } : {}),
      ...(values.contractedTo ? { contractedTo: values.contractedTo } : {}),
      ...(values.notes ? { notes: values.notes } : {}),
    };
    try {
      const result = await create.mutateAsync({ body, idempotencyKey });
      toast.notify({ tone: 'success', title: t('providers.created') });
      await navigate({
        to: '/providers/$providerId',
        params: { providerId: result.data.id },
      });
    } catch (err) {
      setProblem(problemOf(err));
      if (err instanceof ApiError && err.status === 422) {
        // Nothing was written, so the next attempt starts a fresh command.
        setIdempotencyKey(randomId());
      }
    }
  }

  return (
    <>
      <PageHeader
        title={t('providers.createTitle')}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('providers.title'),
                render: (label) => (
                  <ProviderLink target={{ to: '/providers', search: {} }}>{label}</ProviderLink>
                ),
              },
              { label: t('providers.createTitle') },
            ]}
          />
        }
      />
      <Card>
        <form onSubmit={form.handleSubmit(submit)} className="grid gap-4" noValidate>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('providers.fields.organization')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.tenantOrganizationId)}
            >
              <Select
                {...form.register('tenantOrganizationId')}
                placeholder={t('common.none')}
                options={(organizations.data?.items ?? []).map((organization) => ({
                  value: organization.id,
                  label: organization.displayName,
                }))}
              />
            </FormField>
            <FormField
              label={t('providers.fields.providerType')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.providerType)}
            >
              <Select
                {...form.register('providerType')}
                options={PROVIDER_TYPES.map((type) => ({
                  value: type,
                  label: t(`providers.types.${type}`),
                }))}
              />
            </FormField>
            <FormField
              label={t('providers.fields.networkTier')}
              error={message(form.formState.errors.networkTier)}
            >
              <Input {...form.register('networkTier')} autoComplete="off" className="font-mono" />
            </FormField>
            <div />
            <FormField
              label={t('providers.fields.contractedFrom')}
              error={message(form.formState.errors.contractedFrom)}
            >
              <Input {...form.register('contractedFrom')} type="date" />
            </FormField>
            <FormField
              label={t('providers.fields.contractedTo')}
              error={message(form.formState.errors.contractedTo)}
            >
              <Input {...form.register('contractedTo')} type="date" />
            </FormField>
          </div>
          <FormField
            label={t('providers.fields.notes')}
            error={message(form.formState.errors.notes)}
          >
            <Textarea {...form.register('notes')} rows={3} />
          </FormField>

          <ProblemAlert problem={problem} />

          <div className="flex justify-end gap-2">
            <Button
              variant="secondary"
              onClick={() => void navigate({ to: '/providers', search: {} })}
              disabled={create.isPending}
            >
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
