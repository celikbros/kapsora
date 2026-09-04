import type { Problem } from '@kapsora/api-client';
import { fieldErrorMessage, useTranslation } from '@kapsora/i18n';
import { useEffect } from 'react';
import type { FieldValues, Path, UseFormReturn } from 'react-hook-form';

import { parseIssueMessage } from '../problems';

/**
 * Puts the server's 422 field errors back on the fields they name. `items[0].code` is the
 * wire shape and `items.0.code` is the form path, so the indexes are rewritten on the way
 * in. The code travels with the server's own message behind a pipe, so a message the
 * catalog has no key for is still shown rather than swallowed.
 */
export function useServerFieldErrors<T extends FieldValues>(
  form: UseFormReturn<T>,
  problem: Problem | null,
): void {
  useEffect(() => {
    if (!problem?.errors) return;
    for (const error of problem.errors) {
      const path = error.field.replace(/\[(\d+)\]/g, '.$1') as Path<T>;
      form.setError(path, {
        type: 'server',
        message: error.message ? `${error.code}|${error.message}` : error.code,
      });
    }
  }, [problem, form]);
}

/** Turns a form error — client issue or server code — into the Turkish message for it. */
export function useFieldMessage(): (error: { message?: string } | undefined) => string | undefined {
  const { t } = useTranslation();
  return (error) => {
    if (!error?.message) return undefined;
    const [codePart, serverMessage] = error.message.split('|');
    const { code, params } = parseIssueMessage(codePart ?? '');
    return fieldErrorMessage(t, code, serverMessage, params);
  };
}

/** Today as an ISO calendar date; the default `asOf` and the default period start. */
export function todayIso(): string {
  return new Date().toISOString().slice(0, 10);
}
