import { initI18n } from '@kapsora/i18n';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeAll, describe, expect, it } from 'vitest';

import { FormField } from './FormField';
import { PasswordInput } from './PasswordInput';

beforeAll(() => {
  initI18n('tr');
});

describe('PasswordInput', () => {
  it('hides the value until the reader asks, and the label still names the field', async () => {
    const user = userEvent.setup();
    render(
      <FormField label="Parola" required>
        <PasswordInput name="password" defaultValue="demo parola 2026 kapsora" />
      </FormField>,
    );
    const field = screen.getByLabelText(/^Parola/);
    expect(field).toHaveAttribute('type', 'password');

    const toggle = screen.getByRole('button', { name: 'Göster' });
    expect(toggle).toHaveAttribute('aria-pressed', 'false');
    expect(toggle).toHaveAttribute('aria-controls', field.id);

    await user.click(toggle);
    expect(field).toHaveAttribute('type', 'text');
    expect(screen.getByRole('button', { name: 'Gizle' })).toHaveAttribute('aria-pressed', 'true');
    // Showing changes the type, never the value.
    expect(field).toHaveValue('demo parola 2026 kapsora');

    await user.click(screen.getByRole('button', { name: 'Gizle' }));
    expect(field).toHaveAttribute('type', 'password');
  });

  it('is the only thing the label finds, so a lookup by label stays single', () => {
    render(
      <FormField label="Parola" required>
        <PasswordInput name="password" />
      </FormField>,
    );
    expect(screen.getAllByLabelText(/^Parola/)).toHaveLength(1);
  });
});
