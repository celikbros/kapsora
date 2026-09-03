import { initI18n } from '@kapsora/i18n';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { createServices } from './api';
import './app.css';

async function start() {
  initI18n('tr');
  // Mocks are on unless VITE_API_MOCK=false; the real API is reached through the Vite proxy.
  if (import.meta.env['VITE_API_MOCK'] !== 'false') {
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
