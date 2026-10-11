import {
  SessionProvider,
  afterSignIn,
  browser,
  useAccountWatch,
  fitsApp,
  pendingOrganizationNames,
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
  EmptyState,
  FormField,
  HelpButton,
  HelpDrawer,
  Input,
  PasswordInput,
  ProblemAlert,
  SignInLayout,
  ToastProvider,
  pageHelpFor,
  useToast,
  DEMO_ACCOUNTS,
  DEMO_PASSWORD,
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
  useRouterState,
  useSearch,
  type RouterHistory,
} from '@tanstack/react-router';
import { useEffect, useMemo, useState, type ComponentType, type FormEvent } from 'react';
import { billingDestination, portalLanding, portalNav, type PortalArea } from './access';
import { RouteAccess } from './RouteAccess';
import { HELP_ROUTES } from './help';
import { EligibilityPage } from './EligibilityPage';
import { MyRequestsPage } from './MyRequestsPage';
import { NewRequestPage } from './NewRequestPage';
import { RequestPage } from './RequestPage';
import { ClaimListPage } from './claims/ClaimListPage';
import { ClaimNewPage } from './claims/ClaimNewPage';
import { ClaimPage } from './claims/ClaimPage';
import { CaseListPage } from './health/CaseListPage';
import { CaseOpenPage } from './health/CaseOpenPage';
import { CasePage } from './health/CasePage';
import { ReportPage } from './health/ReportPage';
import { StayPage } from './health/StayPage';
import { BatchListPage } from './billing/BatchListPage';
import { BatchNewPage, BatchPage } from './billing/BatchPage';
import { EarningsPage } from './billing/EarningsPage';
import { InvoiceListPage } from './billing/InvoiceListPage';
import { InvoiceNewPage, InvoicePage } from './billing/InvoicePage';
import { StatementPage } from './billing/StatementPage';
import { DeskPage } from './lodging/DeskPage';
import { InventoryPage } from './lodging/InventoryPage';
import { ServicesProvider, type AppServices } from './services';
import { APP_URLS } from './appUrls';
import { AppChooserPage } from './AppChooserPage';

/** Provider portal shell: routing, session bootstrap, tenant header, the desk's rail. */

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
 * Demo sign-in: only on the development server. A built bundle
 * has DEV false, so the list below never reaches a real deployment, whether it talks to the sample data or to a local API.
 */
const DEMO_LOGIN = import.meta.env.DEV;
// The hand-over between apps is a page load into another origin, and it only means anything
// where they share a session. The demo launcher runs the three apps on their own in-browser
// worlds and sets VITE_SINGLE_SIGN_IN=false, so there an account stays where it signed in and
// the chooser does the telling instead.
const HANDS_OVER = import.meta.env['VITE_SINGLE_SIGN_IN'] !== 'false';

// With the hand-over off every app is its own world, so the list offers only the accounts that
// can work in this one: tapping somebody else's account would sign in and then be told it is the
// wrong app, which is a dead end this screen invited.
const DEMO_LIST = HANDS_OVER
  ? DEMO_ACCOUNTS
  : DEMO_ACCOUNTS.filter((account) => account.app === 'provider' || account.app === 'both');

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
      if (pendingOrganizationNames(state.me?.tenants ?? []).length > 0) {
        await navigate({ href: `/auth/apps?returnTo=${encodeURIComponent(target)}` });
        return;
      }
      // The single sign-in: an account whose only app is another one goes there, one with
      // several chooses, one with none is told (APP_CHOOSER_PATH).
      const next = HANDS_OVER
        ? afterSignIn(state.me?.tenants ?? [], 'provider')
        : ({ kind: 'stay' } as const);
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
    <SignInLayout
      app="provider"
      demoAccounts={
        DEMO_LOGIN ? (
          <DemoAccounts
            accounts={DEMO_LIST}
            app="provider"
            busy={busy}
            onPick={(user) => {
              setUsername(user);
              void signIn(user, DEMO_PASSWORD);
            }}
          />
        ) : null
      }
    >
      <Card className="w-full">
        <h2 className="text-xl font-semibold">{t('auth.loginTitle')}</h2>
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
      </Card>
    </SignInLayout>
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
            .filter((ctx) => fitsApp(ctx, 'provider'))
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

