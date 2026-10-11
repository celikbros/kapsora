import { useSession } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { EmptyState, PageHeader } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { canDiscoverWallets } from '../nav';

/** Member discovery for the existing per-person entitlement accounts and ledger. */
export function WalletsPage() {
  const { t } = useTranslation();
  const active = useSession((state) => state.activeTenant);
  const allowed = canDiscoverWallets(active?.permissions ?? []);

  return (
    <section data-testid="wallets-services">
      <PageHeader title={t('walletsServices.title')} description={t('walletsServices.intro')} />
      {allowed ? (
        <ul className="divide-line divide-y rounded-lg border border-line bg-surface">
          <li className="px-4 py-3 sm:px-5">
            <div className="text-sm font-medium">
              <Link
                to="/people"
                search={{}}
                data-testid="wallets-people"
                className="text-primary hover:underline"
              >
                {t('walletsServices.people')}
              </Link>
            </div>
            <p className="text-fg-muted mt-1 text-sm">{t('walletsServices.peopleDescription')}</p>
          </li>
        </ul>
      ) : (
        <EmptyState title={t('problems.PERMISSION_DENIED')} />
      )}
    </section>
  );
}
