import {
  ApiError,
  randomId,
  type AcceptNewInvitationResponse,
  type InspectInvitationResponse,
} from '@kapsora/api-client';
import { useSessionStore } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import { Button, Card, Input, PasswordInput, ProblemAlert } from '@kapsora/ui';
import { Link } from '@tanstack/react-router';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useOps } from '../api';
import { problemOf } from '../problems';

type Mode = 'create' | 'receipt';
type Command = {
  code: string;
  displayName: string;
  key: string;
  revision: number;
  context: string;
};

function contextOf(store: ReturnType<typeof useSessionStore>): string {
  const state = store.getState();
  return [
    state.status,
    state.session?.actorId ?? '',
    state.session?.expiresAt ?? '',
    state.csrfToken ?? '',
  ].join(':');
}

/** Anonymous recipient flow. The proof, password and pinned retry exist only in this component. */
export function AnonymousInvitationPage() {
  const { t } = useTranslation();
  const store = useSessionStore();
  const ops = useOps();
  const [mode, setMode] = useState<Mode>('create');
  const [code, setCode] = useState('');
  const [preview, setPreview] = useState<InspectInvitationResponse | null>(null);
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [confirmed, setConfirmed] = useState(false);
  const [command, setCommand] = useState<Command | null>(null);
  const [uncertain, setUncertain] = useState(false);
  const [receiptSuggested, setReceiptSuggested] = useState(false);
  const [accepted, setAccepted] = useState<AcceptNewInvitationResponse | null>(null);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const [genericError, setGenericError] = useState(false);
  const [busy, setBusy] = useState(false);
  const mounted = useRef(true);
  const revision = useRef(0);
  const serial = useRef(0);
  const inFlight = useRef(false);

  function clear() {
    revision.current++;
    serial.current++;
    inFlight.current = false;
    setBusy(false);
    setCode('');
    setPreview(null);
    setDisplayName('');
    setPassword('');
    setConfirmed(false);
    setCommand(null);
    setUncertain(false);
    setReceiptSuggested(false);
    setAccepted(null);
    setProblem(null);
    setGenericError(false);
  }

  useEffect(() => {
    mounted.current = true;
    const activeRevision = revision;
    const activeSerial = serial;
    let previous = contextOf(store);
    const unsubscribe = store.subscribe(() => {
      const next = contextOf(store);
      if (next !== previous) {
        previous = next;
        clear();
      }
    });
    return () => {
      mounted.current = false;
      activeRevision.current++;
      activeSerial.current++;
      unsubscribe();
    };
  }, [store]);

  function current(atRevision: number, context: string) {
    return (
      mounted.current &&
      revision.current === atRevision &&
      contextOf(store) === context &&
      store.getState().status === 'anonymous'
    );
  }

  function switchMode(next: Mode) {
    clear();
    setMode(next);
  }

  function editCode(next: string) {
    revision.current++;
    serial.current++;
    inFlight.current = false;
    setBusy(false);
    setCode(next);
    setPreview(null);
    setConfirmed(false);
    setProblem(null);
    setGenericError(false);
  }

  async function inspect(event: FormEvent) {
    event.preventDefault();
    const proof = code.trim();
    if (!proof || inFlight.current) return;
    const atRevision = revision.current;
    const context = contextOf(store);
    const request = ++serial.current;
    inFlight.current = true;
    setBusy(true);
    setPreview(null);
    setProblem(null);
    setGenericError(false);
    try {
      const result = await ops.admin.inspectNewInvitation(proof);
      if (current(atRevision, context) && result.invitationStatus === 'PENDING') setPreview(result);
    } catch {
      if (current(atRevision, context)) setGenericError(true);
    } finally {
      if (request === serial.current) {
        inFlight.current = false;
        if (current(atRevision, context)) setBusy(false);
      }
    }
  }

  async function sendNew(snapshot: Command, chosenPassword: string) {
    if (inFlight.current || !current(snapshot.revision, snapshot.context)) return;
    const request = ++serial.current;
    inFlight.current = true;
    setBusy(true);
    setPassword('');
    setProblem(null);
    setGenericError(false);
    try {
      const result = await ops.admin.acceptNewInvitation(
        snapshot.code,
        snapshot.displayName,
        chosenPassword,
        snapshot.key,
      );
      if (!current(snapshot.revision, snapshot.context)) return;
      setAccepted(result);
      setCode('');
      setPreview(null);
      setCommand(null);
      setUncertain(false);
    } catch (error) {
      if (!current(snapshot.revision, snapshot.context)) return;
      const status = error instanceof ApiError ? error.status : 0;
      const errorCode = error instanceof ApiError ? error.problem.code : '';
      if (status === 409 && errorCode === 'IDEMPOTENCY_KEY_REUSED') {
        setReceiptSuggested(true);
        setCommand(null);
        setPreview(null);
        setUncertain(false);
      } else if (
        status === 0 ||
        status === 408 ||
        status === 429 ||
        status >= 500 ||
        errorCode === 'IDEMPOTENCY_IN_PROGRESS'
      ) {
        setUncertain(true);
      } else {
        setCommand(null);
        setPreview(null);
      }
      if (status === 404 || status === 423 || status === 429) setGenericError(true);
      else if (!(status === 409 && errorCode === 'IDEMPOTENCY_KEY_REUSED'))
        setProblem(problemOf(error));
    } finally {
      if (request === serial.current) {
        inFlight.current = false;
        if (current(snapshot.revision, snapshot.context)) setBusy(false);
      }
    }
  }

  function accept(event: FormEvent) {
    event.preventDefault();
    if (
      inFlight.current ||
      !preview ||
      preview.invitationStatus !== 'PENDING' ||
      !confirmed ||
      Array.from(password).length < 12 ||
      new TextEncoder().encode(password).length > 1024
    )
      return;
    const snapshot: Command = command ?? {
      code: code.trim(),
      displayName: displayName.trim(),
      key: randomId(),
      revision: revision.current,
      context: contextOf(store),
    };
    if (
      !snapshot.code ||
      !snapshot.displayName ||
      Array.from(snapshot.displayName).length > 200 ||
      /[\p{Cc}\p{Cf}]/u.test(snapshot.displayName)
    )
      return;
    if (!command) setCommand(snapshot);
    void sendNew(snapshot, password);
  }

  async function receipt(event: FormEvent) {
    event.preventDefault();
    const proof = code.trim();
    if (!proof || !password || inFlight.current) return;
    const atRevision = revision.current;
    const context = contextOf(store);
    const request = ++serial.current;
    const chosenPassword = password;
    inFlight.current = true;
    setBusy(true);
    setPassword('');
    setProblem(null);
    setGenericError(false);
    try {
      const result = await ops.admin.recoverNewInvitation(proof, chosenPassword);
      if (!current(atRevision, context)) return;
      setAccepted(result);
      setCode('');
    } catch {
      if (current(atRevision, context)) setGenericError(true);
    } finally {
      if (request === serial.current) {
        inFlight.current = false;
        if (current(atRevision, context)) setBusy(false);
      }
    }
  }

  return (
    <main
      id="main"
      className="bg-surface-subtle flex min-h-dvh items-center justify-center p-4 sm:p-8"
      data-testid="invitation-anonymous-page"
    >
      <Card className="w-full max-w-xl">
        <p className="text-fg-muted text-xs font-semibold tracking-wide">KAPSORA</p>
        <h1 className="mt-2 text-2xl font-semibold">{t('invitation.title')}</h1>
        {accepted ? (
          <div className="mt-6 grid gap-4" data-testid="invitation-new-result" role="status">
            <h2 className="text-lg font-semibold">{t('invitation.acceptedTitle')}</h2>
            <p>
              {accepted.accessPending
                ? t('invitation.accessPending')
                : t('invitation.alreadyAccessible', { tenant: accepted.tenantDisplayName })}
            </p>
            <p className="text-fg-muted text-sm">{accepted.tenantDisplayName}</p>
            <label className="grid gap-1 text-sm font-medium">
              {t('invitation.loginHandle')}
              <Input
                value={accepted.loginHandle}
                readOnly
                onFocus={(event) => event.target.select()}
              />
            </label>
            <p className="text-sm">{t('invitation.keepHandle')}</p>
            <p className="text-fg-muted text-sm">
              {t('invitation.recoveryDeadline', {
                date: formatDateTime(accepted.recoveryExpiresAt),
              })}
            </p>
            <p className="text-fg-muted text-sm">{t('invitation.noAutomaticLogin')}</p>
            <Link className="text-primary text-sm font-medium hover:underline" to="/auth/login">
              {t('invitation.signIn')}
            </Link>
          </div>
        ) : (
          <>
            <p className="text-fg-muted mt-2 text-sm">{t('invitation.newIntro')}</p>
            <div className="border-line mt-5 flex flex-wrap gap-3 border-y py-3 text-sm">
              <Link
                className="text-primary font-medium hover:underline"
                to="/auth/login"
                search={{ returnTo: '/invitation' }}
              >
                {t('invitation.useExisting')}
              </Link>
              <button
                className="text-primary font-medium hover:underline"
                type="button"
                onClick={() => switchMode(mode === 'receipt' ? 'create' : 'receipt')}
              >
                {mode === 'receipt' ? t('invitation.createInstead') : t('invitation.recoverHandle')}
              </button>
            </div>
            {receiptSuggested && (
              <div
                className="border-line bg-surface mt-4 grid gap-2 rounded-lg border p-4 text-sm"
                role="status"
              >
                <p>{t('invitation.receiptSuggested')}</p>
                <button
                  className="text-primary justify-self-start font-medium hover:underline"
                  type="button"
                  onClick={() => switchMode('receipt')}
                >
                  {t('invitation.recoverHandle')}
                </button>
              </div>
            )}
            {mode === 'create' ? (
              <>
                <form className="mt-6 grid gap-3" onSubmit={(event) => void inspect(event)}>
                  <label className="grid gap-1 text-sm font-medium">
                    {t('invitation.code')}
                    <Input
                      value={command?.code ?? code}
                      onChange={(event) => editCode(event.target.value)}
                      autoComplete="off"
                      spellCheck={false}
                      maxLength={300}
                      disabled={!!command}
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
                {preview?.invitationStatus === 'PENDING' && (
                  <form
                    className="border-line bg-surface mt-6 grid gap-4 rounded-lg border p-4"
                    data-testid="invitation-new-consent"
                    onSubmit={accept}
                  >
                    <div>
                      <h2 className="font-semibold">{preview.tenantDisplayName}</h2>
                      <p className="text-fg-muted mt-1 text-sm">
                        {t('invitation.expiresAt')}: {formatDateTime(preview.expiresAt)}
                      </p>
                    </div>
                    <p className="text-sm">
                      {t('invitation.newConsentIntro', { tenant: preview.tenantDisplayName })}
                    </p>
                    <label className="grid gap-1 text-sm font-medium">
                      {t('invitation.displayName')}
                      <Input
                        value={command?.displayName ?? displayName}
                        onChange={(event) => setDisplayName(event.target.value)}
                        maxLength={200}
                        disabled={!!command || busy}
                        required
                      />
                    </label>
                    <label className="grid gap-1 text-sm font-medium">
                      {t('invitation.newPassword')}
                      <PasswordInput
                        value={password}
                        onChange={(event) => setPassword(event.target.value)}
                        autoComplete="new-password"
                        disabled={busy}
                        required
                      />
                    </label>
                    <p className="text-fg-muted text-xs">{t('invitation.passwordHint')}</p>
                    <label className="flex items-start gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={confirmed}
                        onChange={(event) => setConfirmed(event.target.checked)}
                        disabled={busy}
                      />
                      <span>
                        {t('invitation.newConsent', { tenant: preview.tenantDisplayName })}
                      </span>
                    </label>
                    {uncertain && (
                      <p className="text-fg-muted text-sm" role="status">
                        {t('invitation.newUncertain')}
                      </p>
                    )}
                    <div>
                      <Button
                        type="submit"
                        loading={busy}
                        disabled={
                          !confirmed || Array.from(password).length < 12 || !displayName.trim()
                        }
                      >
                        {command ? t('invitation.retry') : t('invitation.createAccount')}
                      </Button>
                    </div>
                  </form>
                )}
              </>
            ) : (
              <form
                className="mt-6 grid gap-4"
                data-testid="invitation-receipt-form"
                onSubmit={(event) => void receipt(event)}
              >
                <p className="text-sm">{t('invitation.receiptIntro')}</p>
                <label className="grid gap-1 text-sm font-medium">
                  {t('invitation.code')}
                  <Input
                    value={code}
                    onChange={(event) => editCode(event.target.value)}
                    autoComplete="off"
                    spellCheck={false}
                    maxLength={300}
                    disabled={busy}
                    required
                  />
                </label>
                <label className="grid gap-1 text-sm font-medium">
                  {t('invitation.currentPassword')}
                  <PasswordInput
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    autoComplete="current-password"
                    disabled={busy}
                    required
                  />
                </label>
                <div>
                  <Button type="submit" loading={busy} disabled={!code.trim() || !password}>
                    {t('invitation.recoverHandle')}
                  </Button>
                </div>
              </form>
            )}
            {genericError && (
              <p className="mt-4 text-sm" role="alert">
                {t('invitation.unavailable')}
              </p>
            )}
            <ProblemAlert problem={problem} className="mt-4" hideFieldErrors />
          </>
        )}
      </Card>
    </main>
  );
}
