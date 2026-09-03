import { fieldErrorMessage, problemMessage, useTranslation } from '@kapsora/i18n';
import { useState, type ReactNode } from 'react';
import { cn } from './cn';

/** Structural subset of the contract's Problem; kept local so ui does not import api-client. */
export interface ProblemLike {
  code: string;
  title: string;
  status: number;
  detail?: string;
  traceId: string;
  errors?: Array<{ field: string; code: string; message?: string }>;
}

export interface ProblemAlertProps {
  problem: ProblemLike | null | undefined;
  /** Field errors already shown next to inputs are hidden from the list. */
  hideFieldErrors?: boolean;
  actions?: ReactNode;
  className?: string;
}

/**
 * Renders a problem+json document: Turkish message by code, server detail when present,
 * field errors, and the trace id for support (WP-I1-05 section 4.6).
 */
export function ProblemAlert({
  problem,
  hideFieldErrors = false,
  actions,
  className,
}: ProblemAlertProps) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  if (!problem) return null;

  const message = problemMessage(t, problem.code, problem.title);
  const tone =
    problem.status >= 500 || problem.status === 0
      ? 'bg-danger-soft border-danger'
      : 'bg-warning-soft border-warning';

  async function copy() {
    try {
      await navigator.clipboard.writeText(problem!.traceId);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard may be unavailable; the id is still visible.
    }
  }

  return (
    <div
      role="alert"
      className={cn('text-fg rounded-md border-l-4 p-3 text-sm', tone, className)}
      data-problem-code={problem.code}
    >
      <p className="font-medium">{message}</p>
      {problem.detail ? <p className="mt-1">{problem.detail}</p> : null}
      {!hideFieldErrors && problem.errors && problem.errors.length > 0 ? (
        <ul className="mt-2 list-disc pl-5">
          {problem.errors.map((e, i) => (
            <li key={`${e.field}-${i}`}>
              <span className="font-mono text-xs">{e.field}</span>:{' '}
              {fieldErrorMessage(t, e.code, e.message)}
            </li>
          ))}
        </ul>
      ) : null}
      <div className="mt-2 flex flex-wrap items-center gap-3">
        {problem.traceId ? (
          <span className="text-fg-muted text-xs">
            {t('common.traceId')}: <code className="font-mono">{problem.traceId}</code>{' '}
            <button type="button" className="underline" onClick={copy}>
              {copied ? t('common.copied') : t('common.copy')}
            </button>
          </span>
        ) : null}
        {actions}
      </div>
    </div>
  );
}
