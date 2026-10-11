// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { canDiscoverWallets, SOON_PATHS, visibleNavEntries } from './nav';
import { WalletsPage } from './pages/WalletsPage';

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

describe('wallet discovery', () => {
  it('lets member and entitlement readers reach the member search', () => {
    activePermissions = ['member.read', 'entitlement.read'];
    render(<WalletsPage />);
    expect(screen.getByTestId('wallets-people').getAttribute('href')).toBe('/people');
    expect(
      visibleNavEntries(activePermissions).find((entry) => entry.key === 'wallets')?.implemented,
    ).toBe(true);
    expect(SOON_PATHS).not.toContain('/wallets');
  });

  it.each([
    ['finance without entitlement read', ['member.read', 'report.read']],
    ['auditor without member read', ['entitlement.read', 'audit.read']],
  ])('denies %s before exposing any wallet link', (_role, permissions) => {
    activePermissions = permissions;
    render(<WalletsPage />);
    expect(screen.queryByTestId('wallets-people')).toBeNull();
    expect(screen.getByText('problems.PERMISSION_DENIED')).toBeTruthy();
    expect(canDiscoverWallets(permissions)).toBe(false);
    expect(visibleNavEntries(permissions).some((entry) => entry.key === 'wallets')).toBe(false);
  });
});
