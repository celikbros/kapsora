import { initI18n } from '@kapsora/i18n';
import { render, screen } from '@testing-library/react';
import { beforeAll, describe, expect, it } from 'vitest';
import { ProblemAlert } from './ProblemAlert';

beforeAll(() => {
  initI18n('tr');
});

describe('ProblemAlert', () => {
  it('shows the Turkish message for the code, field errors and the trace id', () => {
    render(
      <ProblemAlert
        problem={{
          code: 'VALIDATION_FAILED',
          title: 'Doğrulama hatası',
          status: 422,
          traceId: 'trace-42',
          errors: [{ field: 'identifiers[0].value', code: 'IDENTIFIER_INVALID' }],
        }}
      />,
    );
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Formda düzeltilmesi gereken alanlar var.');
    expect(alert).toHaveTextContent('identifiers[0].value');
    expect(alert).toHaveTextContent('kontrol basamağı hatalı');
    expect(alert).toHaveTextContent('trace-42');
    expect(alert).toHaveAttribute('data-problem-code', 'VALIDATION_FAILED');
  });

  it('falls back to the server title for unknown codes and renders nothing without a problem', () => {
    const { rerender } = render(
      <ProblemAlert
        problem={{ code: 'NEW_THING', title: 'Sunucudan gelen başlık', status: 409, traceId: '' }}
      />,
    );
    expect(screen.getByRole('alert')).toHaveTextContent('Sunucudan gelen başlık');
    rerender(<ProblemAlert problem={null} />);
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('turns a refusal of the page itself into a page message with the way home', () => {
    const retry = <button type="button">Yeniden dene</button>;
    const refusal = { code: 'PERMISSION_DENIED', title: 'x', status: 403, traceId: 't-1' };
    const { rerender } = render(<ProblemAlert page problem={refusal} actions={retry} />);
    expect(screen.getByRole('alert')).toHaveTextContent('Bu sayfayı görme yetkiniz yok.');
    expect(screen.getByRole('link', { name: 'Ana sayfaya dön' })).toHaveAttribute('href', '/');
    expect(screen.queryByRole('button', { name: 'Yeniden dene' })).toBeNull();

    // Any other failure of a page keeps its retry: a server error may pass.
    rerender(
      <ProblemAlert
        page
        problem={{ code: 'INTERNAL_ERROR', title: 'x', status: 500, traceId: 't-2' }}
        actions={retry}
      />,
    );
    expect(screen.getByRole('button', { name: 'Yeniden dene' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Ana sayfaya dön' })).toBeNull();

    // And a refused action is still a refused action.
    rerender(<ProblemAlert problem={refusal} />);
    expect(screen.getByRole('alert')).toHaveTextContent('Bu işlem için yetkiniz yok.');
  });
});
