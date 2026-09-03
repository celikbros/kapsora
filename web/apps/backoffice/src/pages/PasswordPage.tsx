import { useSessionStore } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { Button, Card } from '@kapsora/ui';
import { useNavigate } from '@tanstack/react-router';

/**
 * Shown when the session says mustChangePassword. The change-password form itself
 * arrives with the account screens; until then the user can sign out.
 */
export function PasswordPage() {
  const { t } = useTranslation();
  const store = useSessionStore();
  const navigate = useNavigate();
  return (
    <main id="main" className="flex min-h-dvh items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <h1 className="text-xl font-semibold">{t('auth.password')}</h1>
        <p className="mt-2 text-sm">{t('auth.mustChangePassword')}</p>
        <Button
          className="mt-6"
          variant="secondary"
          onClick={() => {
            void store.logout().finally(() => navigate({ to: '/auth/logout' }));
          }}
        >
          {t('auth.logout')}
        </Button>
      </Card>
    </main>
  );
}
