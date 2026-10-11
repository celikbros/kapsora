import { registerStatementAcceptance } from './real-statement-acceptance';

registerStatementAcceptance({
  title: 'paid lodging invoice appears on its hotel statement and in its worker CSV',
  environmentPrefix: 'E2E_LODGING_BILLING',
  otherProviderEnvironment: 'E2E_LODGING_BILLING_FOREIGN_PROVIDER',
  requireOtherProvider: true,
  username: 'hotel.billing.a',
  domain: 'ACCOMMODATION',
  total: '1800',
  paidText: '1.800,00',
  evidencePrefix: 'pc06',
  screenshots: '.impeccable/review/lodging-billing',
});
