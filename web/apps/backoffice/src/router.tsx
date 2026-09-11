import { redirectIfAuthenticated, requireAuthenticated, requireTenant } from '@kapsora/auth';
import {
  Outlet,
  createRootRouteWithContext,
  createRoute,
  createRouter,
  type RouterHistory,
} from '@tanstack/react-router';
import { AdjustmentQueuePage } from './adjustments/AdjustmentQueuePage';
import type { AppServices } from './api';
import { CategoryTreePage } from './catalog/CategoryTreePage';
import { CodeSystemDetailPage } from './catalog/CodeSystemDetailPage';
import { CodeSystemListPage } from './catalog/CodeSystemListPage';
import { DefinitionCreatePage } from './catalog/DefinitionCreatePage';
import { DefinitionDetailPage } from './catalog/DefinitionDetailPage';
import { DefinitionListPage } from './catalog/DefinitionListPage';
import { ContractCreatePage } from './contracts/ContractCreatePage';
import { ContractDetailPage } from './contracts/ContractDetailPage';
import { ContractListPage, type ContractListSearch } from './contracts/ContractListPage';
import { ContractVersionPage } from './contracts/ContractVersionPage';
import { QuotePage } from './pricing/QuotePage';
import { ClaimDetailPage } from './claims/ClaimDetailPage';
import { ClaimListPage, type ClaimListSearch } from './claims/ClaimListPage';
import { ReportReviewListPage } from './health/ReportReviewListPage';
import { ReportReviewPage } from './health/ReportReviewPage';
import { RequestCreatePage } from './requests/RequestCreatePage';
import { RequestDetailPage } from './requests/RequestDetailPage';
import { RequestListPage, type RequestListSearch } from './requests/RequestListPage';
import { WorklistPage, type WorklistSearch } from './worklist/WorklistPage';
import { NotificationsPage } from './notifications/NotificationsPage';
import { ProviderCreatePage } from './providers/ProviderCreatePage';
import { RuleSetDetailPage } from './rules/RuleSetDetailPage';
import { RuleSetListPage } from './rules/RuleSetListPage';
import { RuleSetVersionPage } from './rules/RuleSetVersionPage';
import type { RuleSetListSearch } from './rules/routing';
import { ProviderDetailPage } from './providers/ProviderDetailPage';
import { BookingDetailPage } from './lodging/BookingDetailPage';
import { BookingListPage } from './lodging/BookingListPage';
import { PropertiesPage } from './lodging/PropertiesPage';
import { PropertyDetailPage } from './lodging/PropertyDetailPage';
import { WaitlistPage } from './lodging/WaitlistPage';
import { BatchReviewListPage } from './billing/BatchReviewListPage';
import { BatchReviewPage } from './billing/BatchReviewPage';
import { ReimbursementListPage } from './billing/ReimbursementListPage';
import { ReimbursementPage } from './billing/ReimbursementPage';
import { SettlementListPage } from './billing/SettlementListPage';
import { SettlementPage } from './billing/SettlementPage';
import { ExportsPage } from './billing/ExportsPage';
import { ReconciliationListPage } from './billing/ReconciliationListPage';
import { ReconciliationRunPage } from './billing/ReconciliationRunPage';
import { ProviderListPage } from './providers/ProviderListPage';
import { providerListSearch } from './providers/routes';
import { ImportDetailPage } from './imports/ImportDetailPage';
import { ImportListPage } from './imports/ImportListPage';
import { ImportUploadPage } from './imports/ImportUploadPage';
import { PlanDetailPage } from './benefit/PlanDetailPage';
import { PlanVersionPage } from './benefit/PlanVersionPage';
import { ProgramCreatePage } from './benefit/ProgramCreatePage';
import { ProgramDetailPage } from './benefit/ProgramDetailPage';
import { ProgramListPage, type ProgramListSearch } from './benefit/ProgramListPage';
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
import { AppChooserPage } from './pages/AppChooserPage';
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

/** The single sign-in's chooser: where an account with several apps, or none here, goes. */
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

/** Everything under the shell needs a session and an active tenant. */
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
      'backoffice',
    ),
  component: AppLayout,
});

const homeRoute = createRoute({ getParentRoute: () => appRoute, path: '/', component: HomePage });

function requestListSearch(raw: Record<string, unknown>): RequestListSearch {
  const out: RequestListSearch = {};
  if (typeof raw['status'] === 'string' && raw['status'] !== '')
    out.status = raw['status'] as NonNullable<RequestListSearch['status']>;
  if (typeof raw['channel'] === 'string' && raw['channel'] !== '')
    out.channel = raw['channel'] as NonNullable<RequestListSearch['channel']>;
  if (typeof raw['cursor'] === 'string' && raw['cursor'] !== '') out.cursor = raw['cursor'];
  return out;
}
const requestsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/requests',
  validateSearch: requestListSearch,
  component: RequestListPage,
});
const requestCreateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/requests/new',
  component: RequestCreatePage,
});
const requestDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/requests/$requestId',
  component: RequestDetailPage,
});

