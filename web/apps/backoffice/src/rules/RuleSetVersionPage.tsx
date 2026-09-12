import type {
  ReasonCommand,
  RuleInput,
  RuleTestCaseInput,
  UpdateRuleSetVersionRequest,
} from '@kapsora/api-client';
import { usePermission, useSession, useStepUp } from '@kapsora/auth';
import { fieldErrorMessage, formatDate, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Breadcrumb,
  Button,
  Card,
  Dialog,
  FormField,
  HelpHint,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  StepUpDialog,
  Textarea,
  statusTone,
  useToast,
} from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { zodResolver } from '@hookform/resolvers/zod';
import { useEffect, useState } from 'react';
import { useForm } from 'react-hook-form';

import { parseIssueMessage, problemOf } from '../problems';
import { RulesEditor, RulesTable } from './RulesEditor';
import { SimulationPanel } from './SimulationPanel';
import { TestCasesEditor, TestCasesTable, TestRunResults } from './TestCasesEditor';
import {
  useRuleSet,
  useRuleSetVersion,
  useRuleSetVersionCommands,
  useRuleTestRun,
} from './queries';
import { useRuleParams } from './routing';
import {
  RETIRE_REASONS,
  parseInputSchema,
  retireFormSchema,
  ruleVersionDraftFormSchema,
  type RetireFormValues,
  type RuleVersionDraftFormValues,
} from './schema';

/**
 * A rule set version: the draft is authored, submitted for review, published by someone
 * else and eventually retired. Status decides everything on this page, because a
 * published version is what recorded decisions were made from and must never move under
 * them — and submitting is gated on a passing test run, because an untested rule that
 * decides what a member is owed is not a rule anybody should have to trust.
 */
