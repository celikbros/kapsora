import { useTranslation } from '@kapsora/i18n';
import { Link } from '@tanstack/react-router';

/**
 * The billing section's three lists on one line: the icmals to decide, the settlements to
 * approve and pay, the reimbursements to decide. The main navigation names the section
 * once; this names its lists, as DESIGN.md settles a section tab strip.
 */
export function BillingNav() {
  const { t } = useTranslation();
  const tab =
    'aria-[current=page]:border-primary aria-[current=page]:text-primary text-fg-muted -mb-px border-b-2 border-transparent px-3 py-2 text-sm';
  return (
    <nav
      aria-label={t('nav.billing')}
      className="border-line flex border-b"
      data-testid="billing-nav"
    >
      <Link to="/billing/batches" className={tab}>
        {t('billing.office.batchesTitle')}
      </Link>
      <Link to="/billing/settlements" className={tab}>
        {t('billing.office.settlementsTitle')}
      </Link>
      <Link to="/billing/reimbursements" className={tab}>
        {t('billing.office.reimbursementsTitle')}
      </Link>
    </nav>
  );
}
