/** Exact amounts and UTC day boundaries for the read-only PC05 calendar check. */
export function micros(value: string): bigint {
  if (!/^-?\d+(?:\.\d{1,6})?$/.test(value)) throw new Error('Invalid exact decimal');
  const negative = value.startsWith('-');
  const [whole, fraction = ''] = value.replace(/^-/, '').split('.');
  const result = BigInt(whole!) * 1_000_000n + BigInt(fraction.padEnd(6, '0'));
  return negative ? -result : result;
}

export function eligibleUtcDay(dueDate: string): string {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(dueDate)) throw new Error('Invalid due date');
  const date = new Date(`${dueDate}T00:00:00.000Z`);
  if (Number.isNaN(date.valueOf()) || date.toISOString().slice(0, 10) !== dueDate)
    throw new Error('Invalid due date');
  date.setUTCDate(date.getUTCDate() + 1);
  return date.toISOString().slice(0, 10);
}

export function calendarState(now: Date, dueDate: string): 'before' | 'eligible-day' | 'after' {
  const day = now.toISOString().slice(0, 10);
  const eligible = eligibleUtcDay(dueDate);
  if (day < eligible) return 'before';
  return day === eligible ? 'eligible-day' : 'after';
}

type Window = { startedAt: string; finishedAt: string };
type Run = {
  scope: 'PROVIDER' | 'TENANT';
  providerOrganizationId?: string | null;
  currencyCode: string;
  periodFrom: string;
  periodTo: string;
  status: string;
  ranAt: string;
};

/** The run-to-job relationship is an observed time interval, not a database foreign key. */
export function matchingRunPair<T extends Run>(
  providerRuns: T[],
  tenantRuns: T[],
  windows: Window[],
  target: { providerOrganizationId: string; currencyCode: string; dueDate: string },
): { own: T; tenant: T } | undefined {
  const inWindow = (run: T, window: Window) => {
    const at = Date.parse(run.ranAt);
    const start = Date.parse(window.startedAt);
    const finish = Date.parse(window.finishedAt);
    return (
      Number.isFinite(at) &&
      Number.isFinite(start) &&
      Number.isFinite(finish) &&
      at >= start &&
      at <= finish
    );
  };
  const matches = (run: T, window: Window) =>
    inWindow(run, window) &&
    run.currencyCode === target.currencyCode &&
    run.periodFrom === target.dueDate &&
    run.periodTo === target.dueDate &&
    (run.status === 'BALANCED' || run.status === 'DIFFERENCES');
  for (const window of windows) {
    const own = providerRuns.find(
      (run) =>
        matches(run, window) &&
        run.scope === 'PROVIDER' &&
        run.providerOrganizationId === target.providerOrganizationId,
    );
    const tenant = tenantRuns.find(
      (run) => matches(run, window) && run.scope === 'TENANT' && !run.providerOrganizationId,
    );
    if (own && tenant) return { own, tenant };
  }
  return undefined;
}
