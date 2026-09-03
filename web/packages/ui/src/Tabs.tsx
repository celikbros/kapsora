import { Tabs as Radix } from 'radix-ui';
import type { ReactNode } from 'react';

import { cn } from './cn';

export interface TabDefinition {
  value: string;
  label: string;
  /** Rendered when the tab is active. */
  content: ReactNode;
  /** Hidden entirely when false, for tabs a permission does not allow. */
  visible?: boolean;
}

export interface TabsProps {
  tabs: TabDefinition[];
  value: string;
  onValueChange: (value: string) => void;
  ariaLabel: string;
}

/**
 * Horizontal tab set on Radix: arrow keys move between tabs, the panel is labelled by its
 * trigger, and only the active panel is mounted.
 */
export function Tabs({ tabs, value, onValueChange, ariaLabel }: TabsProps) {
  const shown = tabs.filter((tab) => tab.visible !== false);
  return (
    <Radix.Root value={value} onValueChange={onValueChange}>
      <Radix.List aria-label={ariaLabel} className="border-line mb-4 flex flex-wrap gap-1 border-b">
        {shown.map((tab) => (
          <Radix.Trigger
            key={tab.value}
            value={tab.value}
            className={cn(
              'text-fg-muted -mb-px border-b-2 border-transparent px-3 py-2 text-sm font-medium',
              'hover:text-fg data-[state=active]:border-primary data-[state=active]:text-primary-strong',
            )}
          >
            {tab.label}
          </Radix.Trigger>
        ))}
      </Radix.List>
      {shown.map((tab) => (
        <Radix.Content key={tab.value} value={tab.value} className="outline-none">
          {tab.content}
        </Radix.Content>
      ))}
    </Radix.Root>
  );
}
