import { useTranslation } from '@kapsora/i18n';
import { Card } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';

export function LogoutPage() {
  const { t } = useTranslation();
  return (
    <main id="main" className="flex min-h-dvh items-center justify-center p-4">
      <Card className="w-full max-w-sm text-center">
        <h1 className="text-xl font-semibold">{t('auth.loggedOutTitle')}</h1>
        <p className="text-fg-muted mt-1 text-sm">{t('auth.loggedOutBody')}</p>
        <Link
          to="/auth/login"
          search={{}}
          className="bg-primary text-primary-fg hover:bg-primary-strong mt-6 inline-flex h-10 items-center rounded-md px-4 text-sm font-medium"
        >
          {t('auth.loginAgain')}
        </Link>
      </Card>
    </main>
  );
}
