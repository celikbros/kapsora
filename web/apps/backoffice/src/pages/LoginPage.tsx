import type { ApiError } from '@kapsora/api-client';
import { safeReturnTo, useSessionStore } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import { Button, Card, FormField, Input, ProblemAlert } from '@kapsora/ui';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { problemOf } from '../problems';
import { useState, type FormEvent } from 'react';

/** Own-credentials login (ADR-022): username + password to POST /api/v1/session/login. */
export function LoginPage() {
  const { t } = useTranslation();
  const store = useSessionStore();
  const navigate = useNavigate();
  const search = useSearch({ from: '/auth/login' });
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ApiError['problem'] | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setProblem(null);
    try {
      const state = await store.login(username.trim(), password);
      setPassword('');
      if (state.session?.mustChangePassword) {
        await navigate({ to: '/auth/password' });
        return;
      }
      const target = safeReturnTo(search.returnTo, '/');
      if (!state.activeTenant) {
        // The server never picks a tenant; one active membership is selected here, several
        // go to the picker (the app-route guard applies the same rule on deep links).
        const active = (state.me?.tenants ?? []).filter((c) => c.tenant.status === 'ACTIVE');
        if (active.length === 1) {
          await store.switchTenant(active[0]!.tenant.id);
        } else {
          await navigate({ to: '/auth/tenant', search: { returnTo: target } });
          return;
        }
      }
      await navigate({ href: target });
    } catch (err) {
      setProblem(problemOf(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main id="main" className="flex min-h-dvh items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <h1 className="text-xl font-semibold">{t('auth.loginTitle')}</h1>
        <p className="text-fg-muted mt-1 text-sm">{t('auth.loginIntro')}</p>
        <form onSubmit={submit} className="mt-6 grid gap-4" noValidate>
          <FormField label={t('auth.username')} required requiredLabel={t('common.requiredMark')}>
            <Input
              name="username"
              autoComplete="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              required
              autoFocus
            />
          </FormField>
          <FormField label={t('auth.password')} required requiredLabel={t('common.requiredMark')}>
            <Input
              name="password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
          </FormField>
          <ProblemAlert problem={problem} />
          <Button type="submit" loading={busy} disabled={username.trim() === '' || password === ''}>
            {busy ? t('auth.submitting') : t('auth.submit')}
          </Button>
        </form>
      </Card>
    </main>
  );
}
