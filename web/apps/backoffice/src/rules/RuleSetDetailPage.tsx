import {
  randomId,
  type CreateRuleSetVersionRequest,
  type RuleSetVersionSummary,
  type UpdateRuleSetRequest,
} from '@kapsora/api-client';
import { usePermission } from '@kapsora/auth';
import { fieldErrorMessage, formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
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
  Textarea,
  statusTone,
  useToast,
} from '@kapsora/ui';
import { Link, useNavigate } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { useEffect, useState } from 'react';
import { useForm } from 'react-hook-form';

import { parseIssueMessage, problemOf } from '../problems';
import {
  useCreateRuleSetVersion,
  useRuleSet,
  useRuleSetVersions,
  useUpdateRuleSet,
} from './queries';
import { useRuleParams } from './routing';
import {
  RULE_SET_STATUSES,
  emptyRuleSetVersionForm,
  ruleSetEditFormSchema,
  ruleSetVersionFormSchema,
  type RuleSetEditFormValues,
  type RuleSetVersionFormValues,
} from './schema';

/** Half-open period, printed the way an operator reads a validity window. */
function period(t: (key: string) => string, from?: string | null, to?: string | null): string {
  const start = formatDate(from) || t('common.none');
  return to ? `${start} – ${formatDate(to)}` : start;
}

/**
 * One rule set with its version history. The set is a name and a purpose; the rules live
 * in its versions, and only a published version ever decides anything.
 */
