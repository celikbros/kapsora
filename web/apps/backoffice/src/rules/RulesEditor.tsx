import type { Rule, RuleInput } from '@kapsora/api-client';
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
import { useFieldArray, useForm, type UseFormReturn } from 'react-hook-form';

import { parseIssueMessage, type problemOf } from '../problems';
import {
  RULE_ACTION_TYPES,
  emptyRuleRow,
  nextPriority,
  rulesFormSchema,
  toRuleInput,
  toRuleRow,
  type RulesFormValues,
} from './schema';

/**
 * Server field errors of a set write, keyed by `${index}.${field}`. A condition that does
 * not compile answers 422 on `items[3].condition` naming the rule, and it belongs on that
 * rule's own field rather than in a page-level alert nobody can act on.
 */
export function rowFieldErrors(
  problem: ReturnType<typeof problemOf> | null,
): Map<string, { code: string; message?: string }> {
  const out = new Map<string, { code: string; message?: string }>();
  for (const error of problem?.errors ?? []) {
    const match = /^items\[(\d+)\]\.(.+)$/.exec(error.field);
    if (!match) continue;
    out.set(`${match[1]}.${match[2]}`, {
      code: error.code,
      ...(error.message ? { message: error.message } : {}),
    });
  }
  return out;
}

export interface RulesEditorProps {
  rules: Rule[];
  /** Saves the whole set; the API replaces the list rather than patching rows. */
  onSave: (items: RuleInput[]) => Promise<void>;
  saving: boolean;
  problem: ReturnType<typeof problemOf> | null;
}

/** The actions of one rule: a closed list of types, each with an optional JSON payload. */
function ActionsField({ form, index }: { form: UseFormReturn<RulesFormValues>; index: number }) {
  const { t } = useTranslation();
  const actions = useFieldArray({ control: form.control, name: `items.${index}.actions` });
  return (
    <div className="grid gap-2">
      <span className="text-fg text-sm font-medium">{t('rules.actions.title')}</span>
      {actions.fields.map((field, actionIndex) => (
        <div key={field.id} className="flex items-end gap-2">
          <Select
            {...form.register(`items.${index}.actions.${actionIndex}.type`)}
            aria-label={t('rules.actions.title')}
            options={RULE_ACTION_TYPES.map((type) => ({
              value: type,
              label: t(`rules.actions.${type}`),
            }))}
            className="w-56"
          />
          <Input
            {...form.register(`items.${index}.actions.${actionIndex}.payload`)}
            aria-label={t('common.details')}
            autoComplete="off"
            className="font-mono"
          />
          <Button
            variant="ghost"
            size="sm"
            onClick={() => actions.remove(actionIndex)}
            disabled={actions.fields.length === 1}
          >
            {t('rules.items.remove')}
          </Button>
        </div>
      ))}
      <div>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => actions.append({ type: 'WARN', payload: '' })}
        >
          {t('rules.actions.add')}
        </Button>
      </div>
    </div>
  );
}

/**
 * The rules of a draft version, edited as a set and saved under the version's ETag.
 * Conditions are CEL (ADR-023): a monospaced field, no autocorrect and no visual builder,
 * because an operator writing rules is a trained user and the compiler's own message is
 * more use than anything a half-built builder could say.
 */
