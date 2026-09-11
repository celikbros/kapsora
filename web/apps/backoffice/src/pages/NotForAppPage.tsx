import { appsFor, useSession, useSessionStore } from '@kapsora/auth';
import { NotForThisApp } from '@kapsora/ui';
import { useNavigate } from '@tanstack/react-router';
import { useState } from 'react';

/**
 * Where requireTenant sends an account that has no work in this app: it names the apps the
 * account does have work in, from the same rule the guard used, and offers signing in as
 * somebody else. It reads nothing but the account's own context.
 */
export function NotForAppPage() {
  const store = useSessionStore();
  const navigate = useNavigate();
  const me = useSession((s) => s.me);
  const [signingOut, setSigningOut] = useState(false);
  const fits = appsFor((me?.tenants ?? []).filter((t) => t.tenant.status === 'ACTIVE'));

  function signOut() {
    setSigningOut(true);
    void store.logout().finally(() => navigate({ href: '/auth/login' }));
  }

  return (
    <NotForThisApp current="backoffice" fits={fits} onSignOut={signOut} signingOut={signingOut} />
  );
}
