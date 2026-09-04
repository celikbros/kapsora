import { randomId, type CreateServiceDefinitionRequest, type Problem } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';
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
import { Link, useNavigate } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { useRef, useState } from 'react';
import { useForm } from 'react-hook-form';

import { problemOf } from '../problems';
import { useFieldMessage, useServerFieldErrors } from './forms';
import { useCategoryTree, useCreateDefinition } from './queries';
import {
  FULFILLMENT_MODES,
  SERVICE_UNIT_TYPES,
  definitionFormSchema,
  emptyDefinitionForm,
  type DefinitionFormValues,
} from './schema';

/**
 * A new service definition. The code is typed once and never again: contracts, price
 * items and claims all quote it, so the form says so before the operator commits to one.
 */
export function DefinitionCreatePage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const categories = useCategoryTree();
  const create = useCreateDefinition();
  const [problem, setProblem] = useState<Problem | null>(null);
  // One key per form instance: a retried submit replays instead of creating a twin.
  const idempotencyKey = useRef(randomId());

  const form = useForm<DefinitionFormValues>({
    resolver: zodResolver(definitionFormSchema),
    defaultValues: emptyDefinitionForm,
    mode: 'onBlur',
  });
  useServerFieldErrors(form, problem);
  const message = useFieldMessage();

  async function submit(values: DefinitionFormValues) {
    setProblem(null);
    const body: CreateServiceDefinitionRequest = {
      code: values.code,
      name: values.name,
      categoryId: values.categoryId,
      fulfillmentMode: values.fulfillmentMode,
      defaultUnitType: values.defaultUnitType,
      requiresProvider: values.requiresProvider,
      active: values.active,
      ...(values.description ? { description: values.description } : {}),
    };
    try {
      const created = await create.mutateAsync({ body, idempotencyKey: idempotencyKey.current });
      toast.notify({ tone: 'success', title: t('catalog.created') });
      await navigate({
        to: '/catalog/definitions/$definitionId',
        params: { definitionId: created.data.id },
      });
    } catch (err) {
      setProblem(problemOf(err));
      // Nothing was written, so the next attempt starts a fresh command.
      idempotencyKey.current = randomId();
    }
  }

  const categoryOptions = (categories.data ?? []).map((category) => ({
    value: category.id,
    label: `${category.code} · ${category.name}`,
  }));

  return (
    <>
      <PageHeader
        title={t('catalog.definitions.createTitle')}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('catalog.definitions.title'),
                render: (label) => <Link to="/catalog/definitions">{label}</Link>,
              },
              { label: t('catalog.definitions.createTitle') },
            ]}
          />
        }
      />
      <Card>
        <form
          onSubmit={(event) => void form.handleSubmit(submit)(event)}
          className="grid gap-5"
          noValidate
          data-testid="definition-create-form"
        >
          <ProblemAlert problem={problem} hideFieldErrors />
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('catalog.fields.code')}
              required
              requiredLabel={t('common.requiredMark')}
              hint={t('catalog.codeImmutableHint')}
              error={message(form.formState.errors.code)}
            >
              <Input {...form.register('code')} autoComplete="off" className="font-mono" />
            </FormField>
            <FormField
              label={t('catalog.fields.name')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.name)}
            >
              <Input {...form.register('name')} />
            </FormField>
            <FormField
              label={t('catalog.fields.category')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.categoryId)}
            >
              <Select
                {...form.register('categoryId')}
                placeholder={t('common.none')}
                options={categoryOptions}
              />
            </FormField>
            <FormField
              label={t('catalog.fields.fulfillmentMode')}
              error={message(form.formState.errors.fulfillmentMode)}
            >
              <Select
                {...form.register('fulfillmentMode')}
                options={FULFILLMENT_MODES.map((mode) => ({
                  value: mode,
                  label: t(`catalog.fulfillment.${mode}`),
                }))}
              />
            </FormField>
            <FormField
              label={t('catalog.fields.defaultUnitType')}
              error={message(form.formState.errors.defaultUnitType)}
            >
              <Select
                {...form.register('defaultUnitType')}
                options={SERVICE_UNIT_TYPES.map((unit) => ({
                  value: unit,
                  label: t(`plans.definitions.units.${unit}`),
                }))}
              />
            </FormField>
          </div>
          <FormField
            label={t('catalog.fields.description')}
            error={message(form.formState.errors.description)}
          >
            <Textarea {...form.register('description')} rows={3} />
          </FormField>
          <div className="flex flex-wrap items-center gap-6 text-sm">
            <label className="flex items-center gap-2">
              <input type="checkbox" {...form.register('requiresProvider')} />
              {t('catalog.fields.requiresProvider')}
            </label>
            <label className="flex items-center gap-2">
              <input type="checkbox" {...form.register('active')} />
              {t('catalog.fields.active')}
            </label>
          </div>
          <div className="flex justify-end gap-2">
            <Button
              variant="secondary"
              onClick={() => void navigate({ to: '/catalog/definitions' })}
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
