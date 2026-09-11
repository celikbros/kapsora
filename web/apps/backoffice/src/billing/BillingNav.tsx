import { useTranslation } from '@kapsora/i18n';
import { Link } from '@tanstack/react-router';
import { useEffect, useRef } from 'react';

/**
 * The billing section's five lists on one line: the icmals to decide, the settlements to
 * approve and pay, the reimbursements to decide, the daily reconciliation, the exports. The
 * main navigation names the section once; this names its lists. On a phone the strip keeps
 * its one line and scrolls sideways, so the active underline always sits on the rule.
 */
export function BillingNav() {
  const { t } = useTranslation();
  const strip = useRef<HTMLElement>(null);
  // On a phone the strip scrolls; the list the reader is on is scrolled into view so the
  // underline that says where they are is never off the edge.
  useEffect(() => {
    const bring = () => {
      const active = strip.current?.querySelector<HTMLElement>('[aria-current="page"]');
      active?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
    };
    bring();
    // A phone turned sideways, or a window narrowed: the strip starts scrolling, so look again.
    window.addEventListener('resize', bring);
    return () => window.removeEventListener('resize', bring);
  }, []);
  const tab =
    'aria-[current=page]:border-primary aria-[current=page]:text-primary text-fg-muted -mb-px shrink-0 border-b-2 border-transparent px-3 py-2 text-sm whitespace-nowrap';
  return (
    <nav
      ref={strip}
      aria-label={t('nav.billing')}
      className="border-line flex min-w-0 overflow-x-auto border-b"
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
      <Link to="/billing/reconciliation" className={tab}>
        {t('billing.report.reconciliationTitle')}
      </Link>
      <Link to="/billing/exports" className={tab}>
        {t('billing.report.exportsTitle')}
      </Link>
    </nav>
  );
}
