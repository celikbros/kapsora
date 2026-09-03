import { redirectIfAuthenticated, requireAuthenticated, requireTenant } from '@kapsora/auth';
import {
  Outlet,
  createRootRouteWithContext,
  createRoute,
  createRouter,
  type RouterHistory,
} from '@tanstack/react-router';
import type { AppServices } from './api';
import { AppLayout } from './layout/AppLayout';
import { SOON_PATHS } from './nav';
import { OrganizationDetailPage } from './organizations/OrganizationDetailPage';
import { OrganizationEditPage } from './organizations/OrganizationEditPage';
import {
  OrganizationListPage,
  type OrganizationListSearch,
} from './organizations/OrganizationListPage';
import { PersonCreatePage } from './people/PersonCreatePage';
import { PersonDetailPage } from './people/PersonDetailPage';
import { PersonListPage, type PersonListSearch } from './people/PersonListPage';
import { HomePage } from './pages/HomePage';
import { LoginPage } from './pages/LoginPage';
import { LogoutPage } from './pages/LogoutPage';
import { PasswordPage } from './pages/PasswordPage';
import { ProfilePage } from './pages/ProfilePage';
import { SoonPage } from './pages/SoonPage';
import { TenantPickerPage } from './pages/TenantPickerPage';

export interface RouterContext {
  services: AppServices;
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: () => <Outlet />,
});

function returnToSearch(raw: Record<string, unknown>): { returnTo?: string } {
  return typeof raw['returnTo'] === 'string' ? { returnTo: raw['returnTo'] } : {};
}

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/login',
  validateSearch: returnToSearch,
  beforeLoad: ({ context }) => redirectIfAuthenticated(context.services.store, '/'),
  component: LoginPage,
});

const logoutRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/logout',
  component: LogoutPage,
});

const tenantRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/tenant',
  validateSearch: returnToSearch,
  beforeLoad: ({ context, location }) =>
    requireAuthenticated(context.services.store, {
      pathname: location.pathname,
      searchStr: location.searchStr,
    }),
  component: TenantPickerPage,
});

const passwordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/auth/password',
  beforeLoad: ({ context, location }) =>
    requireAuthenticated(context.services.store, {
      pathname: location.pathname,
      searchStr: location.searchStr,
    }),
  component: PasswordPage,
});

/** Everything under the shell needs a session and an active tenant. */
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'app',
  beforeLoad: ({ context, location }) =>
    requireTenant(context.services.store, {
      pathname: location.pathname,
      searchStr: location.searchStr,
    }),
  component: AppLayout,
});

const homeRoute = createRoute({ getParentRoute: () => appRoute, path: '/', component: HomePage });
const profileRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/profile',
  component: ProfilePage,
});

function organizationListSearch(raw: Record<string, unknown>): OrganizationListSearch {
  const out: OrganizationListSearch = {};
  const role = raw['role'];
  if (
    role === 'PAYER' ||
    role === 'SPONSOR' ||
    role === 'PROVIDER' ||
    role === 'VENDOR' ||
    role === 'PARTNER'
  )
    out.role = role;
  if (typeof raw['q'] === 'string' && raw['q'] !== '') out.q = raw['q'];
  if (typeof raw['cursor'] === 'string' && raw['cursor'] !== '') out.cursor = raw['cursor'];
  return out;
}

const organizationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/organizations',
  validateSearch: organizationListSearch,
  component: OrganizationListPage,
});
const organizationNewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/organizations/new',
  component: () => <OrganizationEditPage mode="create" />,
});
const organizationDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/organizations/$organizationId',
  component: OrganizationDetailPage,
});
const organizationEditRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/organizations/$organizationId/edit',
  component: () => <OrganizationEditPage mode="edit" />,
});

function personListSearch(raw: Record<string, unknown>): PersonListSearch {
  const out: PersonListSearch = {};
  const status = raw['status'];
  if (
    status === 'ACTIVE' ||
    status === 'INACTIVE' ||
    status === 'DECEASED' ||
    status === 'MERGED'
  ) {
    out.status = status;
  }
  if (typeof raw['q'] === 'string' && raw['q'] !== '') out.q = raw['q'];
  if (typeof raw['cursor'] === 'string' && raw['cursor'] !== '') out.cursor = raw['cursor'];
  return out;
}

const peopleRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/people',
  validateSearch: personListSearch,
  component: PersonListPage,
});
const personCreateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/people/new',
  component: PersonCreatePage,
});

const personDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/people/$personId',
  component: PersonDetailPage,
});

const soonRoutes = SOON_PATHS.map((path) =>
  createRoute({ getParentRoute: () => appRoute, path, component: SoonPage }),
);

const routeTree = rootRoute.addChildren([
  loginRoute,
  logoutRoute,
  tenantRoute,
  passwordRoute,
  appRoute.addChildren([
    homeRoute,
    profileRoute,
    organizationsRoute,
    organizationNewRoute,
    organizationDetailRoute,
    organizationEditRoute,
    peopleRoute,
    personCreateRoute,
    personDetailRoute,
    ...soonRoutes,
  ]),
]);

export function createAppRouter(services: AppServices, history?: RouterHistory) {
  return createRouter({
    routeTree,
    context: { services },
    defaultPreload: false,
    scrollRestoration: true,
    ...(history ? { history } : {}),
  });
}

export type AppRouter = ReturnType<typeof createAppRouter>;

declare module '@tanstack/react-router' {
  interface Register {
    router: AppRouter;
  }
}

export {
  loginRoute,
  organizationDetailRoute,
  organizationEditRoute,
  organizationsRoute,
  tenantRoute,
};