export function RulesEditor({ rules, onSave, saving, problem }: RulesEditorProps) {
  const { t } = useTranslation();
  const form = useForm<RulesFormValues>({
    resolver: zodResolver(rulesFormSchema),
    defaultValues: { items: rules.length > 0 ? rules.map(toRuleRow) : [emptyRuleRow] },
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

  async function submit(values: RulesFormValues) {
    await onSave(values.items.map(toRuleInput));
  }

  return (
    <form
      onSubmit={form.handleSubmit(submit)}
      className="grid gap-4"
      noValidate
      data-testid="rules-form"
    >
      <ProblemAlert problem={problem} hideFieldErrors className="mb-1" />
      <p className="text-fg-muted text-sm">{t('rules.items.priorityHint')}</p>
      <p className="text-fg-muted text-sm">{t('rules.items.conditionHint')}</p>

      {rows.fields.map((field, index) => {
        const row = watched[index];
        const errors = form.formState.errors.items?.[index];
        return (
          <fieldset
            key={field.id}
            className="border-line grid gap-3 rounded-md border p-4"
            data-testid={`rule-row-${index}`}
          >
            <legend className="text-fg-muted px-1 text-xs font-medium">
              {row?.code || t('rules.items.columns.code')}
            </legend>
            <div className="grid gap-3 md:grid-cols-4">
              <FormField
                label={t('rules.items.fields.priority')}
                required
                requiredLabel={t('common.requiredMark')}
                error={message(errors?.priority, index, 'priority')}
              >
                <Input
                  {...form.register(`items.${index}.priority`)}
                  inputMode="numeric"
                  autoComplete="off"
                  className="text-right font-mono"
                />
              </FormField>
              <FormField
                label={t('rules.items.fields.code')}
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
                label={t('rules.items.fields.name')}
                required
                requiredLabel={t('common.requiredMark')}
                error={message(errors?.name, index, 'name')}
              >
                <Input {...form.register(`items.${index}.name`)} />
              </FormField>
              <FormField
                label={t('rules.items.fields.explanationCode')}
                required
                requiredLabel={t('common.requiredMark')}
                error={message(errors?.explanationCode, index, 'explanationCode')}
              >
                <Input
                  {...form.register(`items.${index}.explanationCode`)}
                  autoComplete="off"
                  className="font-mono"
                />
              </FormField>
            </div>

            <FormField
              label={t('rules.items.fields.condition')}
              required
              requiredLabel={t('common.requiredMark')}
              error={message(errors?.condition, index, 'condition')}
            >
              <Textarea
                {...form.register(`items.${index}.condition`)}
                rows={2}
                spellCheck={false}
                autoComplete="off"
                autoCorrect="off"
                autoCapitalize="off"
                className="font-mono"
              />
            </FormField>

            <ActionsField form={form} index={index} />

            <div className="flex flex-wrap items-center gap-6 text-sm">
              <label className="flex items-center gap-2">
                <input type="checkbox" {...form.register(`items.${index}.stopOnMatch`)} />
                {t('rules.items.fields.stopOnMatch')}
              </label>
              <label className="flex items-center gap-2">
                <input type="checkbox" {...form.register(`items.${index}.active`)} />
                {t('rules.items.fields.active')}
              </label>
              <Button
                variant="ghost"
                size="sm"
                className="ml-auto"
                onClick={() => rows.remove(index)}
                disabled={rows.fields.length === 1}
              >
                {t('rules.items.remove')}
              </Button>
            </div>
          </fieldset>
        );
      })}

      <div className="flex justify-between gap-2">
        <Button
          variant="secondary"
          onClick={() => rows.append({ ...emptyRuleRow, priority: nextPriority(watched) })}
        >
          {t('rules.items.add')}
        </Button>
        <Button type="submit" loading={saving}>
          {t('common.save')}
        </Button>
      </div>
    </form>
  );
}

/** Read-only view of the rules of a version nobody may change any more. */
export function RulesTable({ rules }: { rules: Rule[] }) {
  const { t } = useTranslation();
  if (rules.length === 0) {
    return <p className="text-fg-muted text-sm">{t('rules.items.empty')}</p>;
  }
  return (
    <Table data-testid="rule-table">
      <THead>
        <TR>
          <TH>{t('rules.items.columns.priority')}</TH>
          <TH>{t('rules.items.columns.code')}</TH>
          <TH>{t('rules.items.columns.condition')}</TH>
          <TH>{t('rules.items.columns.actions')}</TH>
          <TH>{t('rules.items.columns.explanation')}</TH>
          <TH>{t('rules.items.fields.stopOnMatch')}</TH>
        </TR>
      </THead>
      <TBody>
        {rules.map((rule) => (
          <TR key={rule.id}>
            <TD className="text-right font-mono">{rule.priority}</TD>
            <TD>
              <code className="font-mono text-xs">{rule.code}</code>
              <span className="text-fg-muted block text-xs">{rule.name}</span>
            </TD>
            <TD>
              <code className="font-mono text-xs break-all">{rule.condition}</code>
            </TD>
            <TD>
              <span className="flex flex-wrap gap-1">
                {rule.actions.map((action, i) => (
                  <Badge key={`${action.type}-${i}`}>{t(`rules.actions.${action.type}`)}</Badge>
                ))}
              </span>
            </TD>
            <TD>
              <code className="font-mono text-xs">{rule.explanationCode}</code>
            </TD>
            <TD>{rule.stopOnMatch ? t('common.yes') : t('common.no')}</TD>
          </TR>
        ))}
      </TBody>
    </Table>
  );
}
