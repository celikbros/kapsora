import { initI18n } from '@kapsora/i18n';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeAll, describe, expect, it, vi } from 'vitest';

import { DEMO_ACCOUNTS, DemoAccounts } from './DemoAccounts';
import { SignInLayout } from './SignInLayout';

beforeAll(() => {
  initI18n('tr');
});

describe('the sign-in page', () => {
  it('says what KAPSORA is, names the three apps and marks the one it belongs to', () => {
    render(
      <SignInLayout app="provider">
        <p>form</p>
      </SignInLayout>,
    );
    expect(screen.getByRole('heading', { level: 1, name: 'KAPSORA' })).toBeInTheDocument();
    expect(screen.getByText('Hak ve fayda defteri')).toBeInTheDocument();

    const apps = screen.getAllByRole('term').map((term) => term.textContent ?? '');
    expect(apps.map((text) => text.replace('Buradasınız', ''))).toEqual([
      'Yönetim paneli',
      'Sağlayıcı portalı',
      'Üye uygulaması',
    ]);
    // The mark is one word, on the app this screen is for, and nowhere else.
    expect(apps.filter((text) => text.includes('Buradasınız'))).toHaveLength(1);
    expect(apps[1]).toContain('Buradasınız');
    expect(screen.getByRole('main')).toContainElement(screen.getByText('form'));
  });
});

describe('the demo account list', () => {
  it('groups the accounts by app and signs in with one press', async () => {
    const onPick = vi.fn();
    render(<DemoAccounts accounts={DEMO_ACCOUNTS} onPick={onPick} />);
    const list = screen.getByTestId('demo-accounts');

    const groups = within(list)
      .getAllByRole('heading', { level: 3 })
      .map((heading) => heading.textContent);
    expect(groups).toEqual([
      'Yönetim paneli',
      'Sağlayıcı portalı',
      'Üye uygulaması',
      'Yönetim paneli ve üye uygulaması',
    ]);
    // The app is said once per group, never again on every row.
    expect(within(list).queryByText(/Yönetim paneli ·/)).toBeNull();

    // The row names itself: the person, what they do and the username all reach a reader.
    const row = within(list).getByRole('button', { name: /Fuat Mali Değerlendirici/ });
    expect(row).toHaveAccessibleName(/financial.reviewer/);
    await userEvent.click(row);
    expect(onPick).toHaveBeenCalledWith('financial.reviewer');
  });

  it('shows only the groups it was given accounts for', () => {
    render(
      <DemoAccounts
        accounts={DEMO_ACCOUNTS.filter((account) => account.app === 'member')}
        onPick={() => {}}
      />,
    );
    const list = screen.getByTestId('demo-accounts');
    expect(within(list).getAllByRole('heading', { level: 3 })).toHaveLength(1);
    expect(within(list).getByRole('heading', { level: 3 })).toHaveTextContent('Üye uygulaması');
  });
  it("puts the screen's own app first", () => {
    render(<DemoAccounts accounts={DEMO_ACCOUNTS} app="member" onPick={() => {}} />);
    const groups = within(screen.getByTestId('demo-accounts'))
      .getAllByRole('heading', { level: 3 })
      .map((heading) => heading.textContent);
    expect(groups[0]).toBe('Üye uygulaması');
  });
});
