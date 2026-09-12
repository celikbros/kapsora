import { useTranslation } from '@kapsora/i18n';
import type { ReactNode } from 'react';

import type { AppKind } from './AppChooser';
import { cn } from './cn';

const APPS: readonly AppKind[] = ['backoffice', 'provider', 'member'];

export interface SignInLayoutProps {
  /** The app this sign-in screen belongs to; it is the one marked in the list. */
  app: AppKind;
  /** The sign-in card. */
  children: ReactNode;
}

/**
 * The way into KAPSORA: what this is on the left, the way in on the right.
 *
 * The panel is plain on the page ground and the card is the only raised surface, because a
 * ledger introduces itself by saying what it holds rather than by decorating the door: no
 * gradient, no illustration, no hero number. It names the three apps and marks the one this
 * screen belongs to, so a person handed here by the single sign-in can see where they are
 * before they type anything.
 *
 * Both columns start at the same top edge. The card grows with the demo list and the panel
 * does not, so centring them against each other would push the product name half a screen
 * down and leave the first thing a person reads below a void.
 *
 * Narrow screens keep the answer and drop the rest: only the app this screen belongs to
 * stays, with its mark. The member app's first viewport is a 390px phone, so the sentence
 * "which app am I in" may not be the one that falls off the bottom.
 */
export function SignInLayout({ app, children }: SignInLayoutProps) {
  const { t } = useTranslation();
  return (
    <main id="main" className="flex min-h-dvh items-center justify-center p-4 sm:p-6 lg:p-10">
      <div className="mx-auto grid w-full max-w-sm items-start gap-10 lg:max-w-5xl lg:grid-cols-[minmax(0,1fr)_24rem] lg:gap-16">
        <section className="min-w-0">
          <h1 className="text-fg text-2xl font-semibold tracking-tight lg:text-3xl">
            {t('app.name')}
          </h1>
          <p className="text-fg-muted mt-1 text-sm">{t('auth.tagline')}</p>
          <p className="text-fg mt-5 max-w-[58ch] text-base leading-7">{t('auth.welcome')}</p>
          <dl className="border-line divide-line mt-8 border-y lg:divide-y lg:border-b-0">
            {APPS.map((one) => (
              <div
                key={one}
                className={cn(
                  'items-baseline justify-between gap-6 py-3',
                  one === app ? 'flex' : 'hidden lg:flex',
                )}
              >
                <dt className="text-fg min-w-0 text-sm font-medium">
                  {t(`auth.apps.${one}`)}
                  {one === app ? (
                    <span className="text-primary ml-3 text-xs font-medium">
                      {t('auth.youAreHere')}
                    </span>
                  ) : null}
                </dt>
                <dd className="text-fg-muted hidden max-w-[38ch] text-sm lg:block">
                  {t(`auth.appLines.${one}`)}
                </dd>
              </div>
            ))}
          </dl>
        </section>
        <div className="min-w-0">{children}</div>
      </div>
    </main>
  );
}
