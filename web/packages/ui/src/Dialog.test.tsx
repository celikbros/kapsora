import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { Button } from './Button';
import { Dialog } from './Dialog';

describe('Dialog', () => {
  it('is labelled by its title, traps focus on open and closes with Escape', async () => {
    const onOpenChange = vi.fn();
    render(
      <Dialog
        open
        onOpenChange={onOpenChange}
        title="Kaydı sil"
        description="Bu işlem geri alınamaz."
        actions={<Button>Onayla</Button>}
      >
        <p>Gövde</p>
      </Dialog>,
    );
    const dialog = screen.getByRole('dialog', { name: 'Kaydı sil' });
    expect(dialog).toHaveAccessibleDescription('Bu işlem geri alınamaz.');
    expect(dialog.contains(document.activeElement)).toBe(true);
    await userEvent.keyboard('{Escape}');
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
