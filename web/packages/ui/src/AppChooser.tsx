import { useTranslation } from '@kapsora/i18n';

import { Button } from './Button';
import { Card } from './PageShell';

/** The three KAPSORA apps, named the way the auth package names them. */
export type AppKind = 'backoffice' | 'provider' | 'member';

export interface AppChooserProps {
  /** The app the person is in. */
  current: AppKind;
  /** The apps the account has work in; empty when it has none anywhere. */
  fits: readonly AppKind[];
  /** Where each app lives. */
  urls: Readonly<Record<AppKind, string>>;
  /** Continue in the current app; given only when the account has work here. */
  onContinue?: () => void;
  /** True while the page is handing the person to their only app. */
  forwarding?: boolean;
  onSignOut: () => void;
  signingOut?: boolean;
}

/**
 * The single sign-in's one page of its own. An account with work in several apps chooses
 * where to go, the app it is in first when it has work there; an account whose only app is
 * another one is told it is being taken there, with the link in case the browser does not
 * follow; an account with no app anywhere is told to see its administrator. Every state
 * offers signing in as somebody else, and none of them shows any data.
 */
export function AppChooser({
  current,
  fits,
  urls,
  onContinue,
  forwarding = false,
  onSignOut,
  signingOut = false,
}: AppChooserProps) {
  const { t } = useTranslation();
  const name = (app: AppKind) => t(`auth.apps.${app}`);
  const here = onContinue !== undefined && fits.includes(current);
  const ordered = here ? [current, ...fits.filter((a) => a !== current)] : fits;
  const only = forwarding && fits.length === 1 ? fits[0]! : null;

  let title: string;
  let intro: string;
  if (fits.length === 0) {
    title = t('auth.noAppTitle');
    intro = t('auth.notForAppNone');
  } else if (only) {
    title = t('auth.forwardingTitle', { app: name(only) });
    intro = t('auth.forwardingBody');
  } else if (here) {
    title = t('auth.chooseTitle');
    intro = t('auth.chooseIntro');
  } else {
    title = t('auth.notForAppTitle');
    intro = t('auth.notForAppBody');
  }

  return (
    <main id="main" className="flex min-h-dvh items-center justify-center p-4">
      <Card className="w-full max-w-md">
        <h1 className="text-xl font-semibold text-balance">{title}</h1>
        <p className="text-fg-muted mt-2 text-sm">{intro}</p>
        {ordered.length > 0 ? (
          <ul className="mt-5 grid gap-2" data-testid="app-choices">
            {ordered.map((app) => (
              <li
                key={app}
                className="border-line flex items-center justify-between gap-3 rounded-md border px-3 py-2.5"
              >
                <span className="text-sm font-medium">{name(app)}</span>
                {here && app === current ? (
                  <Button size="sm" onClick={onContinue}>
                    {t('auth.continueHere')}
                  </Button>
                ) : (
                  <a
                    href={urls[app]}
                    className="border-line hover:bg-surface-sunken focus-visible:outline-primary rounded-md border px-3 py-1.5 text-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-2"
                  >
                    {t('auth.openApp')}
                  </a>
                )}
              </li>
            ))}
          </ul>
        ) : null}
        <div className="mt-6">
          <Button variant="secondary" loading={signingOut} onClick={onSignOut}>
            {t('auth.notForAppSignOut')}
          </Button>
        </div>
      </Card>
    </main>
  );
}
