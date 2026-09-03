import { initI18n } from '@kapsora/i18n';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { createServices } from './services';
import './app.css';

async function start() {
  initI18n('tr');
  if (import.meta.env['VITE_API_MOCK'] !== 'false') {
    const { startMockApi } = await import('@kapsora/api-client/mocks/browser');
    await startMockApi();
  }
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App services={createServices()} />
    </StrictMode>,
  );
}

void start();