function worklistSearch(raw: Record<string, unknown>): WorklistSearch {
  const out: WorklistSearch = {};
  const view = raw['view'];
  if (view === 'mine' || view === 'unassigned' || view === 'overdue' || view === 'all')
    out.view = view;
  if (typeof raw['queueId'] === 'string' && raw['queueId'] !== '') out.queueId = raw['queueId'];
  if (typeof raw['cursor'] === 'string' && raw['cursor'] !== '') out.cursor = raw['cursor'];
  return out;
}
const worklistRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/worklist',
  validateSearch: worklistSearch,
  component: WorklistPage,
});
function claimListSearch(raw: Record<string, unknown>): ClaimListSearch {
  const out: ClaimListSearch = {};
  if (typeof raw['status'] === 'string' && raw['status'] !== '')
    out.status = raw['status'] as NonNullable<ClaimListSearch['status']>;
  if (typeof raw['cursor'] === 'string' && raw['cursor'] !== '') out.cursor = raw['cursor'];
  return out;
}
const claimsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/claims',
  validateSearch: claimListSearch,
  component: ClaimListPage,
});
const claimDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/claims/$claimId',
  component: ClaimDetailPage,
});
const lodgingPropertiesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/lodging/properties',
  component: PropertiesPage,
});
const lodgingPropertyRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/lodging/properties/$propertyId',
  component: PropertyDetailPage,
});
const lodgingBookingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/lodging/bookings',
  component: BookingListPage,
});
const lodgingBookingRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/lodging/bookings/$bookingId',
  component: BookingDetailPage,
});
const lodgingWaitlistRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/lodging/waitlist',
  component: WaitlistPage,
});
function statusSearch(raw: Record<string, unknown>): { status?: string } {
  return typeof raw['status'] === 'string' && raw['status'] !== '' ? { status: raw['status'] } : {};
}
const billingBatchesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/batches',
  validateSearch: statusSearch,
  component: BatchReviewListPage,
});
const billingBatchRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/batches/$batchId',
  component: BatchReviewPage,
});
const billingSettlementsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/settlements',
  validateSearch: statusSearch,
  component: SettlementListPage,
});
const billingSettlementRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/settlements/$settlementId',
  component: SettlementPage,
});
const billingReimbursementsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/reimbursements',
  validateSearch: statusSearch,
  component: ReimbursementListPage,
});
const billingReimbursementRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/reimbursements/$reimbursementId',
  component: ReimbursementPage,
});
const billingReconciliationRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/reconciliation',
  component: ReconciliationListPage,
});
const billingReconciliationRunRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/reconciliation/$runId',
  component: ReconciliationRunPage,
});
const billingExportsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/billing/exports',
  component: ExportsPage,
});
const medicalReportsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/medical-reports',
  component: ReportReviewListPage,
});
const medicalReportRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/medical-reports/$reportId',
  component: ReportReviewPage,
});
const notificationsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/notifications',
  component: NotificationsPage,
});
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

function programListSearch(raw: Record<string, unknown>): ProgramListSearch {
  const out: ProgramListSearch = {};
  const status = raw['status'];
  if (status === 'DRAFT' || status === 'ACTIVE' || status === 'SUSPENDED' || status === 'CLOSED') {
    out.status = status;
  }
  if (typeof raw['q'] === 'string' && raw['q'] !== '') out.q = raw['q'];
  if (typeof raw['cursor'] === 'string' && raw['cursor'] !== '') out.cursor = raw['cursor'];
  return out;
}

const programsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/programs',
  validateSearch: programListSearch,
  component: ProgramListPage,
});
const programCreateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/programs/new',
  component: ProgramCreatePage,
});
const programDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/programs/$programId',
  component: ProgramDetailPage,
});
const planDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/plans/$planId',
  component: PlanDetailPage,
});
const planVersionRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/plan-versions/$planVersionId',
  component: PlanVersionPage,
});

const adjustmentsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/entitlement-adjustments',
  component: AdjustmentQueuePage,
});

function ruleSetListSearch(raw: Record<string, unknown>): RuleSetListSearch {
  const out: RuleSetListSearch = {};
  const purpose = raw['purpose'];
  if (
    purpose === 'ELIGIBILITY' ||
    purpose === 'DOCUMENT' ||
    purpose === 'PREAUTH' ||
    purpose === 'LIMIT' ||
    purpose === 'DUPLICATE' ||
    purpose === 'DIAGNOSIS_SERVICE' ||
    purpose === 'PRICE' ||
    purpose === 'ADJUDICATION'
  ) {
    out.purpose = purpose;
  }
  if (typeof raw['q'] === 'string' && raw['q'] !== '') out.q = raw['q'];
  if (typeof raw['cursor'] === 'string' && raw['cursor'] !== '') out.cursor = raw['cursor'];
  return out;
}

const ruleSetsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/rule-sets',
  validateSearch: ruleSetListSearch,
  component: RuleSetListPage,
});
const ruleSetDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/rule-sets/$ruleSetId',
  component: RuleSetDetailPage,
});
const ruleSetVersionRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/rule-set-versions/$ruleSetVersionId',
  component: RuleSetVersionPage,
});

const providersRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/providers',
  validateSearch: providerListSearch,
  component: ProviderListPage,
});
const providerCreateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/providers/new',
  component: ProviderCreatePage,
});
const providerDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/providers/$providerId',
  component: ProviderDetailPage,
});

// Declared one by one rather than mapped over a list: createRoute keeps the path as a
// literal type, and that is what makes Link and useNavigate check a path at compile time.
// A .map() erases the literals and every link to these pages becomes a plain string.
const categoryTreeRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/catalog/categories',
  component: CategoryTreePage,
});
const definitionListRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/catalog/definitions',
  component: DefinitionListPage,
});
const definitionCreateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/catalog/definitions/new',
  component: DefinitionCreatePage,
});
const definitionDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/catalog/definitions/$definitionId',
  component: DefinitionDetailPage,
});
const codeSystemListRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/catalog/code-systems',
  component: CodeSystemListPage,
});
const codeSystemDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/catalog/code-systems/$codeSystemId',
  component: CodeSystemDetailPage,
});

function contractListSearch(raw: Record<string, unknown>): ContractListSearch {
  const out: ContractListSearch = {};
  const status = raw['status'];
  if (status === 'DRAFT' || status === 'ACTIVE' || status === 'SUSPENDED' || status === 'CLOSED') {
    out.status = status;
  }
  if (typeof raw['q'] === 'string' && raw['q'] !== '') out.q = raw['q'];
  if (typeof raw['cursor'] === 'string' && raw['cursor'] !== '') out.cursor = raw['cursor'];
  return out;
}

const contractsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/contracts',
  validateSearch: contractListSearch,
  component: ContractListPage,
});
const contractCreateRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/contracts/new',
  component: ContractCreatePage,
});
const contractDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/contracts/$contractId',
  component: ContractDetailPage,
});
const contractVersionRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/contract-versions/$contractVersionId',
  component: ContractVersionPage,
});
const pricingRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/pricing',
  component: QuotePage,
});

const importsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/imports',
  component: ImportListPage,
});
const importUploadRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/imports/new',
  component: ImportUploadPage,
});
const importDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/imports/$importId',
  component: ImportDetailPage,
});

const soonRoutes = SOON_PATHS.map((path) =>
  createRoute({ getParentRoute: () => appRoute, path, component: SoonPage }),
);

const routeTree = rootRoute.addChildren([
  loginRoute,
  logoutRoute,
  tenantRoute,
  passwordRoute,
  appChooserRoute,
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
    programsRoute,
    programCreateRoute,
    programDetailRoute,
    planDetailRoute,
    planVersionRoute,
    adjustmentsRoute,
    importsRoute,
    importUploadRoute,
    importDetailRoute,
    contractsRoute,
    contractCreateRoute,
    contractDetailRoute,
    contractVersionRoute,
    pricingRoute,
    ruleSetsRoute,
    ruleSetDetailRoute,
    ruleSetVersionRoute,
    providersRoute,
    providerCreateRoute,
    providerDetailRoute,
    categoryTreeRoute,
    definitionListRoute,
    definitionCreateRoute,
    definitionDetailRoute,
    codeSystemListRoute,
    codeSystemDetailRoute,
    requestsRoute,
    requestCreateRoute,
    requestDetailRoute,
    claimsRoute,
    claimDetailRoute,
    lodgingPropertiesRoute,
    lodgingPropertyRoute,
    lodgingBookingsRoute,
    lodgingBookingRoute,
    lodgingWaitlistRoute,
    billingBatchesRoute,
    billingBatchRoute,
    billingSettlementsRoute,
    billingSettlementRoute,
    billingReimbursementsRoute,
    billingReimbursementRoute,
    billingReconciliationRoute,
    billingReconciliationRunRoute,
    billingExportsRoute,
    medicalReportsRoute,
    medicalReportRoute,
    worklistRoute,
    notificationsRoute,
    ...soonRoutes,
  ]),
]);

export function createAppRouter(services: AppServices, history?: RouterHistory) {
  return createRouter({
    basepath: import.meta.env.BASE_URL,
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
