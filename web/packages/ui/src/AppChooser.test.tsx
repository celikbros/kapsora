import { initI18n } from '@kapsora/i18n';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { AppChooser } from './AppChooser';

const urls = { backoffice: '/app', provider: '/portal', member: '/member' };

it('refreshes waiting access and uses accurate copy when the refreshed account has one app', async () => {
  initI18n('tr');
  const refresh = vi.fn(async () => {});
  const { rerender } = render(
    <AppChooser
      current="backoffice"
      fits={[]}
      urls={urls}
      onSignOut={() => {}}
      pendingOrganizations={['Örnek Kurum']}
      onRefreshAccess={refresh}
    />,
  );
  expect(screen.getByText('Örnek Kurum')).toBeInTheDocument();
  await userEvent.setup().click(screen.getByRole('button', { name: 'Erişimi yenile' }));
  expect(refresh).toHaveBeenCalledTimes(1);
  rerender(
    <AppChooser
      current="backoffice"
      fits={['backoffice']}
      urls={urls}
      onContinue={() => {}}
      onSignOut={() => {}}
    />,
  );
  expect(screen.getByText('Çalışmanıza devam edin')).toBeInTheDocument();
  expect(screen.queryByText(/birden fazla uygulamada/)).not.toBeInTheDocument();
});
