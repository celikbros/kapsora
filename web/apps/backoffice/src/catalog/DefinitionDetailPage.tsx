import type {
  Problem,
  ServiceCodeMappingInput,
  ServiceDefinition,
  UpdateServiceDefinitionRequest,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
  FormField,
  HelpHint,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  Tabs,
  Textarea,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { Link, useParams } from '@tanstack/react-router';
import { useEffect, useState } from 'react';
import { useForm } from 'react-hook-form';

import { problemOf } from '../problems';
import { CodeMappingsEditor, CodeMappingsTable } from './CodeMappingsEditor';
import { useFieldMessage, useServerFieldErrors } from './forms';
import {
  useCategoryTree,
  useCodeMappings,
  useReplaceCodeMappings,
  useServiceDefinition,
  useUpdateDefinition,
} from './queries';
import {
  FULFILLMENT_MODES,
  SERVICE_UNIT_TYPES,
  definitionEditSchema,
  type DefinitionEditValues,
} from './schema';

/**
 * One service definition: what it is, and the external codes it is reported under. The
 * two are separate concerns saved separately — the fields are a merge-patch under
 * If-Match, the mappings are a set replaced whole — so they sit on their own tabs.
 */
export function DefinitionDetailPage() {
  const { t } = useTranslation();
  const params: Record<string, string | undefined> = useParams({ strict: false });
  const definitionId = params['definitionId'] ?? '';
  const canManage = usePermission('catalog.manage');
  const query = useServiceDefinition(definitionId);
  const [tab, setTab] = useState('fields');

  if (query.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (query.error || !query.data) {
    return (
      <ProblemAlert
        page
        problem={problemOf(query.error)}
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />
    );
  }

  const definition = query.data.data;
  const etag = query.data.etag;

  return (
    <>
      <PageHeader
        title={definition.name}
        description={definition.description ?? undefined}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('catalog.definitions.title'),
                render: (label) => <Link to="/catalog/definitions">{label}</Link>,
              },
              { label: definition.code },
            ]}
          />
        }
        actions={
          <Badge tone={definition.active ? 'success' : 'neutral'}>
            {definition.active ? t('catalog.active') : t('catalog.inactive')}
          </Badge>
        }
      />

      <Tabs
        ariaLabel={t('catalog.definitions.detailTitle')}
        value={tab}
        onValueChange={setTab}
        tabs={[
          {
            value: 'fields',
            label: t('catalog.definitions.detailTitle'),
            content: canManage ? (
              <DefinitionFieldsForm definition={definition} etag={etag} />
            ) : (
              <DefinitionFieldsCard definition={definition} />
            ),
          },
          {
            value: 'mappings',
            label: t('catalog.mappings.title'),
            content: <MappingsTab definitionId={definitionId} etag={etag} canManage={canManage} />,
          },
        ]}
      />
    </>
  );
}

