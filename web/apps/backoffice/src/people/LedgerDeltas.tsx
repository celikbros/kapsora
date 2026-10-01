import type { LedgerEntry } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';

const fields = [
  ['deltaTotal', 'entitlements.columns.total'],
  ['deltaAvailable', 'entitlements.columns.available'],
  ['deltaReserved', 'entitlements.columns.reserved'],
  ['deltaConsumed', 'entitlements.columns.consumed'],
  ['deltaExpired', 'entitlements.columns.expired'],
] as const;

/** A movement may transfer balance between buckets without changing the total. */
export function LedgerDeltas({ entry }: { entry: LedgerEntry }) {
  const { t } = useTranslation();
  const changed = fields.filter(([field]) => !/^-?0+(?:\.0+)?$/.test(entry[field]));
  if (changed.length === 0) return <span className="font-mono">0</span>;
  return (
    <dl className="grid gap-0.5 text-xs">
      {changed.map(([field, label]) => (
        <div key={field} className="flex flex-wrap gap-x-1.5">
          <dt className="text-fg-muted">{t(label)}:</dt>
          <dd className="font-mono">{entry[field]}</dd>
        </div>
      ))}
    </dl>
  );
}
