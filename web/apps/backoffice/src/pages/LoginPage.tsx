import type { ApiError } from '@kapsora/api-client';
import { afterSignIn, browser, safeReturnTo, useSessionStore } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import {
  Button,
  Card,
  DemoAccounts,
  FormField,
  Input,
  PasswordInput,
  ProblemAlert,
  type DemoAccount,
} from '@kapsora/ui';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { APP_URLS } from '../appUrls';
import { problemOf } from '../problems';
import { useState, type FormEvent } from 'react';

/**
 * Demo sign-in: only on the development server with the in-browser sample data. A built bundle
 * has DEV false, so the list below never reaches a real deployment, whatever the mock flag says.
 */
const DEMO_LOGIN = import.meta.env.DEV && import.meta.env['VITE_API_MOCK'] !== 'false';
const DEMO_PASSWORD = 'demo parola 2026 kapsora';
const DEMO_ACCOUNTS: DemoAccount[] = [
  {
    username: 'financial.reviewer',
    name: 'Fuat Mali Değerlendirici',
    role: 'İcmal, geri ödeme, günlük mutabakat, dışa aktarım',
  },
  {
    username: 'payer.approver',
    name: 'Pınar Ödeyici Onaylayıcı',
    role: 'Ödeme mutabakatı onayı ve ödeme kaydı',
  },
  {
    username: 'admin.a',
    name: 'Ayşe Yönetici',
    role: 'Kurumlar, hak sahipleri, programlar, sözleşmeler',
  },
  {
    username: 'doctor.a',
    name: 'Demet Tıbbi Değerlendirici',
    role: 'Tıbbi rapor ve claim incelemesi',
  },
  {
    username: 'sponsor.hr',
    name: 'Selin İnsan Kaynakları',
    role: 'Çalışan görünümü, tanı görmeden',
  },
  {
    username: 'staff.member',
    name: 'Deniz Çalışan',
    role: 'Tıbbi değerlendirici; aynı zamanda üye (iki uygulama)',
  },
];

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

  function submit(e: FormEvent) {
    e.preventDefault();
    void signIn(username.trim(), password);
  }

  async function signIn(user: string, pass: string) {
    setBusy(true);
    setProblem(null);
    try {
      const state = await store.login(user, pass);
      setPassword('');
      if (state.session?.mustChangePassword) {
        await navigate({ to: '/auth/password' });
        return;
      }
      const target = safeReturnTo(search.returnTo, '/');
      // The single sign-in: an account whose only app is another one goes there, one with
      // several chooses, one with none is told (APP_CHOOSER_PATH).
      const next = afterSignIn(state.me?.tenants ?? [], 'backoffice');
      if (next.kind === 'go') {
        browser.assign(APP_URLS[next.app]);
        return;
      }
      if (next.kind !== 'stay') {
        await navigate({ href: `/auth/apps?returnTo=${encodeURIComponent(target)}` });
        return;
      }
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
            <PasswordInput
              name="password"
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
        {DEMO_LOGIN ? (
          <DemoAccounts
            accounts={DEMO_ACCOUNTS}
            busy={busy}
            onPick={(user) => {
              setUsername(user);
              void signIn(user, DEMO_PASSWORD);
            }}
          />
        ) : null}
      </Card>
    </main>
  );
}
