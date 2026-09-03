import { useTranslation } from '@kapsora/i18n';
import { EmptyState, PageHeader } from '@kapsora/ui';
import { useRouterState } from '@tanstack/react-router';
import { NAV_ENTRIES } from '../nav';

/** Placeholder for navigation entries whose screens come in later increments. */
export function SoonPage() {
  const { t } = useTranslation();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const entry = NAV_ENTRIES.find((e) => e.path !== '/' && pathname.startsWith(e.path));
  return (
    <>
      <PageHeader title={entry ? t(entry.labelKey) : t('app.soonTitle')} />
      <EmptyState title={t('app.soonTitle')} description={t('app.soonBody')} />
    </>
  );
}
