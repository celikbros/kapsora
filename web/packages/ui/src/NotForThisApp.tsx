import { useTranslation } from '@kapsora/i18n';

import { Button } from './Button';
import { Card } from './PageShell';

/** The three KAPSORA apps, named the way the auth package names them. */
export type AppKind = 'backoffice' | 'provider' | 'member';

export interface NotForThisAppProps {
  /** The app the person opened. */
  current: AppKind;
  /** The apps the account does have work in; empty when it has none anywhere. */
  fits: readonly AppKind[];
  onSignOut: () => void;
  signingOut?: boolean;
}

/**
 * The page an account lands on when it signed in to an app it has no role in. It says so in
 * one sentence, names the apps that account can use, and offers the one useful action here:
 * signing in as somebody else. It shows nothing of the account's data, because the app it is
 * in has none for it.
 */
export function NotForThisApp({
  current,
  fits,
  onSignOut,
  signingOut = false,
}: NotForThisAppProps) {
  const { t } = useTranslation();
  const name = (app: AppKind) => t(`auth.apps.${app}`);
  return (
    <main id="main" className="flex min-h-dvh items-center justify-center p-4">
      <Card className="w-full max-w-md">
        <h1 className="text-xl font-semibold text-balance">
          {t('auth.notForAppTitle', { app: name(current) })}
        </h1>
        <p className="text-fg-muted mt-2 text-sm">{t('auth.notForAppBody')}</p>
        {fits.length > 0 ? (
          <div className="mt-5">
            <p className="text-sm font-medium">{t('auth.notForAppFits')}</p>
            <ul className="mt-2 grid gap-1.5" data-testid="not-for-app-fits">
              {fits.map((app) => (
                <li
                  key={app}
                  className="border-line bg-surface-sunken rounded-md border px-3 py-2 text-sm"
                >
                  {name(app)}
                </li>
              ))}
            </ul>
          </div>
        ) : (
          <p className="text-fg-muted mt-5 text-sm">{t('auth.notForAppNone')}</p>
        )}
        <div className="mt-6">
          <Button variant="secondary" loading={signingOut} onClick={onSignOut}>
            {t('auth.notForAppSignOut')}
          </Button>
        </div>
      </Card>
    </main>
  );
}
