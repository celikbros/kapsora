import {
  ApiError,
  randomId,
  type AccommodationTenantSettings,
  type SetAccommodationTenantSettingsRequest,
  type TenantSettingInteger,
  type Versioned,
} from '@kapsora/api-client';
import { useSession, useSessionStore, useStepUp, useTenantId } from '@kapsora/auth';
import { useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Dialog,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  StepUpDialog,
} from '@kapsora/ui';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { directoryContextKey, useOps } from '../api';
import { problemOf } from '../problems';

type Reason = SetAccommodationTenantSettingsRequest['reasonCode'];
type Draft = { holdMinutes: string; quoteTtlMinutes: string; reasonCode: Reason | '' };
type Command = {
  context: string;
  revision: number;
  csrfToken: string | null;
  tenantId: string;
  tenantName: string;
  before: AccommodationTenantSettings;
  body: Readonly<SetAccommodationTenantSettingsRequest>;
  etag: string;
  key: string;
};

const blank: Draft = { holdMinutes: '', quoteTtlMinutes: '', reasonCode: '' };
function valuesOf(settings: AccommodationTenantSettings): Draft {
  return {
    holdMinutes: String(settings.holdMinutes.effectiveValue),
    quoteTtlMinutes: String(settings.quoteTtlMinutes.effectiveValue),
    reasonCode: '',
  };
}

function integer(raw: string, setting: TenantSettingInteger): number | null {
  if (!/^[0-9]+$/.test(raw)) return null;
  const value = Number(raw);
  return Number.isSafeInteger(value) && value >= setting.minimum && value <= setting.maximum
    ? value
    : null;
}

function sourceText(source: TenantSettingInteger['source'], t: (key: string) => string) {
  return t(`tenantSettings.source.${source}`);
}

function SettingRow({
  label,
  effect,
  setting,
  value,
  onChange,
  editable,
  disabled,
  error,
}: {
  label: string;
  effect: string;
  setting: TenantSettingInteger;
  value: string;
  onChange: (value: string) => void;
  editable: boolean;
  disabled: boolean;
  error: string | null;
}) {
  const { t } = useTranslation();
  return (
    <div className="border-line grid min-w-0 gap-3 border-t py-5 first:border-t-0 sm:grid-cols-[minmax(0,1fr)_12rem] sm:gap-6">
      <div className="min-w-0">
        <h3 className="text-base font-semibold">{label}</h3>
        <p className="text-fg-muted mt-1 max-w-prose text-sm">{effect}</p>
        <div className="mt-2 flex flex-wrap items-center gap-2 text-sm">
          <Badge tone={setting.source === 'INVALID_FALLBACK' ? 'warning' : 'neutral'}>
            {sourceText(setting.source, t)}
          </Badge>
          <span className="text-fg-muted">
            {t('tenantSettings.defaultValue')}: {setting.defaultValue} {t('tenantSettings.minutes')}
          </span>
        </div>
        {setting.source === 'INVALID_FALLBACK' && (
          <p className="text-warning mt-2 text-sm">{t('tenantSettings.invalidFallback')}</p>
        )}
      </div>
      <div className="min-w-0">
        {editable ? (
          <FormField
            label={t('tenantSettings.valueLabel', { name: label })}
            hint={t('tenantSettings.range', {
              minimum: setting.minimum,
              maximum: setting.maximum,
            })}
            error={error ?? undefined}
          >
            <Input
              type="number"
              inputMode="numeric"
              min={setting.minimum}
              max={setting.maximum}
              step={1}
              value={value}
              onChange={(event) => onChange(event.target.value)}
              disabled={disabled}
            />
          </FormField>
        ) : (
          <p className="text-right text-xl font-semibold tabular-nums sm:pt-2">
            {setting.effectiveValue}{' '}
            <span className="text-fg-muted text-sm font-normal">{t('tenantSettings.minutes')}</span>
          </p>
        )}
      </div>
    </div>
  );
}

