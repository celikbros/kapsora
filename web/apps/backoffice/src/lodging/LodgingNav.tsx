import { useTranslation } from '@kapsora/i18n';
import { Link } from '@tanstack/react-router';

/**
 * The lodging section's three lists on one line: bookings, the waiting list, the
 * properties. A tab strip on a line, as DESIGN.md settles it — the main navigation names
 * the section once and this names its lists.
 */
export function LodgingNav() {
  const { t } = useTranslation();
  const tab =
    'aria-[current=page]:border-primary aria-[current=page]:text-primary text-fg-muted -mb-px border-b-2 border-transparent px-3 py-2 text-sm';
  return (
    <nav
      aria-label={t('nav.lodging')}
      className="border-line flex border-b"
      data-testid="lodging-nav"
    >
      <Link to="/lodging/bookings" className={tab}>
        {t('lodging.office.bookings.title')}
      </Link>
      <Link to="/lodging/waitlist" className={tab}>
        {t('lodging.office.waitlist.title')}
      </Link>
      <Link to="/lodging/properties" className={tab}>
        {t('lodging.office.properties.title')}
      </Link>
    </nav>
  );
}
