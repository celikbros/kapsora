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

/** Refusals a retry cannot change: the account may not read this at all. */
const FORBIDDEN = new Set(['PERMISSION_DENIED', 'TENANT_ACCESS_DENIED']);

export interface ProblemAlertProps {
  problem: ProblemLike | null | undefined;
  /**
   * The problem is why the page itself could not load. A refusal then says the page cannot be
   * shown and offers the app's home instead of `actions`: retrying a refusal changes nothing,
   * and the usual way to meet one is a page left open while the tab changed accounts.
   */
  page?: boolean;
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
  page = false,
}: ProblemAlertProps) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  if (!problem) return null;

  const forbiddenPage = page && FORBIDDEN.has(problem.code);
  const message = forbiddenPage
    ? t('problems.PAGE_FORBIDDEN')
    : problemMessage(t, problem.code, problem.title);
  const detail = forbiddenPage ? t('problems.PAGE_FORBIDDEN_DETAIL') : problem.detail;
  const tone =
    problem.status >= 500 || problem.status === 0
      ? 'bg-danger-soft border-danger/40'
      : 'bg-warning-soft border-warning/40';

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
      className={cn('text-fg rounded-md border p-3 text-sm', tone, className)}
      data-problem-code={problem.code}
    >
      <p className="font-medium">{message}</p>
      {detail ? <p className="mt-1">{detail}</p> : null}
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
        {forbiddenPage ? (
          <a
            href={import.meta.env.BASE_URL}
            className="border-line bg-surface-raised hover:bg-surface-sunken focus-visible:outline-primary rounded-md border px-3 py-1.5 text-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-2"
          >
            {t('common.home')}
          </a>
        ) : (
          actions
        )}
      </div>
    </div>
  );
}
