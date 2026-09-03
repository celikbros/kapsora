import { DropdownMenu as Radix } from 'radix-ui';
import type { ReactNode } from 'react';
import { cn } from './cn';

export interface MenuItem {
  key: string;
  label: string;
  onSelect: () => void;
  danger?: boolean;
  disabled?: boolean;
}

export interface DropdownMenuProps {
  /** The trigger button; it gets aria-haspopup/expanded from Radix. */
  trigger: ReactNode;
  items: MenuItem[];
  /** Optional heading rendered above the items (e.g. the user's name). */
  heading?: ReactNode;
  align?: 'start' | 'end';
}

/** Keyboard-navigable menu (arrow keys, Escape, typeahead) built on Radix. */
export function DropdownMenu({ trigger, items, heading, align = 'end' }: DropdownMenuProps) {
  return (
    <Radix.Root>
      <Radix.Trigger asChild>{trigger}</Radix.Trigger>
      <Radix.Portal>
        <Radix.Content
          align={align}
          sideOffset={6}
          className="bg-surface-raised text-fg border-line shadow-card z-50 min-w-48 rounded-md border p-1 text-sm"
        >
          {heading ? <div className="text-fg-muted px-2 py-1.5 text-xs">{heading}</div> : null}
          {heading ? <Radix.Separator className="bg-line my-1 h-px" /> : null}
          {items.map((item) => (
            <Radix.Item
              key={item.key}
              {...(item.disabled !== undefined ? { disabled: item.disabled } : {})}
              onSelect={item.onSelect}
              className={cn(
                'cursor-pointer rounded px-2 py-1.5 outline-none select-none',
                'data-highlighted:bg-surface-sunken data-disabled:opacity-50',
                item.danger && 'text-danger',
              )}
            >
              {item.label}
            </Radix.Item>
          ))}
        </Radix.Content>
      </Radix.Portal>
    </Radix.Root>
  );
}
