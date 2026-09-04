import type { RuleTestCase, RuleTestCaseInput, RuleTestRunResult } from '@kapsora/api-client';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  FormField,
  Input,
  ProblemAlert,
  Select,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  Textarea,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { useFieldArray, useForm } from 'react-hook-form';

import { parseIssueMessage, type problemOf } from '../problems';
import { rowFieldErrors } from './RulesEditor';
import {
  RULE_OUTCOMES,
  emptyTestCaseRow,
  testCasesFormSchema,
  toTestCaseInput,
  toTestCaseRow,
  type TestCasesFormValues,
} from './schema';

export interface TestCasesEditorProps {
  testCases: RuleTestCase[];
  /** Saves the whole set; the API replaces the list rather than patching rows. */
  onSave: (items: RuleTestCaseInput[]) => Promise<void>;
  saving: boolean;
  problem: ReturnType<typeof problemOf> | null;
}

/**
 * The test cases of a draft version. A case is an input document, the outcome it must
 * fold to and the explanation codes it must produce; the run compares them and nothing
 * else. Without at least one passing case the version cannot be submitted, which is the
 * whole reason these rows exist.
 */
export function TestCasesEditor({ testCases, onSave, saving, problem }: TestCasesEditorProps) {
  const { t } = useTranslation();
  const form = useForm<TestCasesFormValues>({
    resolver: zodResolver(testCasesFormSchema),
    defaultValues: {
      items: testCases.length > 0 ? testCases.map(toTestCaseRow) : [emptyTestCaseRow],
    },
    mode: 'onBlur',
  });
  const rows = useFieldArray({ control: form.control, name: 'items' });
  const watched = form.watch('items');
  const serverErrors = rowFieldErrors(problem);

  function message(
    error: { message?: string } | undefined,
    index: number,
    field: string,
  ): string | undefined {
    if (error?.message) {
      const { code, params } = parseIssueMessage(error.message);
      return fieldErrorMessage(t, code, undefined, params);
    }
    const server = serverErrors.get(`${index}.${field}`);
    if (server) return fieldErrorMessage(t, server.code, server.message);
    return undefined;
  }

  async function submit(values: TestCasesFormValues) {
    await onSave(values.items.map(toTestCaseInput));
  }

  return (
    <form
      onSubmit={form.handleSubmit(submit)}
      className="grid gap-4"
      noValidate
      data-testid="test-cases-form"
    >
      <ProblemAlert problem={problem} hideFieldErrors />

      {rows.fields.map((field, index) => {
        const row = watched[index];
        const errors = form.formState.errors.items?.[index];
        return (
          <fieldset
            key={field.id}
            className="border-line grid gap-3 rounded-md border p-4"
            data-testid={`test-case-row-${index}`}
          >
            <legend className="text-fg-muted px-1 text-xs font-medium">
              {row?.code || t('rules.tests.columns.code')}
            </legend>
            <div className="grid gap-3 md:grid-cols-3">
              <FormField
                label={t('rules.tests.fields.code')}
                required
                requiredLabel={t('common.requiredMark')}
                error={message(errors?.code, index, 'code')}
              >
                <Input
                  {...form.register(`items.${index}.code`)}
                  autoComplete="off"
                  className="font-mono"
                />
              </FormField>
              <FormField
                label={t('rules.tests.fields.description')}
                error={message(errors?.description, index, 'description')}
              >
                <Input {...form.register(`items.${index}.description`)} />
              </FormField>
              <FormField
                label={t('rules.tests.fields.expectedOutcome')}
                error={message(errors?.expectedOutcome, index, 'expectedOutcome')}
              >
                <Select
                  {...form.register(`items.${index}.expectedOutcome`)}
                  options={RULE_OUTCOMES.map((outcome) => ({
                    value: outcome,
                    label: t(`rules.outcomes.${outcome}`),
                  }))}
                />
              </FormField>
            </div>
            <FormField
              label={t('rules.tests.fields.input')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(errors?.input, index, 'input')}
            >
              <Textarea
                {...form.register(`items.${index}.input`)}
                rows={4}
                spellCheck={false}
                autoComplete="off"
                className="font-mono"
              />
            </FormField>
            <FormField
              label={t('rules.tests.fields.expectedExplanations')}
              error={message(errors?.expectedExplanations, index, 'expectedExplanations')}
            >
              <Input
                {...form.register(`items.${index}.expectedExplanations`)}
                autoComplete="off"
                className="font-mono"
              />
            </FormField>
            <div className="flex justify-end">
              <Button
                variant="ghost"
                size="sm"
                onClick={() => rows.remove(index)}
                disabled={rows.fields.length === 1}
              >
                {t('rules.tests.remove')}
              </Button>
            </div>
          </fieldset>
        );
      })}

      <div className="flex justify-between gap-2">
        <Button variant="secondary" onClick={() => rows.append(emptyTestCaseRow)}>
          {t('rules.tests.add')}
        </Button>
        <Button type="submit" loading={saving}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/** Read-only view of the cases of a version nobody may change any more. */
export function TestCasesTable({ testCases }: { testCases: RuleTestCase[] }) {
  const { t } = useTranslation();
  if (testCases.length === 0) {
    return <p className="text-fg-muted text-sm">{t('rules.tests.empty')}</p>;
  }
  return (
    <Table data-testid="test-case-table">
      <THead>
        <TR>
          <TH>{t('rules.tests.columns.code')}</TH>
          <TH>{t('rules.tests.columns.description')}</TH>
          <TH>{t('rules.tests.columns.expected')}</TH>
        </TR>
      </THead>
      <TBody>
        {testCases.map((testCase) => (
          <TR key={testCase.id}>
            <TD>
              <code className="font-mono text-xs">{testCase.code}</code>
            </TD>
            <TD>{testCase.description || t('common.none')}</TD>
            <TD>
              {t(`rules.outcomes.${testCase.expectedOutcome}`)}
              {testCase.expectedExplanations.length > 0 ? (
                <span className="text-fg-muted block font-mono text-xs">
                  {testCase.expectedExplanations.join(', ')}
                </span>
              ) : null}
            </TD>
          </TR>
        ))}
      </TBody>
    </Table>
  );
}

export interface TestRunResultsProps {
  run: RuleTestRunResult | undefined;
  running: boolean;
  onRun: () => void;
}

/**
 * The run and what it found. It writes nothing, so the button may be pressed as often as
 * the author likes; its answer is what decides whether the version can be submitted.
 */
export function TestRunResults({ run, running, onRun }: TestRunResultsProps) {
  const { t } = useTranslation();
  return (
    <div className="grid gap-3" data-testid="test-run">
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="secondary" onClick={onRun} loading={running}>
          {running ? t('rules.tests.running') : t('rules.tests.run')}
        </Button>
        {run ? (
          <p role="status" className="text-sm">
            {run.passed
              ? t('rules.tests.allPassed', { count: run.total })
              : t('rules.tests.someFailed', { failed: run.failed, total: run.total })}
          </p>
        ) : null}
      </div>

      {run && run.cases.length > 0 ? (
        <Table data-testid="test-result-table">
          <THead>
            <TR>
              <TH>{t('rules.tests.columns.code')}</TH>
              <TH>{t('rules.tests.columns.expected')}</TH>
              <TH>{t('rules.tests.columns.actual')}</TH>
              <TH>{t('rules.tests.columns.result')}</TH>
            </TR>
          </THead>
          <TBody>
            {run.cases.map((result) => (
              <TR key={result.code}>
                <TD>
                  <code className="font-mono text-xs">{result.code}</code>
                </TD>
                <TD>
                  {t(`rules.outcomes.${result.expectedOutcome}`)}
                  <span className="text-fg-muted block font-mono text-xs">
                    {result.expectedExplanations.join(', ') || t('common.none')}
                  </span>
                </TD>
                <TD>
                  {t(`rules.outcomes.${result.actualOutcome}`)}
                  <span className="text-fg-muted block font-mono text-xs">
                    {result.actualExplanations.join(', ') || t('common.none')}
                  </span>
                </TD>
                <TD>
                  <Badge tone={result.passed ? 'success' : 'danger'}>
                    {result.passed ? t('rules.tests.passed') : t('rules.tests.failed')}
                  </Badge>
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
      ) : null}
    </div>
  );
}
