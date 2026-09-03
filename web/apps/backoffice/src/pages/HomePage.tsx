import { useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { Card, PageHeader } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { NAV_ENTRIES } from '../nav';

export function HomePage() {
  const { t } = useTranslation();
  const me = useSession((s) => s.me);
  const active = useSession((s) => s.activeTenant);
  return (
    <>
      <PageHeader
        title={t('nav.home')}
        {...(me ? { description: `${me.displayName} · ${active?.tenant.displayName ?? ''}` } : {})}
      />
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {NAV_ENTRIES.filter(
          (e) => e.key !== 'home' && (!e.permission || active?.permissions.includes(e.permission)),
        ).map((entry) => (
          <Card key={entry.key} className="p-4">
            <Link to={entry.path} className="font-medium underline-offset-2 hover:underline">
              {t(entry.labelKey)}
            </Link>
            <p className="text-fg-muted mt-1 text-xs">
              {entry.implemented ? '' : t('app.soonTitle')}
            </p>
          </Card>
        ))}
      </div>
    </>
  );
}
