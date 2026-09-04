import {
  randomId,
  type CreateRuleSetRequest,
  type RuleSet,
  type RuleSetPurpose,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
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
  statusTone,
  useToast,
} from '@kapsora/ui';
import { Link, useNavigate } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { useId, useState, type FormEvent } from 'react';
import { useForm } from 'react-hook-form';

import { parseIssueMessage, problemOf } from '../problems';
import { useCreateRuleSet, useRuleSets } from './queries';
import { useRuleSetListSearch, type RuleSetListSearch } from './routing';
import {
  RULE_SET_PURPOSES,
  SERVICE_DOMAINS,
  emptyRuleSetForm,
  ruleSetFormSchema,
  type RuleSetFormValues,
} from './schema';

export type { RuleSetListSearch } from './routing';

const PAGE_SIZE = 50;

/**
 * Rule sets: one per decision a caller asks about. The purpose is what makes a set
 * findable — a caller asking which documents a claim needs reads the DOCUMENT sets and
 * never a PRICE one — so it is the filter that leads.
 */
export function RuleSetListPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const search = useRuleSetListSearch();
  const canDraft = usePermission('rule.draft');
  const [q, setQ] = useState(search.q ?? '');
  const [trail, setTrail] = useState<string[]>([]);
  const [creating, setCreating] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [createKey, setCreateKey] = useState(randomId);
  const pagingLabelId = useId();

  const query = useRuleSets({
    ...(search.q ? { q: search.q } : {}),
    ...(search.purpose ? { purpose: search.purpose } : {}),
    ...(search.cursor ? { cursor: search.cursor } : {}),
    limit: PAGE_SIZE,
  });
  const create = useCreateRuleSet();

  const form = useForm<RuleSetFormValues>({
    resolver: zodResolver(ruleSetFormSchema),
    defaultValues: emptyRuleSetForm,
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  function go(next: RuleSetListSearch) {
    void navigate({ to: '/rule-sets', search: next });
  }

  function applyFilters(event: FormEvent) {
    event.preventDefault();
    setTrail([]);
    go({
      ...(q.trim() ? { q: q.trim() } : {}),
      ...(search.purpose ? { purpose: search.purpose } : {}),
    });
  }

  function setPurpose(purpose: string) {
    setTrail([]);
    go({
      ...(search.q ? { q: search.q } : {}),
      ...(purpose ? { purpose: purpose as RuleSetPurpose } : {}),
    });
  }

  function goNext() {
    const next = query.data?.nextCursor;
    if (!next) return;
    setTrail((previous) => [...previous, search.cursor ?? '']);
    go({ ...search, cursor: next });
  }

  function goPrevious() {
    const previous = trail[trail.length - 1];
    setTrail((rest) => rest.slice(0, -1));
    const { cursor: _cursor, ...others } = search;
    go(previous ? { ...others, cursor: previous } : others);
  }

  async function createSet(values: RuleSetFormValues) {
    setProblem(null);
    const body: CreateRuleSetRequest = {
      code: values.code,
      name: values.name,
      domainCode: values.domainCode,
      purpose: values.purpose,
    };
    try {
      const created = await create.mutateAsync({ body, idempotencyKey: createKey });
      setCreating(false);
      setCreateKey(randomId());
      form.reset(emptyRuleSetForm);
      toast.notify({ tone: 'success', title: t('rules.created') });
      await navigate({
        to: '/rule-sets/$ruleSetId',
        params: { ruleSetId: created.data.id },
      });
    } catch (err) {
      setProblem(problemOf(err));
      setCreateKey(randomId());
    }
  }

  const rows: RuleSet[] = query.data?.items ?? [];

  return (
    <>
      <PageHeader
        title={t('rules.title')}
        description={t('rules.intro')}
        actions={
          canDraft ? <Button onClick={() => setCreating(true)}>{t('rules.new')}</Button> : null
        }
      />

      <form onSubmit={applyFilters} className="mb-4 flex flex-wrap items-end gap-2" role="search">
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('rules.search')}</span>
          <Input name="q" value={q} onChange={(e) => setQ(e.target.value)} className="w-64" />
        </label>
        <label className="grid gap-1 text-sm">
          <span className="font-medium">{t('rules.purposeFilter')}</span>
          <Select
            name="purpose"
            value={search.purpose ?? ''}
            onChange={(e) => setPurpose(e.target.value)}
            placeholder={t('rules.allPurposes')}
            options={RULE_SET_PURPOSES.map((purpose) => ({
              value: purpose,
              label: t(`rules.purposes.${purpose}`),
            }))}
            className="w-56"
          />
        </label>
        <Button type="submit" variant="secondary">
          {t('common.search')}
        </Button>
        {search.q || search.purpose ? (
          <Button
            variant="ghost"
            onClick={() => {
              setQ('');
              setTrail([]);
              go({});
            }}
          >
            {t('common.clear')}
          </Button>
        ) : null}
      </form>

      <ProblemAlert
        problem={query.error ? problemOf(query.error) : null}
        className="mb-4"
        actions={
          <Button size="sm" variant="secondary" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        }
      />

      {query.isPending ? (
        <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </div>
      ) : rows.length === 0 ? (
        <EmptyState title={t('rules.empty')} description={t('rules.emptyHint')} />
      ) : (
        <>
          <Table aria-busy={query.isFetching || undefined} data-testid="rule-set-table">
            <THead>
              <TR>
                <TH>{t('rules.columns.code')}</TH>
                <TH>{t('rules.columns.name')}</TH>
                <TH>{t('rules.columns.purpose')}</TH>
                <TH>{t('rules.columns.domain')}</TH>
                <TH>{t('rules.columns.versions')}</TH>
                <TH>{t('rules.columns.status')}</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((ruleSet) => (
                <TR key={ruleSet.id}>
                  <TD>
                    <code className="font-mono text-xs">{ruleSet.code}</code>
                  </TD>
                  <TD>
                    <Link
                      to="/rule-sets/$ruleSetId"
                      params={{ ruleSetId: ruleSet.id }}
                      className="font-medium underline-offset-2 hover:underline"
                    >
                      {ruleSet.name}
                    </Link>
                  </TD>
                  <TD>{t(`rules.purposes.${ruleSet.purpose}`)}</TD>
                  <TD>{t(`catalog.domains.${ruleSet.domainCode}`)}</TD>
                  <TD>{ruleSet.versionCount}</TD>
                  <TD>
                    <Badge tone={statusTone(ruleSet.status)}>
                      {t(`rules.statuses.${ruleSet.status}`)}
                    </Badge>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
          <nav
            className="mt-3 flex items-center justify-between text-sm"
            aria-labelledby={pagingLabelId}
          >
            <span id={pagingLabelId} className="text-fg-muted">
              {t('organizations.page', { n: trail.length + 1 })}
            </span>
            <div className="flex gap-2">
              <Button
                variant="secondary"
                size="sm"
                onClick={goPrevious}
                disabled={trail.length === 0}
              >
                {t('organizations.prevPage')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={goNext}
                disabled={!query.data?.nextCursor}
              >
                {t('organizations.nextPage')}
              </Button>
            </div>
          </nav>
        </>
      )}

      <Dialog
        open={creating}
        onOpenChange={(open) => {
          if (!open) setCreating(false);
        }}
        title={t('rules.createTitle')}
      >
        <form onSubmit={form.handleSubmit(createSet)} className="grid gap-4" noValidate>
          <ProblemAlert problem={problem} />
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('rules.fields.code')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.code)}
            >
              <Input {...form.register('code')} autoComplete="off" className="font-mono" />
            </FormField>
            <FormField
              label={t('rules.fields.name')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(form.formState.errors.name)}
            >
              <Input {...form.register('name')} />
            </FormField>
            <FormField
              label={t('rules.fields.purpose')}
              error={message(form.formState.errors.purpose)}
            >
              <Select
                {...form.register('purpose')}
                options={RULE_SET_PURPOSES.map((purpose) => ({
                  value: purpose,
                  label: t(`rules.purposes.${purpose}`),
                }))}
              />
            </FormField>
            <FormField
              label={t('rules.fields.domain')}
              error={message(form.formState.errors.domainCode)}
            >
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
            <Button variant="secondary" onClick={() => setCreating(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={create.isPending}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  );
}