export function RuleSetVersionPage() {
  const { t } = useTranslation();
  const toast = useToast();
  const ruleSetVersionId = useRuleParams()['ruleSetVersionId'] ?? '';
  const actorId = useSession((s) => s.session?.actorId ?? null);
  const canDraft = usePermission('rule.draft');
  const canPublish = usePermission('rule.publish');
  const version = useRuleSetVersion(ruleSetVersionId);
  const ruleSet = useRuleSet(version.data?.data.ruleSetId ?? '');
  const commands = useRuleSetVersionCommands(ruleSetVersionId);
  const stepUp = useStepUp();
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [rulesProblem, setRulesProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [testsProblem, setTestsProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [retiring, setRetiring] = useState(false);
  const [comment, setComment] = useState('');
  const [confirming, setConfirming] = useState<'submit' | 'publish' | null>(null);

  const loaded = version.data?.data;
  // The run writes nothing, so the page may ask for it the moment there is a case to run.
  const testRun = useRuleTestRun(ruleSetVersionId, (loaded?.testCases.length ?? 0) > 0);

  const draftForm = useForm<RuleVersionDraftFormValues>({
    resolver: zodResolver(ruleVersionDraftFormSchema),
    defaultValues: { validFrom: '', validTo: '', notes: '', inputSchema: '{}' },
    mode: 'onBlur',
  });
  const retireForm = useForm<RetireFormValues>({
    resolver: zodResolver(retireFormSchema),
    defaultValues: { reasonCode: 'SUPERSEDED', reasonText: '' },
    mode: 'onBlur',
  });

  useEffect(() => {
    if (!loaded) return;
    draftForm.reset({
      validFrom: loaded.validFrom ?? '',
      validTo: loaded.validTo ?? '',
      notes: loaded.notes ?? '',
      inputSchema: JSON.stringify(loaded.inputSchema, null, 2),
    });
  }, [loaded, draftForm]);

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function saveDraft(values: RuleVersionDraftFormValues) {
    if (!version.data) return;
    setProblem(null);
    const patch: UpdateRuleSetVersionRequest = {
      validFrom: values.validFrom,
      validTo: values.validTo === '' ? null : values.validTo,
      notes: values.notes === '' ? null : values.notes,
      inputSchema: parseInputSchema(values.inputSchema),
    };
    try {
      await commands.patch.mutateAsync({ etag: version.data.etag, patch });
      toast.notify({ tone: 'success', title: t('rules.updated') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function saveRules(items: RuleInput[]) {
    if (!version.data) return;
    setRulesProblem(null);
    try {
      await commands.rules.mutateAsync({ etag: version.data.etag, items });
      toast.notify({ tone: 'success', title: t('rules.items.saved') });
    } catch (err) {
      // A condition that does not compile answers 422 naming the rule; the editor puts it
      // on that rule's own field rather than leaving it in a page-level alert.
      setRulesProblem(problemOf(err));
    }
  }

  async function saveTestCases(items: RuleTestCaseInput[]) {
    if (!version.data) return;
    setTestsProblem(null);
    try {
      await commands.testCases.mutateAsync({ etag: version.data.etag, items });
      toast.notify({ tone: 'success', title: t('rules.tests.saved') });
    } catch (err) {
      setTestsProblem(problemOf(err));
    }
  }

  async function submitForReview() {
    if (!version.data) return;
    setProblem(null);
    setConfirming(null);
    try {
      await commands.submit.mutateAsync({
        etag: version.data.etag,
        ...(comment !== '' ? { body: { comment } } : {}),
      });
      setComment('');
      toast.notify({ tone: 'success', title: t('rules.versions.submitted') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function publish() {
    if (!version.data) return;
    setProblem(null);
    setConfirming(null);
    const etag = version.data.etag;
    try {
      // Publishing needs a fresh password; the dialog retries this same call after it.
      await stepUp.run(() =>
        commands.publish.mutateAsync({
          etag,
          ...(comment !== '' ? { body: { comment } } : {}),
        }),
      );
      setComment('');
      toast.notify({ tone: 'success', title: t('rules.versions.published') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  async function retire(values: RetireFormValues) {
    if (!version.data) return;
    setProblem(null);
    const etag = version.data.etag;
    const body: ReasonCommand = {
      reasonCode: values.reasonCode,
      ...(values.reasonText !== '' ? { reasonText: values.reasonText } : {}),
    };
    try {
      await stepUp.run(() => commands.retire.mutateAsync({ etag, body }));
      setRetiring(false);
      toast.notify({ tone: 'success', title: t('rules.versions.retired') });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  if (version.isPending) {
    return (
      <div className="text-fg-muted flex items-center gap-2 p-6 text-sm" aria-busy="true">
        <Spinner /> {t('common.loading')}
      </div>
    );
  }
  if (!version.data) {
    return <ProblemAlert problem={problemOf(version.error)} />;
  }

  const row = version.data.data;
  const setName = ruleSet.data?.data.name;
  const isDraft = row.status === 'DRAFT';
  const isUnderReview = row.status === 'UNDER_REVIEW';
  const isPublished = row.status === 'PUBLISHED';
  const editable = isDraft && canDraft;
  // The submitter may not publish their own work; the server refuses it either way, but
  // offering the button and then explaining the refusal would waste the operator's time.
  const submittedByMe = row.submittedBy != null && row.submittedBy === actorId;
  const run = testRun.data;
  const failingCodes = run ? run.cases.filter((c) => !c.passed).map((c) => c.code) : [];
  // Submit stands behind the gate: at least one case, and every one of them passing.
  const testsPass = row.testCases.length > 0 && run?.passed === true;
  const busy =
    commands.submit.isPending ||
    commands.publish.isPending ||
    commands.retire.isPending ||
    stepUp.busy;

  return (
    <>
      <PageHeader
        title={`${setName ?? t('rules.versions.title')} · v${row.versionNo}`}
        description={
          row.validFrom
            ? `${formatDate(row.validFrom)}${row.validTo ? ` – ${formatDate(row.validTo)}` : ''}`
            : undefined
        }
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
              {
                label: setName ?? t('rules.detailTitle'),
                render: (label) => (
                  <Link to="/rule-sets/$ruleSetId" params={{ ruleSetId: row.ruleSetId }}>
                    {label}
                  </Link>
                ),
              },
              { label: `v${row.versionNo}` },
            ]}
          />
        }
        actions={
          <>
            <Badge tone={statusTone(row.status)}>
              {t(`rules.versions.statuses.${row.status}`)}
            </Badge>
            {editable && testsPass ? (
              <Button onClick={() => setConfirming('submit')} disabled={busy}>
                {t('rules.versions.submit')}
              </Button>
            ) : null}
            {isUnderReview && canPublish && !submittedByMe ? (
              <Button onClick={() => setConfirming('publish')} disabled={busy}>
                {t('rules.versions.publish')}
              </Button>
            ) : null}
            {isPublished && canPublish ? (
              <Button variant="secondary" onClick={() => setRetiring(true)} disabled={busy}>
                {t('rules.versions.retire')}
              </Button>
            ) : null}
          </>
        }
      />

      {isDraft && row.testCases.length === 0 ? (
        <p role="status" className="bg-warning-soft text-fg mb-4 rounded-md p-3 text-sm">
          {t('rules.versions.testsRequired')}
        </p>
      ) : null}
      {isDraft && failingCodes.length > 0 ? (
        <p role="status" className="bg-warning-soft text-fg mb-4 rounded-md p-3 text-sm">
          {t('rules.versions.testsFailing', { codes: failingCodes.join(', ') })}
        </p>
      ) : null}
      {isUnderReview && submittedByMe ? (
        <p role="status" className="bg-warning-soft text-fg mb-4 rounded-md p-3 text-sm">
          {t('rules.versions.sameActorBlocked')}
        </p>
      ) : null}
      {isPublished || row.status === 'RETIRED' ? (
        <p className="text-fg-muted mb-4 text-sm">{t('rules.versions.readOnly')}</p>
      ) : null}

      <ProblemAlert problem={problem} className="mb-4" />

      <div className="grid gap-4">
        <Card>
          <h2 className="text-base font-semibold">{t('rules.versions.detailTitle')}</h2>
          {editable ? (
            <form
              onSubmit={draftForm.handleSubmit(saveDraft)}
              className="mt-3 grid gap-4"
              noValidate
              data-testid="rule-version-draft-form"
            >
              <div className="grid gap-4 md:grid-cols-3">
                <FormField
                  label={t('rules.versions.fields.validFrom')}
                  required
                  requiredLabel={t('common.requiredMark')}
                  error={message(draftForm.formState.errors.validFrom)}
                >
                  <Input {...draftForm.register('validFrom')} type="date" />
                </FormField>
                <FormField
                  label={t('rules.versions.fields.validTo')}
                  error={message(draftForm.formState.errors.validTo)}
                >
                  <Input {...draftForm.register('validTo')} type="date" />
                </FormField>
                <FormField
                  label={t('rules.versions.fields.notes')}
                  error={message(draftForm.formState.errors.notes)}
                >
                  <Input {...draftForm.register('notes')} />
                </FormField>
              </div>
              <FormField
                label={t('rules.versions.fields.inputSchema')}
                required
                requiredLabel={t('common.requiredMark')}
                hint={t('rules.items.conditionHint')}
                error={message(draftForm.formState.errors.inputSchema)}
              >
                <Textarea
                  {...draftForm.register('inputSchema')}
                  rows={4}
                  spellCheck={false}
                  autoComplete="off"
                  className="font-mono"
                />
              </FormField>
              <div className="flex justify-end">
                <Button type="submit" variant="secondary" loading={commands.patch.isPending}>
                  {t('common.save')}
                </Button>
              </div>
            </form>
          ) : null}
          <dl className="mt-3 grid grid-cols-[max-content_minmax(0,1fr)] [&>dd]:min-w-0 [&>dd]:break-words gap-x-6 gap-y-2 text-sm">
            {!editable ? (
              <>
                <dt className="text-fg-muted">{t('rules.versions.fields.validFrom')}</dt>
                <dd>{formatDate(row.validFrom) || t('common.none')}</dd>
                <dt className="text-fg-muted">{t('rules.versions.fields.validTo')}</dt>
                <dd>{formatDate(row.validTo) || t('common.none')}</dd>
                <dt className="text-fg-muted">{t('rules.versions.fields.notes')}</dt>
                <dd>{row.notes || t('common.none')}</dd>
                <dt className="text-fg-muted">{t('rules.versions.fields.inputSchema')}</dt>
                <dd className="font-mono text-xs">
                  {Object.entries(row.inputSchema)
                    .map(([name, type]) => `${name}: ${type}`)
                    .join(', ') || t('common.none')}
                </dd>
              </>
            ) : null}
            {row.submittedBy ? (
              <>
                <dt className="text-fg-muted">{t('rules.versions.fields.submittedBy')}</dt>
                <dd>{formatDate(row.submittedAt) || t('common.none')}</dd>
              </>
            ) : null}
            {row.publishedBy ? (
              <>
                <dt className="text-fg-muted">{t('rules.versions.fields.publishedBy')}</dt>
                <dd>{formatDate(row.publishedAt) || t('common.none')}</dd>
              </>
            ) : null}
            {row.contentHash ? (
              <>
                <dt className="text-fg-muted">{t('rules.versions.fields.contentHash')}</dt>
                <dd>
                  <code className="font-mono text-xs">{row.contentHash}</code>
                </dd>
              </>
            ) : null}
          </dl>
        </Card>

        <Card>
          {/* The mark sits beside the heading, not inside it: it is not part of the name. */}
          <div className="mb-3 flex items-baseline gap-1.5">
            <h2 className="text-base font-semibold">{t('rules.items.title')}</h2>
            <HelpHint term="kural" />
          </div>
          {editable ? (
            <RulesEditor
              rules={row.rules}
              onSave={saveRules}
              saving={commands.rules.isPending}
              problem={rulesProblem}
            />
          ) : (
            <RulesTable rules={row.rules} />
          )}
        </Card>

        <Card>
          <h2 className="mb-3 text-base font-semibold">{t('rules.tests.title')}</h2>
          {editable ? (
            <TestCasesEditor
              testCases={row.testCases}
              onSave={saveTestCases}
              saving={commands.testCases.isPending}
              problem={testsProblem}
            />
          ) : (
            <TestCasesTable testCases={row.testCases} />
          )}
          {row.testCases.length > 0 ? (
            <div className="border-line mt-4 border-t pt-4">
              <TestRunResults
                run={run}
                running={testRun.isFetching}
                onRun={() => void testRun.refetch()}
              />
            </div>
          ) : null}
        </Card>

        <Card>
          <h2 className="mb-3 text-base font-semibold">{t('rules.simulation.title')}</h2>
          <SimulationPanel ruleSetVersionId={row.id} inputSchema={row.inputSchema} />
        </Card>
      </div>

      <Dialog
        open={confirming !== null}
        onOpenChange={(open) => {
          if (!open) setConfirming(null);
        }}
        title={confirming === 'publish' ? t('rules.versions.publish') : t('rules.versions.submit')}
        description={
          confirming === 'publish'
            ? t('rules.versions.publishConfirm')
            : t('rules.versions.submitConfirm')
        }
      >
        <div className="grid gap-4">
          <FormField label={t('rules.versions.fields.notes')}>
            <Textarea value={comment} onChange={(e) => setComment(e.target.value)} rows={3} />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setConfirming(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              loading={busy}
              onClick={() => {
                if (confirming === 'publish') void publish();
                else void submitForReview();
              }}
            >
              {confirming === 'publish' ? t('rules.versions.publish') : t('rules.versions.submit')}
            </Button>
          </div>
        </div>
      </Dialog>

      <Dialog
        open={retiring}
        onOpenChange={(open) => {
          if (!open) setRetiring(false);
        }}
        title={t('rules.versions.retire')}
        description={t('rules.versions.retireConfirm')}
      >
        <form onSubmit={retireForm.handleSubmit(retire)} className="grid gap-4" noValidate>
          <FormField
            label={t('rules.versions.retireReason')}
            required
            requiredLabel={t('common.requiredMark')}
            error={message(retireForm.formState.errors.reasonCode)}
          >
            <Select
              {...retireForm.register('reasonCode')}
              options={RETIRE_REASONS.map((reason) => ({
                value: reason,
                label: t(`plans.versions.retireReasons.${reason}`),
              }))}
            />
          </FormField>
          <FormField
            label={t('rules.versions.fields.notes')}
            error={message(retireForm.formState.errors.reasonText)}
          >
            <Textarea {...retireForm.register('reasonText')} rows={3} />
          </FormField>
          <div className="flex justify-end gap-2">
            <Button variant="secondary" onClick={() => setRetiring(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" loading={busy}>
              {t('rules.versions.retire')}
            </Button>
          </div>
        </form>
      </Dialog>

      <StepUpDialog
        open={stepUp.required}
        action={t('rules.versions.publish')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </>
  );
}
