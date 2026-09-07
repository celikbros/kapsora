import { useTranslation } from '@kapsora/i18n';
import { Spinner } from '@kapsora/ui';

/** A loading state with the section's own words, never a bare spinner beside settled content. */
export function Loading() {
  const { t } = useTranslation();
  return (
    <p className="text-fg-muted mt-2 flex items-center gap-2 text-sm" aria-busy="true">
      <Spinner /> {t('common.loading')}
    </p>
  );
}
