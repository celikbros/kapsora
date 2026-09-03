import { Toast as RadixToast } from 'radix-ui';
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';
import { cn } from './cn';

export type ToastTone = 'info' | 'success' | 'warning' | 'danger';

export interface ToastMessage {
  id: number;
  tone: ToastTone;
  title: string;
  description?: string;
}

interface ToastApi {
  notify(input: { tone?: ToastTone; title: string; description?: string }): void;
}

const ToastContext = createContext<ToastApi | null>(null);

const tones: Record<ToastTone, string> = {
  info: 'bg-info-soft border-info/40',
  success: 'bg-success-soft border-success/40',
  warning: 'bg-warning-soft border-warning/40',
  danger: 'bg-danger-soft border-danger/40',
};

/** Provides `useToast()`; mount once near the app root. */
export function ToastProvider({
  children,
  closeLabel = 'Kapat',
}: {
  children: ReactNode;
  closeLabel?: string;
}) {
  const [items, setItems] = useState<ToastMessage[]>([]);
  const notify = useCallback<ToastApi['notify']>((input) => {
    setItems((list) => [
      ...list,
      {
        id: Date.now() + Math.random(),
        tone: input.tone ?? 'info',
        title: input.title,
        ...(input.description ? { description: input.description } : {}),
      },
    ]);
  }, []);
  const api = useMemo(() => ({ notify }), [notify]);

  return (
    <ToastContext.Provider value={api}>
      <RadixToast.Provider swipeDirection="right" duration={6000}>
        {children}
        {items.map((item) => (
          <RadixToast.Root
            key={item.id}
            className={cn(
              'k-toast text-fg shadow-card grid gap-1 rounded-md border p-3 pr-8',
              tones[item.tone],
            )}
            onOpenChange={(open) => {
              if (!open) setItems((list) => list.filter((x) => x.id !== item.id));
            }}
          >
            <RadixToast.Title className="text-sm font-semibold">{item.title}</RadixToast.Title>
            {item.description ? (
              <RadixToast.Description className="text-sm">
                {item.description}
              </RadixToast.Description>
            ) : null}
            <RadixToast.Close
              className="absolute top-2 right-2 rounded p-1 text-xs"
              aria-label={closeLabel}
            >
              ×
            </RadixToast.Close>
          </RadixToast.Root>
        ))}
        <RadixToast.Viewport className="fixed right-4 bottom-4 z-50 flex w-[min(92vw,22rem)] flex-col gap-2 outline-none" />
      </RadixToast.Provider>
    </ToastContext.Provider>
  );
}

/** Queue a toast from anywhere under the provider. */
export function useToast(): ToastApi {
  const api = useContext(ToastContext);
  if (!api) {
    throw new Error('useToast must be used inside <ToastProvider>');
  }
  return api;
}
