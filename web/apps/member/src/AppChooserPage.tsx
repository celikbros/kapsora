import { appsFor, browser, safeReturnTo, useSession, useSessionStore } from '@kapsora/auth';
import { AppChooser } from '@kapsora/ui';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { useEffect, useState } from 'react';
import { APP_URLS } from './appUrls';

/**
 * The single sign-in's chooser. The sign-in screen sends an account with work in several apps
 * here, and requireTenant sends one with no work in this app. It continues here when the
 * account has work here, hands the person to their only other app with a page load, lists the
 * apps when there are several, and says so when there are none. It reads nothing but the
 * account's own context.
 */
export function AppChooserPage() {
  const store = useSessionStore();
  const navigate = useNavigate();
  const search = useSearch({ from: '/auth/apps' });
  const me = useSession((s) => s.me);
  const [signingOut, setSigningOut] = useState(false);
  const fits = appsFor((me?.tenants ?? []).filter((t) => t.tenant.status === 'ACTIVE'));
  const here = fits.includes('member');
  const only = !here && fits.length === 1 ? fits[0]! : null;

  useEffect(() => {
    if (only) browser.assign(APP_URLS[only]);
  }, [only]);

  function signOut() {
    setSigningOut(true);
    void store.logout().finally(() => navigate({ href: '/auth/login' }));
  }

  return (
    <AppChooser
      current="member"
      fits={fits}
      urls={APP_URLS}
      forwarding={only !== null}
      {...(here
        ? { onContinue: () => void navigate({ href: safeReturnTo(search.returnTo, '/') }) }
        : {})}
      onSignOut={signOut}
      signingOut={signingOut}
    />
  );
}