export function TenantSettingsPage() {
  const { t } = useTranslation();
  const ops = useOps();
  const store = useSessionStore();
  const queryClient = useQueryClient();
  const stepUp = useStepUp();
  const stepUpRef = useRef(stepUp);
  useEffect(() => {
    stepUpRef.current = stepUp;
  }, [stepUp]);
  const tenantId = useTenantId();
  const context = useSession(directoryContextKey);
  const tenantName = useSession((s) => s.activeTenant?.tenant.displayName ?? '');
  const canRead = useSession((s) => s.activeTenant?.canReadTenantSettings === true);
  const canManage = useSession((s) => s.activeTenant?.canManageTenantSettings === true);
  const [draft, setDraft] = useState<Draft>(blank);
  const [draftVersion, setDraftVersion] = useState('');
  const [validation, setValidation] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [command, setCommand] = useState<Command | null>(null);
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [cancelled, setCancelled] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [success, setSuccess] = useState<Versioned<AccommodationTenantSettings> | null>(null);
  const [saved, setSaved] = useState(false);
  const [refreshFailed, setRefreshFailed] = useState(false);
  const revision = useRef(0);
  const mounted = useRef(true);
  const inFlight = useRef<Command | null>(null);
  const read = useQuery({
    queryKey: ['admin-tenant-settings', context],
    queryFn: () => ops.admin.getAccommodationTenantSettings(tenantId),
    enabled: canRead,
    retry: false,
  });
  const visible = success ?? read.data;
  const currentDraft =
    visible && draftVersion === visible.etag ? draft : visible ? valuesOf(visible.data) : blank;
  const editable = canManage && visible?.data.canManage === true;
  const locked = busy || !!command || conflict || refreshFailed;

  useEffect(() => {
    mounted.current = true;
    let previous = directoryContextKey(store.getState());
    const unsubscribe = store.subscribe((state) => {
      const next = directoryContextKey(state);
      if (next === previous) return;
      previous = next;
      revision.current += 1;
      stepUpRef.current.cancel();
      inFlight.current = null;
      setDraft(blank);
      setDraftVersion('');
      setValidation(false);
      setConfirmOpen(false);
      setCommand(null);
      setBusy(false);
      setUncertain(false);
      setCancelled(false);
      setConflict(false);
      setProblem(null);
      setSuccess(null);
      setSaved(false);
      setRefreshFailed(false);
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [store]);

  function current(snapshot: Command) {
    const state = store.getState();
    return (
      mounted.current &&
      revision.current === snapshot.revision &&
      state.status === 'authenticated' &&
      state.csrfToken === snapshot.csrfToken &&
      state.activeTenant?.tenant.id === snapshot.tenantId &&
      state.activeTenant?.canReadTenantSettings === true &&
      state.activeTenant.canManageTenantSettings === true &&
      directoryContextKey(state) === snapshot.context
    );
  }

  async function send(snapshot: Command) {
    if (inFlight.current || !current(snapshot)) return;
    inFlight.current = snapshot;
    setBusy(true);
    setProblem(null);
    setCancelled(false);
    try {
      const result = await stepUp.run(() => {
        if (!current(snapshot)) throw new Error('Authorization context changed');
        return ops.admin.setAccommodationTenantSettings(
          snapshot.tenantId,
          snapshot.body,
          snapshot.etag,
          snapshot.key,
        );
      });
      if (!current(snapshot)) return;
      if (!result) {
        setCancelled(true);
        return;
      }
      // The committed command result remains visible even when the subsequent read fails.
      setSuccess(result);
      setSaved(true);
      setCommand(null);
      setUncertain(false);
      setCancelled(false);
      setConflict(false);
      setConfirmOpen(false);
      setDraftVersion(result.etag);
      setDraft(valuesOf(result.data));
      try {
        const refreshed = await ops.admin.getAccommodationTenantSettings(snapshot.tenantId);
        if (!current(snapshot)) return;
        queryClient.setQueryData(['admin-tenant-settings', snapshot.context], refreshed);
        setSuccess(null);
        setDraftVersion(refreshed.etag);
        setRefreshFailed(false);
      } catch {
        if (current(snapshot)) setRefreshFailed(true);
      }
    } catch (error) {
      if (!current(snapshot)) return;
      const status = error instanceof ApiError ? error.status : 0;
      const code = error instanceof ApiError ? error.problem.code : '';
      setProblem(problemOf(error));
      if (
        status === 0 ||
        status === 408 ||
        status === 429 ||
        status >= 500 ||
        code === 'IDEMPOTENCY_IN_PROGRESS'
      ) {
        setUncertain(true);
      } else if (status === 409 || status === 412 || status === 428 || code === 'ETAG_MISMATCH') {
        setConflict(true);
        setCommand(null);
        setUncertain(false);
        setConfirmOpen(false);
      } else {
        setCommand(null);
        setConfirmOpen(false);
      }
    } finally {
      if (inFlight.current === snapshot) {
        inFlight.current = null;
        if (current(snapshot)) setBusy(false);
      }
    }
  }

  async function reload() {
    if (busy) return;
    const expectedContext = directoryContextKey(store.getState());
    const expectedRevision = revision.current;
    try {
      const result = await ops.admin.getAccommodationTenantSettings(tenantId);
      if (
        !mounted.current ||
        expectedRevision !== revision.current ||
        expectedContext !== directoryContextKey(store.getState())
      )
        return;
      queryClient.setQueryData(['admin-tenant-settings', context], result);
      setSuccess(null);
      setRefreshFailed(false);
      setConflict(false);
      setProblem(null);
      setCommand(null);
      setUncertain(false);
      setCancelled(false);
      setConfirmOpen(false);
      setDraft(valuesOf(result.data));
      setDraftVersion(result.etag);
      setValidation(false);
    } catch (error) {
      if (mounted.current && expectedRevision === revision.current) setProblem(problemOf(error));
    }
  }

  const hold = visible ? integer(currentDraft.holdMinutes, visible.data.holdMinutes) : null;
  const quote = visible
    ? integer(currentDraft.quoteTtlMinutes, visible.data.quoteTtlMinutes)
    : null;
  const valid = hold !== null && quote !== null && !!currentDraft.reasonCode;
  const changed =
    !!visible &&
    (hold !== visible.data.holdMinutes.effectiveValue ||
      quote !== visible.data.quoteTtlMinutes.effectiveValue ||
      visible.data.holdMinutes.source !== 'CONFIGURED' ||
      visible.data.quoteTtlMinutes.source !== 'CONFIGURED');

  function prepare(event: FormEvent) {
    event.preventDefault();
    setValidation(true);
    if (
      !valid ||
      !changed ||
      !visible ||
      !editable ||
      locked ||
      !visible.etag ||
      !currentDraft.reasonCode
    )
      return;
    const state = store.getState();
    if (state.status !== 'authenticated' || state.activeTenant?.canManageTenantSettings !== true)
      return;
    const body: SetAccommodationTenantSettingsRequest = Object.freeze({
      holdMinutes: hold!,
      quoteTtlMinutes: quote!,
      reasonCode: currentDraft.reasonCode,
    });
    const snapshot: Command = {
      context: directoryContextKey(state),
      revision: revision.current,
      csrfToken: state.csrfToken,
      tenantId: state.activeTenant.tenant.id,
      tenantName: state.activeTenant.tenant.displayName,
      before: visible.data,
      body,
      etag: visible.etag,
      key: randomId(),
    };
    setCommand(snapshot);
    setSaved(false);
    setProblem(null);
    setConfirmOpen(true);
  }

  return (
    <section data-testid="tenant-settings-page" className="min-w-0">
      <PageHeader title={t('tenantSettings.title')} description={t('tenantSettings.intro')} />
      <p className="text-fg-muted mb-6 text-sm">
        {t('tenantSettings.institution')}: <strong className="text-fg">{tenantName}</strong>
      </p>
      {read.isPending && !visible ? (
        <p aria-busy="true" className="flex items-center gap-2 text-sm">
          <Spinner /> {t('common.loading')}
        </p>
      ) : read.isError && !visible ? (
        <ProblemAlert
          page
          problem={problemOf(read.error)}
          actions={
            <Button variant="secondary" onClick={() => void reload()}>
              {t('common.retry')}
            </Button>
          }
        />
      ) : !visible ? (
        <p className="text-fg-muted text-sm">{t('tenantSettings.empty')}</p>
      ) : (
        <>
          <div className="border-line bg-surface-raised rounded-lg border px-4 sm:px-6">
            <div className="border-line border-b py-5">
              <h2 className="text-lg font-semibold">{t('tenantSettings.accommodation')}</h2>
              <p className="text-fg-muted mt-1 max-w-prose text-sm">
                {t('tenantSettings.accommodationIntro')}
              </p>
            </div>
            <form onSubmit={prepare}>
              <SettingRow
                label={t('tenantSettings.holdMinutes')}
                effect={t('tenantSettings.holdEffect')}
                setting={visible.data.holdMinutes}
                value={currentDraft.holdMinutes}
                onChange={(value) => {
                  setDraft({ ...currentDraft, holdMinutes: value });
                  setDraftVersion(visible.etag);
                }}
                editable={editable}
                disabled={locked}
                error={validation && hold === null ? t('tenantSettings.invalidValue') : null}
              />
              <SettingRow
                label={t('tenantSettings.quoteTtlMinutes')}
                effect={t('tenantSettings.quoteEffect')}
                setting={visible.data.quoteTtlMinutes}
                value={currentDraft.quoteTtlMinutes}
                onChange={(value) => {
                  setDraft({ ...currentDraft, quoteTtlMinutes: value });
                  setDraftVersion(visible.etag);
                }}
                editable={editable}
                disabled={locked}
                error={validation && quote === null ? t('tenantSettings.invalidValue') : null}
              />
              {editable && (
                <div className="border-line flex flex-col gap-4 border-t py-5 sm:flex-row sm:items-end sm:justify-between">
                  <FormField
                    label={t('tenantSettings.reason')}
                    error={
                      validation && !currentDraft.reasonCode
                        ? t('tenantSettings.reasonRequired')
                        : undefined
                    }
                    className="w-full sm:max-w-xs"
                  >
                    <Select
                      value={currentDraft.reasonCode}
                      disabled={locked}
                      onChange={(event) => {
                        setDraft({
                          ...currentDraft,
                          reasonCode: event.target.value as Draft['reasonCode'],
                        });
                        setDraftVersion(visible.etag);
                      }}
                      placeholder={t('tenantSettings.chooseReason')}
                      options={(['OPERATING_REQUIREMENT', 'CORRECT_CONFIGURATION'] as const).map(
                        (value) => ({ value, label: t(`tenantSettings.reasons.${value}`) }),
                      )}
                    />
                  </FormField>
                  <Button type="submit" disabled={locked || !changed}>
                    {t('tenantSettings.reviewChange')}
                  </Button>
                </div>
              )}
            </form>
          </div>
          {!editable && (
            <p className="text-fg-muted mt-4 text-sm">{t('tenantSettings.readOnly')}</p>
          )}
          {problem && (
            <div className="mt-4">
              <ProblemAlert problem={problem} />
            </div>
          )}
          {uncertain && command && (
            <div className="mt-4 space-y-2" role="status">
              <p className="text-sm">{t('tenantSettings.uncertain')}</p>
              <Button disabled={busy} onClick={() => void send(command)}>
                {t('tenantSettings.retrySame')}
              </Button>
            </div>
          )}
          {cancelled && command && !uncertain && (
            <div className="mt-4 space-y-2" role="status">
              <p className="text-sm">{t('tenantSettings.cancelled')}</p>
              <Button disabled={busy} onClick={() => void send(command)}>
                {t('tenantSettings.retrySame')}
              </Button>
            </div>
          )}
          {conflict && (
            <div className="mt-4 space-y-2" role="alert">
              <p className="text-sm">{t('tenantSettings.conflict')}</p>
              <Button variant="secondary" onClick={() => void reload()}>
                {t('tenantSettings.reload')}
              </Button>
            </div>
          )}
          {saved && (
            <p className="text-success mt-4 text-sm" role="status">
              {t('tenantSettings.saved')}
            </p>
          )}
          {refreshFailed && (
            <div className="mt-2 space-y-2" role="status">
              <p className="text-sm">{t('tenantSettings.refreshFailed')}</p>
              <Button variant="secondary" onClick={() => void reload()}>
                {t('tenantSettings.reload')}
              </Button>
            </div>
          )}
        </>
      )}
      <Dialog
        open={confirmOpen && !!command && !uncertain && !cancelled}
        onOpenChange={(open) => {
          if (!open) {
            setConfirmOpen(false);
            if (command) setCancelled(true);
          }
        }}
        title={t('tenantSettings.confirmTitle')}
        description={
          command
            ? t('tenantSettings.confirmInstitution', { tenant: command.tenantName })
            : undefined
        }
        actions={
          <>
            <Button
              variant="secondary"
              onClick={() => {
                setConfirmOpen(false);
                setCancelled(true);
              }}
            >
              {t('common.cancel')}
            </Button>
            <Button
              disabled={busy || !command}
              onClick={() => {
                setConfirmOpen(false);
                if (command) void send(command);
              }}
            >
              {t('tenantSettings.confirmChange')}
            </Button>
          </>
        }
      >
        {command && (
          <div className="mt-4 space-y-3 text-sm">
            <div className="grid grid-cols-[1fr_auto_auto] gap-x-4 gap-y-2 tabular-nums">
              <span></span>
              <strong>{t('tenantSettings.before')}</strong>
              <strong>{t('tenantSettings.after')}</strong>
              <span>{t('tenantSettings.holdMinutes')}</span>
              <span>{command.before.holdMinutes.effectiveValue}</span>
              <span>{command.body.holdMinutes}</span>
              <span>{t('tenantSettings.quoteTtlMinutes')}</span>
              <span>{command.before.quoteTtlMinutes.effectiveValue}</span>
              <span>{command.body.quoteTtlMinutes}</span>
            </div>
            <p>
              {t('tenantSettings.reason')}: {t(`tenantSettings.reasons.${command.body.reasonCode}`)}
            </p>
            <p className="text-fg-muted">{t('tenantSettings.confirmImpact')}</p>
          </div>
        )}
      </Dialog>
      <StepUpDialog
        open={stepUp.required}
        action={t('tenantSettings.confirmTitle')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </section>
  );
}
