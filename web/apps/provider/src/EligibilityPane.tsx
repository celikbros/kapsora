import type { EligibilityCheckResult } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';
import { Badge, HelpHint, ProblemAlert, Spinner } from '@kapsora/ui';

import { problemOf } from './problems';

type Verdict = 'eligible' | 'review' | 'notEligible';

/**
 * Three answers, not two. A service the catalog has not mapped to an entitlement yet is
 * REVIEW_REQUIRED on its line and `eligible: false` at the top — which is "somebody will
 * look", and reads as a refusal only if the screen paints it red.
 */
function verdict(result: EligibilityCheckResult): Verdict {
  if (result.eligible) return 'eligible';
  const hardStop = result.explanations.some((e) => e.severity === 'ERROR');
  const review = (result.items ?? []).some((i) => i.outcome === 'REVIEW_REQUIRED');
  return (review || result.outcome === 'REVIEW_REQUIRED') && !hardStop ? 'review' : 'notEligible';
}
function verdictKey(v: Verdict): string {
  return v === 'review' ? 'reviewRequired' : v;
}

/** One line per distinct reason: the same code with the same words said twice says nothing more. */
function unique<T extends { code: string; message: string }>(items: T[]): T[] {
  const seen = new Set<string>();
  return items.filter((e) => {
    const key = `${e.code}|${e.message}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

export interface LiveCheck {
  /** False while the form cannot be asked yet. */
  asked: boolean;
  isPending: boolean;
  error: unknown;
  data: EligibilityCheckResult | undefined;
}

/**
 * The answer, next to the question. Empty until the form can be asked; then the verdict
 * and every explanation the server gave, in the server's own words — never a number the
 * screen computed itself. A check is information, not a decision: the note says so.
 */
export function EligibilityPane({ check }: { check: LiveCheck }) {
  const { t } = useTranslation();
  // Reasons already said at the top are not said again under a line.
  const shown = new Set((check.data?.explanations ?? []).map((e) => `${e.code}|${e.message}`));
  return (
    <aside
      aria-labelledby="eligibility-pane"
      aria-live="polite"
      className="bg-surface-raised border-line rounded-md border p-4"
      data-testid="eligibility-pane"
    >
      {/* The mark sits beside the heading, not inside it: the aside is named by this h2. */}
      <div className="flex items-center gap-1.5">
        <h2 id="eligibility-pane" className="text-base font-semibold">
          {t('provider.newRequest.resultTitle')}
        </h2>
        <HelpHint term="uygunluk" />
      </div>
      {!check.asked ? (
        <p className="text-fg-muted mt-2 text-sm">{t('provider.newRequest.resultEmpty')}</p>
      ) : check.isPending ? (
        <p className="text-fg-muted mt-2 flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('provider.newRequest.resultChecking')}
        </p>
      ) : check.error ? (
        <ProblemAlert problem={problemOf(check.error)} className="mt-2" />
      ) : check.data ? (
        <div className="mt-2 grid gap-3 text-sm">
          <Badge
            tone={
              verdict(check.data) === 'eligible'
                ? 'success'
                : verdict(check.data) === 'review'
                  ? 'warning'
                  : 'danger'
            }
          >
            {t(`provider.newRequest.${verdictKey(verdict(check.data))}`)}
          </Badge>
          {check.data.explanations.length > 0 ? (
            <ul className="grid gap-1">
              {unique(check.data.explanations).map((e, i) => (
                <li
                  key={i}
                  className={
                    e.severity === 'ERROR'
                      ? 'text-danger'
                      : e.severity === 'WARNING'
                        ? 'text-warning'
                        : undefined
                  }
                >
                  <code className="mr-2 font-mono text-xs">{e.code}</code>
                  {e.message}
                </li>
              ))}
            </ul>
          ) : null}
          {(check.data.items ?? []).map((item, i) => (
            <div key={i} className="border-line border-t pt-2">
              {item.entitlementCode ? (
                <p>
                  <code className="font-mono text-xs">{item.entitlementCode}</code>
                  {item.availableQuantity ? (
                    <span className="ml-2 font-mono text-xs tabular-nums">
                      {item.availableQuantity}
                    </span>
                  ) : null}
                </p>
              ) : null}
              {unique(item.explanations)
                .filter((e) => !shown.has(`${e.code}|${e.message}`))
                .map((e, j) => (
                  <p key={j} className={e.severity === 'ERROR' ? 'text-danger' : undefined}>
                    <code className="mr-2 font-mono text-xs">{e.code}</code>
                    {e.message}
                  </p>
                ))}
            </div>
          ))}
          <p className="text-fg-muted text-xs">{t('provider.newRequest.resultNote')}</p>
        </div>
      ) : null}
    </aside>
  );
}
