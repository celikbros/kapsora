import { randomId, type CreateServiceCategoryRequest, type Problem } from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Dialog,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  useToast,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import type { ServiceCategory } from '@kapsora/api-client';
import { useEffect, useRef, useState } from 'react';
import { useForm } from 'react-hook-form';

import { problemOf } from '../problems';
import { useFieldMessage, useServerFieldErrors } from './forms';
import {
  useCategoryTree,
  useCreateCategory,
  useServiceCategory,
  useUpdateCategory,
} from './queries';
import {
  SERVICE_DOMAINS,
  categoryEditSchema,
  categoryFormSchema,
  emptyCategoryForm,
  type CategoryEditValues,
  type CategoryFormValues,
} from './schema';

/** A category with the depth it sits at, ready to render in reading order. */
interface TreeRow {
  category: ServiceCategory;
  depth: number;
}

/** The tree is six deep by contract, so the indents are a fixed, token-sized ladder. */
const INDENTS = ['pl-0', 'pl-4', 'pl-8', 'pl-12', 'pl-16', 'pl-20'];

/**
 * Flattens the tree into reading order: a parent, then everything under it. Categories
 * whose parent is missing from the page are treated as roots so nothing disappears.
 */
export function flattenTree(categories: ServiceCategory[]): TreeRow[] {
  const byParent = new Map<string, ServiceCategory[]>();
  const ids = new Set(categories.map((c) => c.id));
  for (const category of categories) {
    const parent = category.parentId && ids.has(category.parentId) ? category.parentId : '';
    const siblings = byParent.get(parent) ?? [];
    siblings.push(category);
    byParent.set(parent, siblings);
  }
  for (const siblings of byParent.values()) {
    siblings.sort((a, b) => a.code.localeCompare(b.code, 'tr'));
  }

  const rows: TreeRow[] = [];
  const walk = (parentId: string, depth: number): void => {
    for (const category of byParent.get(parentId) ?? []) {
      rows.push({ category, depth });
      walk(category.id, depth + 1);
    }
  };
  walk('', 0);
  return rows;
}

/**
 * The service categories as a tree. Re-parenting is a picker rather than a drag: six
 * levels of a list nobody reorders daily do not earn a drag surface, and a picker is
 * reachable from the keyboard. The server owns the two rules that matter — a category may
 * not move under its own descendant, and the tree may not grow past six levels — so the
 * picker offers every other category and shows what the server refuses.
 */
export function CategoryTreePage() {
  const { t } = useTranslation();
  const canManage = usePermission('catalog.manage');
  const tree = useCategoryTree();
  const [creating, setCreating] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);

  const categories = tree.data ?? [];
  const rows = flattenTree(categories);

  return (
    <>
      <PageHeader
        title={t('catalog.categories.title')}
        description={t('catalog.intro')}
        actions={
          canManage ? (
            <Button onClick={() => setCreating(true)}>{t('catalog.categories.new')}</Button>
          ) : null
        }
      />

      <ProblemAlert
        page
        problem={tree.error ? problemOf(tree.error) : null}
        className="mb-4"
        actions={
          <Button size="sm" variant="secondary" onClick={() => void tree.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />

      {tree.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <EmptyState title={t('catalog.categories.empty')} />
      ) : (
        <Table aria-busy={tree.isFetching || undefined} data-testid="category-table">
          <THead>
            <TR>
              <TH>{t('catalog.fields.code')}</TH>
              <TH>{t('catalog.fields.name')}</TH>
              <TH>{t('catalog.fields.domain')}</TH>
              <TH>{t('catalog.fields.active')}</TH>
              {canManage ? <TH>{t('common.actions')}</TH> : null}
            </TR>
          </THead>
          <TBody>
            {rows.map(({ category, depth }) => (
              <TR key={category.id}>
                <TD>
                  <code className={`font-mono text-xs ${INDENTS[Math.min(depth, 5)] ?? 'pl-0'}`}>
                    {category.code}
                  </code>
                </TD>
                <TD>{category.name}</TD>
                <TD>{t(`catalog.domains.${category.domain}`)}</TD>
                <TD>
                  <Badge tone={category.active ? 'success' : 'neutral'}>
                    {category.active ? t('catalog.active') : t('catalog.inactive')}
                  </Badge>
                </TD>
                {canManage ? (
                  <TD>
                    <Button size="sm" variant="ghost" onClick={() => setEditingId(category.id)}>
                      {t('organizations.edit')}
                    </Button>
                  </TD>
                ) : null}
              </TR>
            ))}
          </TBody>
        </Table>
      )}

      {canManage && creating ? (
        <CategoryCreateDialog categories={categories} onClose={() => setCreating(false)} />
      ) : null}
      {canManage && editingId ? (
        <CategoryEditDialog
          categoryId={editingId}
          categories={categories}
          onClose={() => setEditingId(null)}
        />
      ) : null}
    </>
  );
}

