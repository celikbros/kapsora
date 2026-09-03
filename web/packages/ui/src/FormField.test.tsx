import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { FormField } from './FormField';
import { Input } from './Input';

describe('FormField', () => {
  it('labels the control and wires hint and error via aria-describedby', () => {
    render(
      <FormField label="Ticari unvan" required hint="Resmî kayıttaki ad" error="Zorunlu alan">
        <Input name="legalName" />
      </FormField>,
    );
    const input = screen.getByLabelText(/Ticari unvan/);
    expect(input).toHaveAttribute('aria-required', 'true');
    expect(input).toHaveAttribute('aria-invalid', 'true');
    const described = input.getAttribute('aria-describedby')!.split(' ');
    expect(described).toHaveLength(2);
    expect(screen.getByRole('alert')).toHaveTextContent('Zorunlu alan');
    expect(screen.getByText('Resmî kayıttaki ad')).toHaveAttribute('id', described[0]);
  });

  it('omits aria-invalid and the alert when there is no error', () => {
    render(
      <FormField label="Kod">
        <Input name="code" />
      </FormField>,
    );
    const input = screen.getByLabelText('Kod');
    expect(input).not.toHaveAttribute('aria-invalid');
    expect(screen.queryByRole('alert')).toBeNull();
  });
});
