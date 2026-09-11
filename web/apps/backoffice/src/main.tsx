import { initI18n } from '@kapsora/i18n';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { createServices } from './api';
import './app.css';

async function start() {
  initI18n('tr');
  // The in-browser sample data runs on the development server only, and there unless
  // VITE_API_MOCK=false (then the real API is reached through the Vite proxy). A build never
  // carries it: a deployment built without the flag must not come up on fake data.
  if (import.meta.env.DEV && import.meta.env['VITE_API_MOCK'] !== 'false') {
    const { startMockApi } = await import('@kapsora/api-client/mocks/browser');
    await startMockApi();
  }
  const services = createServices();
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App services={services} />
    </StrictMode>,
  );
}

void start();