function DefinitionFieldsForm({
  definition,
  etag,
}: {
  definition: ServiceDefinition;
  etag: string;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const update = useUpdateDefinition(definition.id);
  const categories = useCategoryTree();
  const [problem, setProblem] = useState<Problem | null>(null);

  const defaults: DefinitionEditValues = {
    code: definition.code,
    name: definition.name,
    categoryId: definition.categoryId,
    fulfillmentMode: definition.fulfillmentMode,
    defaultUnitType: definition.defaultUnitType,
    description: definition.description ?? '',
    requiresProvider: definition.requiresProvider,
    active: definition.active,
  };

  const form = useForm<DefinitionEditValues>({
    resolver: zodResolver(definitionEditSchema),
    defaultValues: defaults,
    mode: 'onBlur',
  });
  useServerFieldErrors(form, problem);
  const message = useFieldMessage();

  useEffect(() => {
    form.reset({
      code: definition.code,
      name: definition.name,
      categoryId: definition.categoryId,
      fulfillmentMode: definition.fulfillmentMode,
      defaultUnitType: definition.defaultUnitType,
      description: definition.description ?? '',
      requiresProvider: definition.requiresProvider,
      active: definition.active,
    });
  }, [definition, form]);

  async function submit(values: DefinitionEditValues) {
    setProblem(null);
    // The code is never sent: it is what every contract and claim quotes.
    const description = values.description === '' ? null : values.description;
    const patch: UpdateServiceDefinitionRequest = {
      ...(values.name !== definition.name ? { name: values.name } : {}),
      ...(values.categoryId !== definition.categoryId ? { categoryId: values.categoryId } : {}),
      ...(values.fulfillmentMode !== definition.fulfillmentMode
        ? { fulfillmentMode: values.fulfillmentMode }
        : {}),
      ...(values.defaultUnitType !== definition.defaultUnitType
        ? { defaultUnitType: values.defaultUnitType }
        : {}),
      ...(description !== (definition.description ?? null) ? { description } : {}),
      ...(values.requiresProvider !== definition.requiresProvider
        ? { requiresProvider: values.requiresProvider }
        : {}),
      ...(values.active !== definition.active ? { active: values.active } : {}),
    };
    if (Object.keys(patch).length === 0) return;
    try {
      await update.mutateAsync({ etag, patch });
      toast.notify({ tone: 'success', title: t('catalog.updated') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  const categoryOptions = (categories.data ?? []).map((category) => ({
    value: category.id,
    label: `${category.code} · ${category.name}`,
  }));

  return (
    <Card>
      <form
        onSubmit={form.handleSubmit(submit)}
        className="grid gap-5"
        noValidate
        data-testid="definition-form"
      >
        <ProblemAlert problem={problem} hideFieldErrors />
        <div className="grid gap-4 md:grid-cols-2">
          <FormField
            label={t('catalog.fields.code')}
            hint={t('catalog.codeImmutableHint')}
            error={message(form.formState.errors.code)}
          >
            <Input {...form.register('code')} readOnly className="font-mono" />
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
            <Select {...form.register('categoryId')} options={categoryOptions} />
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
        <div className="flex justify-end">
          <Button type="submit" loading={update.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** What the definition holds, for an operator who may read the catalog but not change it. */
function DefinitionFieldsCard({ definition }: { definition: ServiceDefinition }) {
  const { t } = useTranslation();
  const row = (label: string, value: string) => (
    <>
      <dt className="text-fg-muted">{label}</dt>
      <dd>{value === '' ? t('common.none') : value}</dd>
    </>
  );
  return (
    <Card>
      <dl className="grid grid-cols-[max-content_minmax(0,1fr)] [&>dd]:min-w-0 [&>dd]:break-words gap-x-6 gap-y-2 text-sm">
        <dt className="text-fg-muted">{t('catalog.fields.code')}</dt>
        <dd className="font-mono">{definition.code}</dd>
        {row(t('catalog.fields.name'), definition.name)}
        {row(t('catalog.fields.category'), definition.categoryCode)}
        {row(t('catalog.fields.domain'), t(`catalog.domains.${definition.domain}`))}
        {row(
          t('catalog.fields.fulfillmentMode'),
          t(`catalog.fulfillment.${definition.fulfillmentMode}`),
        )}
        {row(
          t('catalog.fields.defaultUnitType'),
          t(`plans.definitions.units.${definition.defaultUnitType}`),
        )}
        {row(t('catalog.fields.description'), definition.description ?? '')}
        {row(
          t('catalog.fields.requiresProvider'),
          definition.requiresProvider ? t('common.yes') : t('common.no'),
        )}
      </dl>
    </Card>
  );
}

function MappingsTab({
  definitionId,
  etag,
  canManage,
}: {
  definitionId: string;
  etag: string;
  canManage: boolean;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const mappings = useCodeMappings(definitionId);
  const replace = useReplaceCodeMappings(definitionId);
  const [problem, setProblem] = useState<Problem | null>(null);

  async function save(items: ServiceCodeMappingInput[]) {
    setProblem(null);
    try {
      await replace.mutateAsync({ etag, items });
      toast.notify({ tone: 'success', title: t('catalog.mappings.saved') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  if (mappings.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (mappings.error || !mappings.data) {
    return (
      <ProblemAlert
        page
        problem={problemOf(mappings.error)}
        actions={
          <Button size="sm" variant="secondary" onClick={() => void mappings.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />
    );
  }

  return (
    <Card>
      {/* The mark sits beside the heading, not inside it: it is not part of the name. */}
      <div className="mb-4 flex items-baseline gap-1.5">
        <h2 className="text-base font-semibold">{t('catalog.mappings.title')}</h2>
        <HelpHint term="kodSistemi" />
      </div>
      {canManage ? (
        <CodeMappingsEditor
          // Remounts on a new version so the rows come from the set the server now holds.
          key={etag}
          mappings={mappings.data}
          onSave={save}
          saving={replace.isPending}
          problem={problem}
        />
      ) : (
        <CodeMappingsTable mappings={mappings.data} />
      )}
    </Card>
  );
}
