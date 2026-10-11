// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ReportsPage } from './pages/ReportsPage';
import { BillingNav } from './billing/BillingNav';
import { SOON_PATHS, visibleNavEntries } from './nav';

let activePermissions: string[] = [];

vi.mock('@kapsora/auth', () => ({
  useSession: (selector: (state: unknown) => unknown) =>
    selector({ activeTenant: { permissions: activePermissions } }),
}));
vi.mock('@kapsora/i18n', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock('@kapsora/ui', () => ({
  PageHeader: ({ title }: { title: string }) => <h1>{title}</h1>,
  EmptyState: ({ title }: { title: string }) => <p>{title}</p>,
}));
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children, ...props }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}));

afterEach(cleanup);

describe('Reports entry', () => {
  it('opens existing report lists for a read-only HR role', () => {
    activePermissions = ['member.read', 'invoice.read', 'report.read'];
    render(<ReportsPage />);
    expect(screen.getByTestId('reports-services')).toBeTruthy();
    expect(screen.getByTestId('reports-reconciliation').getAttribute('href')).toBe(
      '/billing/reconciliation',
    );
    expect(screen.getByTestId('reports-exports').getAttribute('href')).toBe('/billing/exports');
    expect(
      visibleNavEntries(activePermissions).find((entry) => entry.key === 'reports')?.implemented,
    ).toBe(true);
    expect(SOON_PATHS).not.toContain('/reports');
  });

  it('hides links on direct visits without report.read', () => {
    activePermissions = ['health.medical_report.review'];
    render(<ReportsPage />);
    expect(screen.getByText('problems.PERMISSION_DENIED')).toBeTruthy();
    expect(screen.queryByTestId('reports-reconciliation')).toBeNull();
    expect(screen.queryByTestId('reports-exports')).toBeNull();
    expect(visibleNavEntries(activePermissions).some((entry) => entry.key === 'reports')).toBe(
      false,
    );
  });

  it('shows finance lists only when their read endpoints are permitted', () => {
    activePermissions = [
      'invoice.read',
      'settlement.read',
      'claim.financial.review',
      'report.read',
      'report.export',
    ];
    render(<BillingNav />);
    expect(screen.getByRole('link', { name: 'billing.office.batchesTitle' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'billing.office.settlementsTitle' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'billing.office.reimbursementsTitle' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'billing.report.reconciliationTitle' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'billing.report.exportsTitle' })).toBeTruthy();
  });

  it('keeps finance-only billing tabs out of the read-only HR menu', () => {
    activePermissions = ['invoice.read', 'report.read'];
    render(<BillingNav />);
    expect(screen.getAllByRole('link')).toHaveLength(3);
    expect(screen.queryByRole('link', { name: 'billing.office.settlementsTitle' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'billing.office.reimbursementsTitle' })).toBeNull();
  });
});
