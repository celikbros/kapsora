import { SessionProvider } from '@kapsora/auth';
import { ToastProvider } from '@kapsora/ui';
import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider, type RouterHistory } from '@tanstack/react-router';
import { useMemo } from 'react';
import { ServicesProvider, type AppServices } from './api';
import { createAppRouter } from './router';

/** Root component; tests pass a memory history. */
export function App({ services, history }: { services: AppServices; history?: RouterHistory }) {
  const router = useMemo(() => createAppRouter(services, history), [services, history]);
  return (
    <ServicesProvider services={services}>
      <QueryClientProvider client={services.queryClient}>
        <SessionProvider store={services.store}>
          <ToastProvider>
            <RouterProvider router={router} />
          </ToastProvider>
        </SessionProvider>
      </QueryClientProvider>
    </ServicesProvider>
  );
}
