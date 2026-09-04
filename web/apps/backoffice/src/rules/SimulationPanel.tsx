import type { RuleInputSchema } from '@kapsora/api-client';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  FormField,
  ProblemAlert,
  TBody,
  TD,
  TH,
  THead,
  TR,
  Table,
  Textarea,
} from '@kapsora/ui';
import { zodResolver } from '@hookform/resolvers/zod';
import { useState } from 'react';
import { useForm } from 'react-hook-form';

import { parseIssueMessage, problemOf } from '../problems';
import { useRuleSimulation } from './queries';
import { parseJsonObject, simulationFormSchema, type SimulationFormValues } from './schema';

/** A blank input document keyed by the variables this version declares. */
function inputTemplate(inputSchema: RuleInputSchema): string {
  const draft: Record<string, unknown> = {};
  for (const [name, type] of Object.entries(inputSchema)) {
    // Numbers travel as exact decimal text, so the template offers text everywhere but
    // for the two types where a JSON literal is the only honest value.
    draft[name] = type === 'bool' ? false : type === 'list' ? [] : type === 'map' ? {} : '';
  }
  return JSON.stringify(draft, null, 2);
}

export interface SimulationPanelProps {
  ruleSetVersionId: string;
  inputSchema: RuleInputSchema;
}

/**
 * One input run against this version. The engine returns actions and performs none of
 * them, so a simulation records no evaluation, moves no balance and reserves nothing —
 * which is what lets an author run it as often as they like.
 */
export function SimulationPanel({ ruleSetVersionId, inputSchema }: SimulationPanelProps) {
  const { t } = useTranslation();
  const simulate = useRuleSimulation(ruleSetVersionId);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const form = useForm<SimulationFormValues>({
    resolver: zodResolver(simulationFormSchema),
    defaultValues: { input: inputTemplate(inputSchema) },
    mode: 'onBlur',
  });

  function message(error: { message?: string } | undefined): string | undefined {
    if (!error?.message) return undefined;
    const { code, params } = parseIssueMessage(error.message);
    return fieldErrorMessage(t, code, undefined, params);
  }

  async function run(values: SimulationFormValues) {
    setProblem(null);
    try {
      await simulate.mutateAsync({ input: parseJsonObject(values.input) });
    } catch (err) {
      setProblem(problemOf(err));
    }
  }

  const trace = simulate.data;

  return (
    <div className="grid gap-4" data-testid="simulation-panel">
      <p className="text-fg-muted text-sm">{t('rules.simulation.intro')}</p>
      <ProblemAlert problem={problem} />

      <form onSubmit={form.handleSubmit(run)} className="grid gap-3" noValidate>
        <FormField
          label={t('rules.simulation.input')}
          required
          requiredLabel={t('common.requiredMark')}
          error={message(form.formState.errors.input)}
        >
          <Textarea
            {...form.register('input')}
            rows={6}
            spellCheck={false}
            autoComplete="off"
            className="font-mono"
          />
        </FormField>
        <div className="flex justify-end">
          <Button type="submit" loading={simulate.isPending}>
            {t('rules.simulation.run')}
          </Button>
        </div>
      </form>

      {!trace ? (
        <p className="text-fg-muted text-sm">{t('rules.simulation.empty')}</p>
      ) : (
        <div className="grid gap-2">
          <div className="flex items-center gap-2">
            <span className="text-fg text-sm font-medium">{t('rules.simulation.trace')}</span>
            <Badge tone={trace.outcome === 'APPROVED' ? 'success' : 'warning'}>
              {t(`rules.outcomes.${trace.outcome}`)}
            </Badge>
          </div>
          <Table data-testid="simulation-trace">
            <THead>
              <TR>
                <TH>{t('rules.simulation.columns.sequence')}</TH>
                <TH>{t('rules.simulation.columns.rule')}</TH>
                <TH>{t('rules.simulation.columns.matched')}</TH>
                <TH>{t('rules.simulation.columns.action')}</TH>
                <TH>{t('rules.simulation.columns.explanation')}</TH>
              </TR>
            </THead>
            <TBody>
              {trace.results.map((line) => (
                <TR key={`${line.sequence}-${line.ruleCode}`}>
                  <TD className="text-right font-mono">{line.sequence}</TD>
                  <TD>
                    <code className="font-mono text-xs">{line.ruleCode}</code>
                  </TD>
                  <TD>{line.matched ? t('common.yes') : t('common.no')}</TD>
                  <TD>
                    {line.actionType ? t(`rules.actions.${line.actionType}`) : t('common.none')}
                  </TD>
                  <TD>
                    <code className="font-mono text-xs">{line.explanationCode}</code>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
    </div>
  );
}