function Shell() {
  const { t } = useTranslation();
  const store = useSessionStore();
  const navigate = useNavigate();
  // The session is shared by every tab and app: when this tab comes back to the front, make
  // sure it is still the same account's before it shows or does anything more.
  const toast = useToast();
  useAccountWatch((result) => {
    if (result === 'changed') {
      toast.notify({ tone: 'info', title: t('auth.accountChanged') });
      void navigate({ to: '/' });
    } else {
      toast.notify({ tone: 'info', title: t('auth.signedOutElsewhere') });
      void navigate({ to: '/auth/login', search: {} });
    }
  });
  const me = useSession((s) => s.me);
  const active = useSession((s) => s.activeTenant);
  const visible = new Set(portalNav(active?.permissions ?? []).map((entry) => entry.key));
  const billingPath = billingDestination(active?.permissions ?? []);
  const color = active ? tenantColor(active.tenant.code) : null;
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const [helpOpen, setHelpOpen] = useState(false);
  const help = pageHelpFor('provider', pathname, HELP_ROUTES, {
    title: t('help.open'),
    purpose: t('help.none'),
  });
  const header = (
    <div className="flex h-14 min-w-0 items-center gap-2 px-3 sm:gap-3 sm:px-4">
      <Link to="/" className="shrink-0 font-semibold">
        {t('app.name')}
        <span className="hidden sm:inline"> · {t('nav.providers')}</span>
      </Link>
      {active && color ? (
        <Badge
          style={{
            background: color.background,
            color: color.foreground,
            borderColor: color.accent,
          }}
          className="max-w-[7rem] shrink-0 truncate sm:max-w-64"
        >
          {active.tenant.displayName}
        </Badge>
      ) : null}
      <HelpButton
        label={t('help.open')}
        expanded={helpOpen}
        className="ml-auto"
        onClick={() => setHelpOpen(true)}
      />
      <HelpDrawer open={helpOpen} onOpenChange={setHelpOpen} page={help} />
      <span className="text-fg-muted hidden truncate text-sm sm:inline">{me?.displayName}</span>
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
  // The rail names the desk's tasks and nothing else: the request, the answer, and from
  // M5 the case as the spine of everything clinical, with the claim as the billing desk's
  // own list. Uploading a document lives on the request that is waiting for it.
  const item =
    'aria-[current=page]:bg-primary-soft aria-[current=page]:text-primary block rounded px-3 py-2';
  const nav = (
    <nav className="p-3 text-sm" aria-label={t('provider.title')}>
      {visible.has('newRequest') ? (
        <Link to="/" className={item} activeOptions={{ exact: true }}>
          {t('provider.nav.newRequest')}
        </Link>
      ) : null}
      {visible.has('myRequests') ? (
        <Link to="/requests" className={item}>
          {t('provider.nav.myRequests')}
        </Link>
      ) : null}
      {visible.has('eligibility') ? (
        <Link to="/eligibility" className={item}>
          {t('provider.nav.eligibility')}
        </Link>
      ) : null}
      {visible.has('cases') ? (
        <Link to="/cases" className={item}>
          {t('provider.nav.cases')}
        </Link>
      ) : null}
      {visible.has('claims') ? (
        <Link to="/claims" className={item}>
          {t('provider.nav.claims')}
        </Link>
      ) : null}
      {visible.has('inventory') ? (
        <Link to="/lodging/inventory" className={item}>
          {t('provider.nav.inventory')}
        </Link>
      ) : null}
      {visible.has('desk') ? (
        <Link to="/lodging/desk" className={item}>
          {t('provider.nav.desk')}
        </Link>
      ) : null}
      {billingPath ? (
        <Link to={billingPath} className={item}>
          {t('provider.nav.billing')}
        </Link>
      ) : null}
    </nav>
  );
  return (
    <AppShell
      header={header}
      nav={nav}
      skipLinkLabel={t('app.skipToContent')}
      menuLabel={t('app.menu')}
      {...(color ? { accent: color.accent } : {})}
    >
      <Outlet />
    </AppShell>
  );
}

/** The root is the request desk for clinical staff and a safe landing for other roles. */
function HomeEntry() {
  const { t } = useTranslation();
  const permissions = useSession((s) => s.activeTenant?.permissions ?? []);
  const navigate = useNavigate();
  const destination = portalLanding(permissions);
  useEffect(() => {
    if (destination && destination !== '/') void navigate({ href: destination, replace: true });
  }, [destination, navigate]);
  if (destination === '/')
    return (
      <RouteAccess area="newRequest">
        <NewRequestPage />
      </RouteAccess>
    );
  if (destination) return null;
  return (
    <div data-testid="provider-route-denied">
      <EmptyState title={t('problems.PERMISSION_DENIED')} />
    </div>
  );
}

function guarded(area: PortalArea, Page: ComponentType) {
  return function GuardedPage() {
    return (
      <RouteAccess area={area}>
        <Page />
      </RouteAccess>
    );
  };
}

function CaseOpenEntry() {
  const search = useSearch({ from: '/app/cases/new' });
  return (
    <RouteAccess area="caseOpen" fromRequest={Boolean(search.requestId)}>
      <CaseOpenPage />
    </RouteAccess>
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
      'provider',
    ),
  component: Shell,
});
const homeRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/',
  component: HomeEntry,
});
const myRequestsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/requests',
  component: guarded('requests', MyRequestsPage),
});
const requestRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/requests/$requestId',
  component: guarded('requests', RequestPage),
});
const eligibilityRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/eligibility',
  component: guarded('eligibility', EligibilityPage),
});
function idSearch<K extends string>(key: K) {
  return (raw: Record<string, unknown>): Partial<Record<K, string>> =>
    typeof raw[key] === 'string' && raw[key] !== ''
      ? ({ [key]: raw[key] } as Partial<Record<K, string>>)
      : {};
}
const casesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/cases',
  component: guarded('cases', CaseListPage),
});
const caseOpenRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/cases/new',
  validateSearch: idSearch('requestId'),
  component: CaseOpenEntry,
});
const caseRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/cases/$caseId',
  component: guarded('cases', CasePage),
});
const reportRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/reports/$reportId',
  component: guarded('report', ReportPage),
});
const stayRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/stays/$stayId',
  component: guarded('stay', StayPage),
});
const claimsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/claims',
  component: guarded('claims', ClaimListPage),
});
const claimNewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/claims/new',
  validateSearch: idSearch('caseId'),
  component: guarded('claimNew', ClaimNewPage),
});
const claimRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/claims/$claimId',
  component: guarded('claims', ClaimPage),
});
const inventoryRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/lodging/inventory',
  component: guarded('inventory', InventoryPage),
});
const deskRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/lodging/desk',
  component: guarded('desk', DeskPage),
});
function billingSearch(raw: Record<string, unknown>): {
  currency?: string;
  claims?: string;
  supersedes?: string;
} {
  const out: { currency?: string; claims?: string; supersedes?: string } = {};
  if (typeof raw['currency'] === 'string' && raw['currency'] !== '') out.currency = raw['currency'];
  if (typeof raw['claims'] === 'string') out.claims = raw['claims'];
  if (typeof raw['supersedes'] === 'string' && raw['supersedes'] !== '')
    out.supersedes = raw['supersedes'];
  return out;
}
const earningsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing',
  component: guarded('earnings', EarningsPage),
});
const invoicesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/invoices',
  component: guarded('invoices', InvoiceListPage),
});
const invoiceNewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/invoices/new',
  validateSearch: billingSearch,
  component: guarded('invoiceNew', InvoiceNewPage),
});
const invoiceRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/invoices/$invoiceId',
  validateSearch: billingSearch,
  component: guarded('invoices', InvoicePage),
});
const batchesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/batches',
  component: guarded('batches', BatchListPage),
});
const batchNewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/batches/new',
  component: guarded('batchNew', BatchNewPage),
});
const batchRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/batches/$batchId',
  component: guarded('batches', BatchPage),
});
const statementRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/statement',
  component: guarded('statement', StatementPage),
});
const routeTree = rootRoute.addChildren([
  loginRoute,
  tenantRoute,
  appChooserRoute,
  appRoute.addChildren([
    homeRoute,
    myRequestsRoute,
    requestRoute,
    eligibilityRoute,
    casesRoute,
    caseOpenRoute,
    caseRoute,
    reportRoute,
    stayRoute,
    claimsRoute,
    claimNewRoute,
    claimRoute,
    inventoryRoute,
    deskRoute,
    earningsRoute,
    invoicesRoute,
    invoiceNewRoute,
    invoiceRoute,
    batchesRoute,
    batchNewRoute,
    batchRoute,
    statementRoute,
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
    <ServicesProvider services={services}>
      <QueryClientProvider client={services.queryClient}>
        <SessionProvider store={services.store}>
          <ToastProvider>
            <RouterProvider router={router} />
          </ToastProvider>
        </SessionProvider>
      </QueryClientProvider>
    </ServicesProvider>
  );
}
