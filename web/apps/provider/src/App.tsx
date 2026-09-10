import {
  SessionProvider,
  requireTenant,
  safeReturnTo,
  tenantColor,
  usePermission,
  useSession,
  useSessionStore,
} from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import {
  AppShell,
  Badge,
  Button,
  Card,
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
import { DeskPage } from './lodging/DeskPage';
import { InventoryPage } from './lodging/InventoryPage';
import { ServicesProvider, type AppServices } from './services';

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
  const canReadClaims = usePermission('claim.read');
  const canManageInventory = usePermission('accommodation.inventory.manage');
  const canRunDesk = usePermission('accommodation.booking.manage');
  const canBill = usePermission('invoice.read');
  const color = active ? tenantColor(active.tenant.code) : null;
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
      <span className="text-fg-muted ml-auto hidden truncate text-sm sm:inline">
        {me?.displayName}
      </span>
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
      <Link to="/" className={item} activeOptions={{ exact: true }}>
        {t('provider.nav.newRequest')}
      </Link>
      <Link to="/requests" className={item}>
        {t('provider.nav.myRequests')}
      </Link>
      <Link to="/eligibility" className={item}>
        {t('provider.nav.eligibility')}
      </Link>
      <Link to="/cases" className={item}>
        {t('provider.nav.cases')}
      </Link>
      {canReadClaims ? (
        <Link to="/claims" className={item}>
          {t('provider.nav.claims')}
        </Link>
      ) : null}
      {canManageInventory ? (
        <Link to="/lodging/inventory" className={item}>
          {t('provider.nav.inventory')}
        </Link>
      ) : null}
      {canRunDesk ? (
        <Link to="/lodging/desk" className={item}>
          {t('provider.nav.desk')}
        </Link>
      ) : null}
      {canBill ? (
        <Link to="/billing" className={item}>
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
const homeRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/',
  component: NewRequestPage,
});
const myRequestsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/requests',
  component: MyRequestsPage,
});
const requestRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/requests/$requestId',
  component: RequestPage,
});
const eligibilityRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/eligibility',
  component: EligibilityPage,
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
  component: CaseListPage,
});
const caseOpenRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/cases/new',
  validateSearch: idSearch('requestId'),
  component: CaseOpenPage,
});
const caseRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/cases/$caseId',
  component: CasePage,
});
const reportRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/reports/$reportId',
  component: ReportPage,
});
const stayRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/stays/$stayId',
  component: StayPage,
});
const claimsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/claims',
  component: ClaimListPage,
});
const claimNewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/claims/new',
  validateSearch: idSearch('caseId'),
  component: ClaimNewPage,
});
const claimRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/claims/$claimId',
  component: ClaimPage,
});
const inventoryRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/lodging/inventory',
  component: InventoryPage,
});
const deskRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/lodging/desk',
  component: DeskPage,
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
  component: EarningsPage,
});
const invoicesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/invoices',
  component: InvoiceListPage,
});
const invoiceNewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/invoices/new',
  validateSearch: billingSearch,
  component: InvoiceNewPage,
});
const invoiceRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/invoices/$invoiceId',
  validateSearch: billingSearch,
  component: InvoicePage,
});
const batchesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/batches',
  component: BatchListPage,
});
const batchNewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/batches/new',
  component: BatchNewPage,
});
const batchRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/batches/$batchId',
  component: BatchPage,
});
const routeTree = rootRoute.addChildren([
  loginRoute,
  tenantRoute,
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
  ]),
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
