import {
  ApiError,
  randomId,
  type AcceptExistingInvitationResponse,
  type InspectInvitationResponse,
} from '@kapsora/api-client';
import { useSession, useSessionStore } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import { Button, Card, Input, ProblemAlert } from '@kapsora/ui';
import { useNavigate } from '@tanstack/react-router';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useOps } from '../api';
import { problemOf } from '../problems';
import { AnonymousInvitationPage } from './AnonymousInvitationPage';

interface AcceptCommand {
  context: string;
  revision: number;
  code: string;
  key: string;
}

function recipientContext(state: ReturnType<ReturnType<typeof useSessionStore>['getState']>) {
  return [
    state.status,
    state.session?.actorId ?? '',
    state.session?.expiresAt ?? '',
    state.session?.activeTenantId ?? '',
    state.csrfToken ?? '',
    state.me?.displayName ?? '',
  ].join(':');
}

/** Fixed proof-entry page, available before any tenant or application grant is selected. */
export function InvitationPage() {
  const status = useSession((state) => state.status);
  if (status !== 'authenticated') return <AnonymousInvitationPage />;
  return <ExistingInvitationPage />;
}

function ExistingInvitationPage() {
  const { t } = useTranslation();
  const store = useSessionStore();
  const ops = useOps();
  const navigate = useNavigate();
  const accountName = useSession((s) => s.me?.displayName ?? '');
  const [code, setCode] = useState('');
  const [inspected, setInspected] = useState<InspectInvitationResponse | null>(null);
  const [accepted, setAccepted] = useState<AcceptExistingInvitationResponse | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [command, setCommand] = useState<AcceptCommand | null>(null);
  const [uncertain, setUncertain] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const mounted = useRef(true);
  const inFlight = useRef(false);
  const requestSerial = useRef(0);
  const revision = useRef(0);

  useEffect(() => {
    mounted.current = true;
    let previous = recipientContext(store.getState());
    const unsubscribe = store.subscribe((state) => {
      const next = recipientContext(state);
      if (next !== previous) {
        revision.current += 1;
        requestSerial.current += 1;
        inFlight.current = false;
        setBusy(false);
        previous = next;
        setCode('');
        setInspected(null);
        setAccepted(null);
        setConfirmed(false);
        setCommand(null);
        setProblem(null);
        setUncertain(false);
      }
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [store]);

  const current = (context: string, atRevision: number) =>
    mounted.current &&
    revision.current === atRevision &&
    recipientContext(store.getState()) === context &&
    store.getState().status === 'authenticated';

  async function inspect(event: FormEvent) {
    event.preventDefault();
    const proof = code.trim();
    if (!proof || busy || inFlight.current) return;
    const context = recipientContext(store.getState());
    const atRevision = revision.current;
    inFlight.current = true;
    const serial = ++requestSerial.current;
    setBusy(true);
    setProblem(null);
    setInspected(null);
    setAccepted(null);
    setConfirmed(false);
    setCommand(null);
    setUncertain(false);
    try {
      if ((await store.checkAccount()) !== 'same' || !current(context, atRevision)) return;
      const result = await ops.admin.inspectInvitation(proof);
      if (current(context, atRevision)) setInspected(result);
    } catch (error) {
      if (current(context, atRevision)) setProblem(problemOf(error));
    } finally {
      if (serial === requestSerial.current) {
        inFlight.current = false;
        if (current(context, atRevision)) setBusy(false);
      }
    }
  }

  async function send(snapshot: AcceptCommand) {
    if (inFlight.current || !current(snapshot.context, snapshot.revision)) return;
    inFlight.current = true;
    const serial = ++requestSerial.current;
    setBusy(true);
    setProblem(null);
    try {
      if ((await store.checkAccount()) !== 'same' || !current(snapshot.context, snapshot.revision))
        return;
      const result = await ops.admin.acceptExistingInvitation(snapshot.code, snapshot.key);
      if (!current(snapshot.context, snapshot.revision)) return;
      setAccepted(result);
      setInspected(null);
      setCode('');
      setCommand(null);
      setUncertain(false);
    } catch (error) {
      if (!current(snapshot.context, snapshot.revision)) return;
      setProblem(problemOf(error));
      const status = error instanceof ApiError ? error.status : 0;
      const code = error instanceof ApiError ? error.problem.code : '';
      if (
        status === 0 ||
        status === 408 ||
        status === 429 ||
        status >= 500 ||
        code === 'IDEMPOTENCY_IN_PROGRESS'
      ) {
        setUncertain(true);
      } else {
        setCommand(null);
        setInspected(null);
        setConfirmed(false);
        setUncertain(false);
      }
    } finally {
      if (serial === requestSerial.current) {
        inFlight.current = false;
        if (current(snapshot.context, snapshot.revision)) setBusy(false);
      }
    }
  }

  function accept() {
    if (inFlight.current || !confirmed || !inspected || inspected.invitationStatus !== 'PENDING')
      return;
    if (command) {
      void send(command);
      return;
    }
    const snapshot: AcceptCommand = {
      context: recipientContext(store.getState()),
      revision: revision.current,
      code: code.trim(),
      key: randomId(),
    };
    if (!snapshot.code || store.getState().status !== 'authenticated') return;
    setCommand(snapshot);
    void send(snapshot);
  }

  return (
    <main
      id="main"
      className="bg-surface-subtle flex min-h-dvh items-center justify-center p-4 sm:p-8"
      data-testid="invitation-page"
    >
      <Card className="w-full max-w-xl">
        <p className="text-fg-muted text-xs font-semibold tracking-wide">KAPSORA</p>
        <h1 className="mt-2 text-2xl font-semibold">{t('invitation.title')}</h1>
        <p className="text-fg-muted mt-2 text-sm">{t('invitation.existingAccountIntro')}</p>
        <div className="border-line mt-5 flex flex-wrap items-center justify-between gap-2 border-y py-3 text-sm">
          <span>{t('invitation.signedInAs', { name: accountName })}</span>
          <Button
            variant="secondary"
            size="sm"
            onClick={() =>
              void store
                .logout()
                .finally(() => navigate({ href: '/auth/login?returnTo=%2Finvitation' }))
            }
          >
            {t('invitation.differentAccount')}
          </Button>
        </div>
        {accepted ? (
          <div className="mt-6 grid gap-4" role="status">
            <h2 className="text-lg font-semibold">{t('invitation.acceptedTitle')}</h2>
            <p>
              {accepted.accessPending
                ? t('invitation.accessPending')
                : t('invitation.alreadyAccessible', { tenant: accepted.tenantDisplayName })}
            </p>
            <p className="text-fg-muted text-sm">{t('invitation.noAutomaticSwitch')}</p>
            <a className="text-primary text-sm font-medium hover:underline" href="/auth/apps">
              {t('invitation.returnToApps')}
            </a>
          </div>
        ) : (
          <>
            <form onSubmit={(event) => void inspect(event)} className="mt-6 grid gap-3">
              <label className="grid gap-1 text-sm font-medium">
                {t('invitation.code')}
                <Input
                  value={command?.code ?? code}
                  onChange={(event) => {
                    setCode(event.target.value);
                    setInspected(null);
                    setProblem(null);
                    setConfirmed(false);
                  }}
                  autoComplete="off"
                  spellCheck={false}
                  maxLength={300}
                  disabled={!!command || busy}
                  required
                />
              </label>
              <p className="text-fg-muted text-xs">{t('invitation.codeHint')}</p>
              <div>
                <Button
                  type="submit"
                  variant="secondary"
                  loading={busy}
                  disabled={!code.trim() || !!command}
                >
                  {t('invitation.inspect')}
                </Button>
              </div>
            </form>
            <ProblemAlert problem={problem} className="mt-4" hideFieldErrors />
            {inspected && inspected.invitationStatus === 'PENDING' && (
              <div
                className="border-line bg-surface mt-6 grid gap-4 rounded-lg border p-4"
                data-testid="invitation-consent"
              >
                <div>
                  <h2 className="font-semibold">{inspected.tenantDisplayName}</h2>
                  <p className="text-fg-muted mt-1 text-sm">
                    {t('invitation.expiresAt')}: {formatDateTime(inspected.expiresAt)}
                  </p>
                </div>
                <p className="text-sm">
                  {t('invitation.consentIntro', {
                    name: accountName,
                    tenant: inspected.tenantDisplayName,
                  })}
                </p>
                <label className="flex items-start gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={confirmed}
                    onChange={(event) => setConfirmed(event.target.checked)}
                    disabled={busy}
                  />
                  <span>{t('invitation.consent', { tenant: inspected.tenantDisplayName })}</span>
                </label>
                {uncertain && (
                  <p role="status" className="text-fg-muted text-sm">
                    {t('invitation.uncertain')}
                  </p>
                )}
                <div>
                  <Button onClick={accept} loading={busy} disabled={!confirmed}>
                    {command ? t('invitation.retry') : t('invitation.accept')}
                  </Button>
                </div>
              </div>
            )}
            {inspected?.invitationStatus === 'ACCEPTED' && (
              <div
                className="border-line bg-surface mt-6 grid gap-3 rounded-lg border p-4"
                role="status"
              >
                <h2 className="font-semibold">{inspected.tenantDisplayName}</h2>
                <p className="text-sm">{t('invitation.alreadyAccepted')}</p>
                <a className="text-primary text-sm font-medium hover:underline" href="/auth/apps">
                  {t('invitation.returnToApps')}
                </a>
              </div>
            )}
          </>
        )}
      </Card>
    </main>
  );
}