export function RuleSetDetailPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const navigate = useNavigate();
  const ruleSetId = useRuleParams()['ruleSetId'] ?? '';
  const canDraft = usePermission('rule.draft');
  const ruleSet = useRuleSet(ruleSetId);
  const versions = useRuleSetVersions(ruleSetId);
  const update = useUpdateRuleSet(ruleSetId);
  const createVersion = useCreateRuleSetVersion(ruleSetId);
  const [adding, setAdding] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [versionKey, setVersionKey] = useState(randomId);

  const editForm = useForm<RuleSetEditFormValues>({
    resolver: zodResolver(ruleSetEditFormSchema),
    defaultValues: { name: '', status: 'ACTIVE' },
    mode: 'onBlur',
  });
  const versionForm = useForm<RuleSetVersionFormValues>({
    resolver: zodResolver(ruleSetVersionFormSchema),
    defaultValues: emptyRuleSetVersionForm,
    mode: 'onBlur',
  });

  const loaded = ruleSet.data?.data;
  useEffect(() => {
    if (!loaded) return;
    editForm.reset({ name: loaded.name, status: loaded.status });
  }, [loaded, editForm]);

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function saveSet(values: RuleSetEditFormValues) {
    if (!ruleSet.data) return;
    setProblem(null);
    const patch: UpdateRuleSetRequest = { name: values.name, status: values.status };
    try {
      await update.mutateAsync({ etag: ruleSet.data.etag, patch });
      toast.notify({ tone: 'success', title: t('rules.updated') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function submitVersion(values: RuleSetVersionFormValues) {
    setProblem(null);
    const body: CreateRuleSetVersionRequest = {
      validFrom: values.validFrom,
      ...(values.validTo !== '' ? { validTo: values.validTo } : {}),
      ...(values.notes !== '' ? { notes: values.notes } : {}),
      ...(values.copyFromVersionId !== '' ? { copyFromVersionId: values.copyFromVersionId } : {}),
    };
    try {
      const created = await createVersion.mutateAsync({ body, idempotencyKey: versionKey });
      setAdding(false);
      setVersionKey(randomId());
      versionForm.reset(emptyRuleSetVersionForm);
      await navigate({
        to: '/rule-set-versions/$ruleSetVersionId',
        params: { ruleSetVersionId: created.data.id },
      });
    } catch (err) {
      setProblem(problemOf(err));
      setVersionKey(randomId());
    }
  }

  if (ruleSet.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (!ruleSet.data) {
    return <ProblemAlert problem={problemOf(ruleSet.error)} />;
  }

  const row = ruleSet.data.data;
  // Newest first: the version an operator needs is almost always the most recent one.
  const history: RuleSetVersionSummary[] = [...(versions.data ?? [])].sort(
    (a, b) => b.versionNo - a.versionNo,
  );

  return (
    <>
      <PageHeader
        title={row.name}
        description={row.code}
        breadcrumb={
          <Breadcrumb
            items={[
              {
                label: t('rules.title'),
                render: (label) => (
                  <Link to="/rule-sets" search={{}}>
                    {label}
                  </Link>
                ),
              },
              { label: row.name },
            ]}
          />
        }
        actions={
          <>
            <Badge tone={statusTone(row.status)}>{t(`rules.statuses.${row.status}`)}</Badge>
            {canDraft ? (
              <Button size="sm" variant="secondary" onClick={() => setAdding(true)}>
                {t('rules.versions.new')}
              </Button>
            ) : null}
          </>
        }
      />

      <ProblemAlert problem={problem} className="mb-4" />

      <div className="grid gap-4">
        <Card>
          <h2 className="text-base font-semibold">{t('rules.detailTitle')}</h2>
          <dl className="mt-3 grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
            <dt className="text-fg-muted">{t('rules.fields.code')}</dt>
            <dd>
              <code className="font-mono text-xs">{row.code}</code>
            </dd>
            <dt className="text-fg-muted">{t('rules.fields.purpose')}</dt>
            <dd>{t(`rules.purposes.${row.purpose}`)}</dd>
            <dt className="text-fg-muted">{t('rules.fields.domain')}</dt>
            <dd>{t(`catalog.domains.${row.domainCode}`)}</dd>
          </dl>
          {canDraft ? (
            <form
              onSubmit={editForm.handleSubmit(saveSet)}
              className="mt-4 grid gap-4"
              noValidate
              data-testid="rule-set-form"
            >
              <div className="grid gap-4 md:grid-cols-2">
                <FormField
                  label={t('rules.fields.name')}
                  required
                  requiredLabel={t('common.requiredMark')}
                  error={message(editForm.formState.errors.name)}
                >
                  <Input {...editForm.register('name')} />
                </FormField>
                <FormField
                  label={t('rules.fields.status')}
                  error={message(editForm.formState.errors.status)}
                >
                  <Select
                    {...editForm.register('status')}
                    options={RULE_SET_STATUSES.map((status) => ({
                      value: status,
                      label: t(`rules.statuses.${status}`),
                    }))}
                  />
                </FormField>
              </div>
              <div className="flex justify-end">
                <Button type="submit" variant="secondary" loading={update.isPending}>
                  {t('common.save')}
                </Button>
              </div>
            </form>
          ) : null}
        </Card>

        <Card>
          <h2 className="text-base font-semibold">{t('rules.versions.title')}</h2>
          {versions.isPending ? (
            <div className="text-fg-muted flex items-center gap-2 py-4 text-sm" aria-busy="true">
              <Spinner /> {t('common.loading')}
            </div>
          ) : history.length === 0 ? (
            <div className="mt-3">
              <EmptyState title={t('rules.versions.empty')} />
            </div>
          ) : (
            <div className="mt-3">
              <Table data-testid="rule-version-table">
                <THead>
                  <TR>
                    <TH>{t('rules.versions.columns.versionNo')}</TH>
                    <TH>{t('rules.versions.columns.period')}</TH>
                    <TH>{t('rules.versions.columns.rules')}</TH>
                    <TH>{t('rules.versions.columns.status')}</TH>
                  </TR>
                </THead>
                <TBody>
                  {history.map((version) => (
                    <TR key={version.id}>
                      <TD>
                        <Link
                          to="/rule-set-versions/$ruleSetVersionId"
                          params={{ ruleSetVersionId: version.id }}
                          className="font-medium underline-offset-2 hover:underline"
                        >
                          {`v${version.versionNo}`}
                        </Link>
                      </TD>
                      <TD>{period(t, version.validFrom, version.validTo)}</TD>
                      <TD>{version.ruleCount}</TD>
                      <TD>
                        <Badge tone={statusTone(version.status)}>
                          {t(`rules.versions.statuses.${version.status}`)}
                        </Badge>
                      </TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </div>
          )}
        </Card>
      </div>

      <Dialog
        open={adding}
        onOpenChange={(open) => {
          if (!open) setAdding(false);
        }}
        title={t('rules.versions.createTitle')}
      >
        <form onSubmit={versionForm.handleSubmit(submitVersion)} className="grid gap-4" noValidate>
          <div className="grid gap-4 md:grid-cols-2">
            <FormField
              label={t('rules.versions.fields.validFrom')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(versionForm.formState.errors.validFrom)}
            >
              <Input {...versionForm.register('validFrom')} type="date" />
            </FormField>
            <FormField
              label={t('rules.versions.fields.validTo')}
              error={message(versionForm.formState.errors.validTo)}
            >
              <Input {...versionForm.register('validTo')} type="date" />
            </FormField>
          </div>
          <FormField label={t('plans.versions.copyFrom')}>
            <Select
              {...versionForm.register('copyFromVersionId')}
              placeholder={t('plans.versions.copyFromNone')}
              options={history.map((version) => ({
                value: version.id,
                label: `v${version.versionNo} · ${t(`rules.versions.statuses.${version.status}`)}`,
              }))}
            />
          </FormField>
          <FormField
            label={t('rules.versions.fields.notes')}
            error={message(versionForm.formState.errors.notes)}
          >
            <Textarea {...versionForm.register('notes')} rows={3} />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setAdding(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={createVersion.isPending}>
              {t('common.save')}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  );
}
