import {
  SessionProvider,
  requireTenant,
  safeReturnTo,
  tenantColor,
  useSession,
  useSessionStore,
} from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import {
  AppShell,
  Badge,
  Button,
  Card,
  EmptyState,
  FormField,
  Input,
  ProblemAlert,
  ToastProvider,
} from '@kapsora/ui';
import { QueryClientProvider } from '@tanstack/react-query';
import {
  Link,
  Outlet,
  RouterProvider,
  createRootRouteWithContext,
  createRoute,
  createRouter,
  redirect,
  useNavigate,
  useSearch,
  type RouterHistory,
} from '@tanstack/react-router';
import { useMemo, useState, type FormEvent } from 'react';
import type { AppServices } from './services';

/** Provider portal shell: routing, session bootstrap, tenant header, placeholder home. */

interface RouterContext {
  services: AppServices;
}

interface ProblemView {
  code: string;
  title: string;
  status: number;
  traceId: string;
}

const rootRoute = createRootRouteWithContext<RouterContext>()({ component: () => <Outlet /> });

function returnToSearch(raw: Record<string, unknown>): { returnTo?: string } {
  return typeof raw['returnTo'] === 'string' ? { returnTo: raw['returnTo'] } : {};
}

function LoginPage() {
  const { t } = useTranslation();
  const store = useSessionStore();
  const navigate = useNavigate();
  const search = useSearch({ from: '/auth/login' });
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemView | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setProblem(null);
    try {
      await store.login(username.trim(), password);
      setPassword('');
      await navigate({ href: safeReturnTo(search.returnTo, '/') });
    } catch (err) {
      const p = (err as { problem?: ProblemView }).problem;
      setProblem(p ?? { code: 'UNKNOWN', title: '', status: 0, traceId: '' });
    } finally {
      setBusy(false);
    }
  }

  return (
    <main id="main" className="flex min-h-dvh items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <h1 className="text-xl font-semibold">{t('auth.loginTitle')}</h1>
        <form onSubmit={submit} className="mt-6 grid gap-4" noValidate>
          <FormField label={t('auth.username')} required requiredLabel={t('common.requiredMark')}>
            <Input
              name="username"
              autoComplete="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              required
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
            {t('auth.submit')}
          </Button>
        </form>
      </Card>
    </main>
  );
}

function TenantPage() {
  const { t } = useTranslation();
  const store = useSessionStore();
  const navigate = useNavigate();
  const search = useSearch({ from: '/auth/tenant' });
  const me = useSession((s) => s.me);
  return (
    <main id="main" className="flex min-h-dvh items-center justify-center p-4">
      <Card className="w-full max-w-md">
        <h1 className="text-xl font-semibold">{t('auth.tenantPickerTitle')}</h1>
        <ul className="mt-4 grid gap-2">
          {(me?.tenants ?? []).map((ctx) => (
            <li key={ctx.tenant.id} className="flex items-center gap-3">
              <span className="flex-1">{ctx.tenant.displayName}</span>
              <Button
                size="sm"
                onClick={() =>
                  void store
                    .switchTenant(ctx.tenant.id)
                    .then(() => navigate({ href: safeReturnTo(search.returnTo, '/') }))
                }
              >
                {t('auth.tenantSelect')}
              </Button>
            </li>
          ))}
        </ul>
      </Card>
    </main>
  );
}

function Shell() {
  const { t } = useTranslation();
  const store = useSessionStore();
  const navigate = useNavigate();
  const me = useSession((s) => s.me);
  const active = useSession((s) => s.activeTenant);
  const color = active ? tenantColor(active.tenant.code) : null;
  const header = (
    <div className="flex h-14 items-center gap-3 px-4">
      <Link to="/" className="font-semibold">
        {t('app.name')} · {t('nav.providers')}
      </Link>
      {active && color ? (
        <Badge
          style={{
            background: color.background,
            color: color.foreground,
            borderColor: color.accent,
          }}
        >
          {active.tenant.displayName}
        </Badge>
      ) : null}
      <span className="text-fg-muted ml-auto text-sm">{me?.displayName}</span>
      <Button
        size="sm"
        variant="secondary"
        onClick={() => {
          void store.logout().finally(() => navigate({ to: '/auth/login', search: {} }));
        }}
      >
        {t('auth.logout')}
      </Button>
    </div>
  );
  const nav = (
    <nav className="p-3 text-sm">
      <Link to="/" className="block rounded px-3 py-2">
        {t('nav.home')}
      </Link>
    </nav>
  );
  return (
    <AppShell
      header={header}
      nav={nav}
      skipLinkLabel={t('app.skipToContent')}
      {...(color ? { accent: color.accent } : {})}
    >
      <Outlet />
    </AppShell>
  );
}

function HomePage() {
  const { t } = useTranslation();
  const active = useSession((s) => s.activeTenant);
  return (
    <EmptyState
      title={`${t('nav.providers')} · ${active?.tenant.displayName ?? ''}`}
      description={t('app.soonBody')}
    />
  );
}

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/login',
  validateSearch: returnToSearch,
  beforeLoad: async ({ context }) => {
    const s = await context.services.store.bootstrap();
    if (s.status === 'authenticated') throw redirect({ to: '/' });
  },
  component: LoginPage,
});
const tenantRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/tenant',
  validateSearch: returnToSearch,
  component: TenantPage,
});
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'app',
  beforeLoad: ({ context, location }) =>
    requireTenant(context.services.store, {
      pathname: location.pathname,
      searchStr: location.searchStr,
    }),
  component: Shell,
});
const homeRoute = createRoute({ getParentRoute: () => appRoute, path: '/', component: HomePage });
const routeTree = rootRoute.addChildren([
  loginRoute,
  tenantRoute,
  appRoute.addChildren([homeRoute]),
]);

export function createAppRouter(services: AppServices, history?: RouterHistory) {
  return createRouter({ routeTree, context: { services }, ...(history ? { history } : {}) });
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}

export function App({ services, history }: { services: AppServices; history?: RouterHistory }) {
  const router = useMemo(() => createAppRouter(services, history), [services, history]);
  return (
    <QueryClientProvider client={services.queryClient}>
      <SessionProvider store={services.store}>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </SessionProvider>
    </QueryClientProvider>
  );
}
