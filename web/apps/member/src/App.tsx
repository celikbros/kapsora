import {
  SessionProvider,
  afterSignIn,
  browser,
  fitsApp,
  requireAuthenticated,
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
  DemoAccounts,
  FormField,
  Input,
  PasswordInput,
  ProblemAlert,
  ToastProvider,
  useMinWidth,
  type DemoAccount,
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
import { BookingPage } from './lodging/BookingPage';
import { BookingsPage } from './lodging/BookingsPage';
import { HomePage } from './lodging/HomePage';
import { SearchPage } from './lodging/SearchPage';
import { NewReimbursementPage } from './reimbursement/NewReimbursementPage';
import { ReimbursementPage } from './reimbursement/ReimbursementPage';
import { ReimbursementsPage } from './reimbursement/ReimbursementsPage';
import { ServicesProvider, type AppServices } from './services';
import { APP_URLS } from './appUrls';
import { AppChooserPage } from './AppChooserPage';

/**
 * Member PWA shell (mobile-first): routing, session bootstrap, the tenant header and the
 * three tabs — Ana sayfa · Ara · Rezervasyonlarım — pinned to the bottom of a phone and
 * standing in the header on a wide screen.
 */

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

/**
 * Demo sign-in: only on the development server with the in-browser sample data. A built bundle
 * has DEV false, so the list below never reaches a real deployment, whatever the mock flag says.
 */
const DEMO_LOGIN = import.meta.env.DEV && import.meta.env['VITE_API_MOCK'] !== 'false';
const DEMO_PASSWORD = 'demo parola 2026 kapsora';
const DEMO_ACCOUNTS: DemoAccount[] = [
  { username: 'member.a', name: 'Hak sahibi', role: 'Kalan haklar, konaklama, geri ödeme' },
  {
    username: 'staff.member',
    name: 'Deniz Çalışan',
    role: 'Üye; aynı zamanda tıbbi değerlendirici (iki uygulama)',
  },
];

function LoginPage() {
  const { t } = useTranslation();
  const store = useSessionStore();
  const navigate = useNavigate();
  const search = useSearch({ from: '/auth/login' });
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemView | null>(null);

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
      const target = safeReturnTo(search.returnTo, '/');
      // The single sign-in: an account whose only app is another one goes there, one with
      // several chooses, one with none is told (APP_CHOOSER_PATH).
      const next = afterSignIn(state.me?.tenants ?? [], 'member');
      if (next.kind === 'go') {
        browser.assign(APP_URLS[next.app]);
        return;
      }
      if (next.kind !== 'stay') {
        await navigate({ href: `/auth/apps?returnTo=${encodeURIComponent(target)}` });
        return;
      }
      await navigate({ href: target });
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
            {t('auth.submit')}
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
          {(me?.tenants ?? [])
            .filter((ctx) => fitsApp(ctx, 'member'))
            .map((ctx) => (
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

/** A drawn icon per tab: one stroke weight, no glyph standing in for it. */
function TabIcon({ name }: { name: 'home' | 'search' | 'bookings' }) {
  const paths = {
    home: 'M3 10.5 10 4l7 6.5M5 9.5V16h10V9.5',
    search: 'M12.5 12.5 16 16M4 9a5 5 0 1 0 10 0A5 5 0 0 0 4 9Z',
    bookings: 'M5 3h10v14l-5-3-5 3V3Z',
  } as const;
  return (
    <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
      <path
        d={paths[name]}
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function Tabs({ wide }: { wide: boolean }) {
  const { t } = useTranslation();
  const tab = wide
    ? 'aria-[current=page]:text-primary aria-[current=page]:border-primary flex items-center gap-2 border-b-2 border-transparent px-3 py-2 text-sm'
    : 'aria-[current=page]:text-primary text-fg-muted flex flex-1 flex-col items-center gap-0.5 py-2 text-xs';
  return (
    <nav
      aria-label={t('member.nav')}
      data-testid="member-tabs"
      className={
        wide
          ? 'flex items-center gap-1'
          : 'bg-surface-raised border-line fixed inset-x-0 bottom-0 z-30 flex border-t pb-[env(safe-area-inset-bottom)]'
      }
    >
      <Link to="/" className={tab} activeOptions={{ exact: true }}>
        <TabIcon name="home" />
        {t('member.home')}
      </Link>
      <Link to="/search" className={tab}>
        <TabIcon name="search" />
        {t('member.search')}
      </Link>
      <Link to="/bookings" className={tab}>
        <TabIcon name="bookings" />
        {t('member.bookings')}
      </Link>
    </nav>
  );
}

function Shell() {
  const { t } = useTranslation();
  const store = useSessionStore();
  const navigate = useNavigate();
  const wide = useMinWidth(768);
  const active = useSession((s) => s.activeTenant);
  const color = active ? tenantColor(active.tenant.code) : null;
  const header = (
    <div className="flex h-14 items-center gap-3 px-4">
      <Link to="/" className="font-semibold">
        {t('app.name')}
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
      {wide ? (
        <div className="ml-4">
          <Tabs wide />
        </div>
      ) : null}
      <Button
        size="sm"
        variant="secondary"
        className="ml-auto"
        onClick={() => {
          void store.logout().finally(() => navigate({ to: '/auth/login', search: {} }));
        }}
      >
        {t('auth.logout')}
      </Button>
    </div>
  );
  return (
    <AppShell
      header={header}
      skipLinkLabel={t('app.skipToContent')}
      {...(color ? { accent: color.accent } : {})}
    >
      <div className={wide ? 'mx-auto w-full max-w-5xl' : 'pb-16'}>
        <Outlet />
      </div>
      {!wide ? <Tabs wide={false} /> : null}
    </AppShell>
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
const appChooserRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/apps',
  validateSearch: returnToSearch,
  beforeLoad: ({ context, location }) =>
    requireAuthenticated(context.services.store, {
      pathname: location.pathname,
      searchStr: location.searchStr,
    }),
  component: AppChooserPage,
});
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'app',
  beforeLoad: ({ context, location }) =>
    requireTenant(
      context.services.store,
      {
        pathname: location.pathname,
        searchStr: location.searchStr,
      },
      'member',
    ),
  component: Shell,
});
const homeRoute = createRoute({ getParentRoute: () => appRoute, path: '/', component: HomePage });
const searchRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/search',
  component: SearchPage,
});
const bookingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/bookings',
  component: BookingsPage,
});
const bookingRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/bookings/$bookingId',
  component: BookingPage,
});
const reimbursementsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/reimbursements',
  component: ReimbursementsPage,
});
const reimbursementNewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/reimbursements/new',
  component: NewReimbursementPage,
});
const reimbursementRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/reimbursements/$reimbursementId',
  component: ReimbursementPage,
});
const routeTree = rootRoute.addChildren([
  loginRoute,
  tenantRoute,
  appChooserRoute,
  appRoute.addChildren([
    homeRoute,
    searchRoute,
    bookingsRoute,
    bookingRoute,
    reimbursementsRoute,
    reimbursementNewRoute,
    reimbursementRoute,
  ]),
]);

export function createAppRouter(services: AppServices, history?: RouterHistory) {
  // BASE_URL is / on the development server and the deployed path in a build (vite.config.ts).
  return createRouter({
    routeTree,
    basepath: import.meta.env.BASE_URL,
    context: { services },
    ...(history ? { history } : {}),
  });
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
      <ServicesProvider services={services}>
        <SessionProvider store={services.store}>
          <ToastProvider>
            <RouterProvider router={router} />
          </ToastProvider>
        </SessionProvider>
      </ServicesProvider>
    </QueryClientProvider>
  );
}
