import type { RoleChangeRequest } from '@kapsora/api-client';
import { useTranslation } from '@kapsora/i18n';

export function roleTitle(
  code: string,
  t: (key: string, options: { defaultValue: string }) => string,
) {
  return t(`roleChanges.roles.${code}.name`, { defaultValue: code });
}

export function roleDuty(
  code: string,
  t: (key: string, options: { defaultValue: string }) => string,
) {
  return t(`roleChanges.roles.${code}.duty`, { defaultValue: code });
}

export function PermissionEvidence({ request }: { request: RoleChangeRequest }) {
  const { t } = useTranslation();
  return (
    <details className="border-line mt-4 rounded-md border p-3 text-sm">
      <summary className="cursor-pointer font-medium">
        {t('roleChanges.permissionEvidence')}
      </summary>
      <p className="text-fg-muted mt-2 break-all">
        {t('roleChanges.configurationHash')}: <code>{request.configurationHash}</code>
      </p>
      <ul className="mt-2 grid gap-1">
        {request.permissionSnapshot.map((item) => (
          <li key={item.code} className="flex min-w-0 flex-wrap justify-between gap-2">
            <code className="break-all">{item.code}</code>
            <span>{t(`roleChanges.sensitivity.${item.sensitivity}`)}</span>
          </li>
        ))}
      </ul>
    </details>
  );
}
