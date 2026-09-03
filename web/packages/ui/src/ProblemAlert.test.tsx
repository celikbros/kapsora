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
});
