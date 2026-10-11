import { useTranslation } from '@kapsora/i18n';
import { Button } from '@kapsora/ui';
import { useTenantUser } from './queries';

/** Names come from the authorized directory; request evidence contains membership IDs only. */
export function RoleChangePerson({ membershipId }: { membershipId: string }) {
  const { t } = useTranslation();
  const query = useTenantUser(membershipId);
  return (
    <span className="min-w-0 break-words">
      {query.data?.data.membership.displayName ??
        t('roleChanges.memberReference', { id: membershipId })}
      {query.isError && (
        <Button variant="secondary" onClick={() => void query.refetch()}>
          {t('roleChanges.reloadName')}
        </Button>
      )}
    </span>
  );
}