/** Parent options: every category except the one being edited. */
function parentOptions(categories: ServiceCategory[], selfId: string | null) {
  return categories
    .filter((category) => category.id !== selfId)
    .map((category) => ({ value: category.id, label: `${category.code} · ${category.name}` }));
}

/** Mounted only while it is open, so every dialog starts on a clean form. */
function CategoryCreateDialog({
  categories,
  onClose,
}: {
  categories: ServiceCategory[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const create = useCreateCategory();
  const [problem, setProblem] = useState<Problem | null>(null);
  // One key per dialog instance: a retried submit replays instead of creating a twin.
  const idempotencyKey = useRef(randomId());

  const form = useForm<CategoryFormValues>({
    resolver: zodResolver(categoryFormSchema),
    defaultValues: emptyCategoryForm,
    mode: 'onBlur',
  });
  useServerFieldErrors(form, problem);
  const message = useFieldMessage();

  async function submit(values: CategoryFormValues) {
    setProblem(null);
    const body: CreateServiceCategoryRequest = {
      code: values.code,
      name: values.name,
      domain: values.domain,
      active: values.active,
      ...(values.parentId ? { parentId: values.parentId } : {}),
    };
    try {
      await create.mutateAsync({ body, idempotencyKey: idempotencyKey.current });
      toast.notify({ tone: 'success', title: t('catalog.categoryCreated') });
      onClose();
    } catch (err) {
      setProblem(problemOf(err));
      // Nothing was written, so the next attempt starts a fresh command.
      idempotencyKey.current = randomId();
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      title={t('catalog.categories.createTitle')}
    >
      <form
        onSubmit={(event) => void form.handleSubmit(submit)(event)}
        className="grid gap-4"
        noValidate
        data-testid="category-create-form"
      >
        <ProblemAlert problem={problem} hideFieldErrors />
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
        <FormField label={t('catalog.fields.domain')}>
          <Select
            {...form.register('domain')}
            options={SERVICE_DOMAINS.map((domain) => ({
              value: domain,
              label: t(`catalog.domains.${domain}`),
            }))}
          />
        </FormField>
        <FormField
          label={t('catalog.fields.parent')}
          error={message(form.formState.errors.parentId)}
        >
          <Select
            {...form.register('parentId')}
            placeholder={t('catalog.categories.root')}
            options={parentOptions(categories, null)}
          />
        </FormField>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" {...form.register('active')} />
          {t('catalog.fields.active')}
        </label>
        <div className="flex justify-end gap-2">
          <Button variant="secondary" onClick={onClose} disabled={create.isPending}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" loading={create.isPending}>
            {t('common.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function CategoryEditDialog({
  categoryId,
  categories,
  onClose,
}: {
  categoryId: string;
  categories: ServiceCategory[];
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const toast = useToast();
  const query = useServiceCategory(categoryId);
  const update = useUpdateCategory(categoryId);
  const [problem, setProblem] = useState<Problem | null>(null);

  const form = useForm<CategoryEditValues>({
    resolver: zodResolver(categoryEditSchema),
    defaultValues: { name: '', parentId: '', active: true },
    mode: 'onBlur',
  });
  useServerFieldErrors(form, problem);
  const message = useFieldMessage();

  const category = query.data?.data;
  useEffect(() => {
    if (!category) return;
    form.reset({
      name: category.name,
      parentId: category.parentId ?? '',
      active: category.active,
    });
  }, [category, form]);

  async function submit(values: CategoryEditValues) {
    if (!query.data || !category) return;
    setProblem(null);
    const parentId = values.parentId === '' ? null : values.parentId;
    const patch = {
      ...(values.name !== category.name ? { name: values.name } : {}),
      ...(parentId !== (category.parentId ?? null) ? { parentId } : {}),
      ...(values.active !== category.active ? { active: values.active } : {}),
    };
    if (Object.keys(patch).length === 0) {
      onClose();
      return;
    }
    try {
      await update.mutateAsync({ etag: query.data.etag, patch });
      toast.notify({ tone: 'success', title: t('catalog.categoryUpdated') });
      onClose();
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(next) => (next ? undefined : onClose())}
      title={t('catalog.categories.editTitle')}
    >
      {query.isPending || !category ? (
        <div className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : (
        <form
          onSubmit={form.handleSubmit(submit)}
          className="grid gap-4"
          noValidate
          data-testid="category-edit-form"
        >
          <ProblemAlert problem={problem} hideFieldErrors />
          <FormField label={t('catalog.fields.code')} hint={t('catalog.codeImmutableHint')}>
            <Input value={category.code} readOnly className="font-mono" />
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
            label={t('catalog.fields.parent')}
            error={message(form.formState.errors.parentId)}
          >
            <Select
              {...form.register('parentId')}
              placeholder={t('catalog.categories.root')}
              options={parentOptions(categories, category.id)}
            />
          </FormField>
          <label className="flex items-center gap-2 text-sm">
            <input type="checkbox" {...form.register('active')} />
            {t('catalog.fields.active')}
          </label>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={onClose} disabled={update.isPending}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={update.isPending}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  );
}
