import { registerStatementAcceptance } from './real-statement-acceptance';

registerStatementAcceptance({
  title: 'paid HEALTH invoice appears on the own-provider statement and in its worker CSV',
  environmentPrefix: 'E2E_HEALTH_BILLING',
  otherProviderEnvironment: 'E2E_OTHER_PROVIDER_ID',
  requireOtherProvider: false,
  username: 'billing.a',
  domain: 'HEALTH',
  total: '800',
  paidText: '800,00',
  evidencePrefix: 'pc05',
  screenshots: '.impeccable/review/health-billing',
});
