import { ApiError, randomId, type TenantInvitation } from '@kapsora/api-client';
import { useSession, useSessionStore, useStepUp } from '@kapsora/auth';
import { formatDateTime, useTranslation } from '@kapsora/i18n';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  ProblemAlert,
  Select,
  Spinner,
  StepUpDialog,
  statusTone,
} from '@kapsora/ui';
import { useQueryClient } from '@tanstack/react-query';
import { Link, useNavigate } from '@tanstack/react-router';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { directoryContextKey, useOps } from '../api';
import { problemOf } from '../problems';
import { useTenantInvitations } from './queries';

const statuses: TenantInvitation['status'][] = ['PENDING', 'ACCEPTED', 'CANCELLED', 'EXPIRED'];
interface CreateCommand {
  context: string;
  revision: number;
  tenantId: string;
  email: string;
  key: string;
}

export function AdminInvitationsPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const ops = useOps();
  const store = useSessionStore();
  const queryClient = useQueryClient();
  const stepUp = useStepUp();
  const canManage = useSession((s) => s.activeTenant?.canManageTenantUsers === true);
  const [status, setStatus] = useState<TenantInvitation['status'] | ''>('');
  const [cursors, setCursors] = useState<(string | null)[]>([null]);
  const [index, setIndex] = useState(0);
  const [email, setEmail] = useState('');
  const [command, setCommand] = useState<CreateCommand | null>(null);
  const [uncertain, setUncertain] = useState(false);
  const [deliveryDisabled, setDeliveryDisabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ReturnType<typeof problemOf> | null>(null);
  const inFlight = useRef(false);
  const mounted = useRef(true);
  const revision = useRef(0);
  const cursor = cursors[index] ?? null;
  const query = useTenantInvitations({
    limit: 25,
    ...(status ? { status } : {}),
    ...(cursor ? { cursor } : {}),
  });
  const nextCursor = query.data?.nextCursor ?? null;
  const canNext = !!nextCursor && !cursors.slice(0, index + 1).includes(nextCursor);

  useEffect(() => {
    mounted.current = true;
    let previous = directoryContextKey(store.getState());
    const unsubscribe = store.subscribe((state) => {
      const next = directoryContextKey(state);
      if (next !== previous) {
        revision.current += 1;
        previous = next;
      }
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [store]);

  const current = (snapshot: CreateCommand) => {
    const state = store.getState();
    return (
      mounted.current &&
      revision.current === snapshot.revision &&
      state.activeTenant?.canManageTenantUsers === true &&
      state.activeTenant.tenant.id === snapshot.tenantId &&
      directoryContextKey(state) === snapshot.context
    );
  };

  async function send(snapshot: CreateCommand) {
    if (inFlight.current || !current(snapshot)) return;
    inFlight.current = true;
    setBusy(true);
    setProblem(null);
    try {
      const result = await stepUp.run(() => {
        if (!current(snapshot)) throw new Error('Authorization context changed');
        return ops.admin.createInvitation(snapshot.tenantId, snapshot.email, snapshot.key);
      });
      if (!current(snapshot) || !result) return;
      setEmail('');
      setCommand(null);
      setUncertain(false);
      queryClient.setQueryData(
        ['admin-invitations', snapshot.context, 'detail', result.data.invitationId],
        result,
      );
      void queryClient.invalidateQueries({
        queryKey: ['admin-invitations', snapshot.context, 'list'],
      });
      await navigate({
        to: '/admin/invitations/$invitationId',
        params: { invitationId: result.data.invitationId },
      });
    } catch (error) {
      if (!current(snapshot)) return;
      setProblem(problemOf(error));
      const statusCode = error instanceof ApiError ? error.status : 0;
      const code = error instanceof ApiError ? error.problem.code : '';
      if (code === 'INVITATION_DELIVERY_DISABLED') {
        setDeliveryDisabled(true);
        setCommand(null);
        setUncertain(false);
      } else if (
        statusCode === 0 ||
        statusCode === 408 ||
        statusCode === 429 ||
        statusCode >= 500 ||
        code === 'IDEMPOTENCY_IN_PROGRESS'
      ) {
        setUncertain(true);
      } else {
        setCommand(null);
        setUncertain(false);
      }
    } finally {
      inFlight.current = false;
      if (current(snapshot)) setBusy(false);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current || !canManage || deliveryDisabled) return;
    if (command) {
      void send(command);
      return;
    }
    const address = email.trim();
    if (!address) return;
    const state = store.getState();
    if (state.activeTenant?.canManageTenantUsers !== true) return;
    const snapshot: CreateCommand = {
      context: directoryContextKey(state),
      revision: revision.current,
      tenantId: state.activeTenant.tenant.id,
      email: address,
      key: randomId(),
    };
    setCommand(snapshot);
    void send(snapshot);
  }

  function changeStatus(value: string) {
    setStatus(value as TenantInvitation['status'] | '');
    setCursors([null]);
    setIndex(0);
  }

  return (
    <section data-testid="admin-invitations-page">
      <Link to="/admin" className="text-primary mb-4 inline-block text-sm hover:underline">
        ← {t('adminInvitations.back')}
      </Link>
      <PageHeader title={t('adminInvitations.title')} description={t('adminInvitations.intro')} />
      {canManage && (
        <Card className="mb-6 max-w-2xl">
          <h2 className="text-lg font-semibold">{t('adminInvitations.createTitle')}</h2>
          <p className="text-fg-muted mt-1 text-sm">{t('adminInvitations.createIntro')}</p>
          <form onSubmit={submit} className="mt-4 grid gap-3">
            <FormField label={t('adminInvitations.email')} required>
              <Input
                type="email"
                value={command?.email ?? email}
                onChange={(event) => setEmail(event.target.value)}
                autoComplete="off"
                disabled={!!command || busy}
                maxLength={254}
                required
              />
            </FormField>
            <ProblemAlert problem={problem} hideFieldErrors />
            {uncertain && (
              <p role="status" className="text-fg-muted text-sm">
                {t('adminInvitations.uncertain')}
              </p>
            )}
            <div>
              <Button
                type="submit"
                loading={busy}
                disabled={deliveryDisabled || (!command && !email.trim())}
              >
                {command ? t('adminInvitations.retry') : t('adminInvitations.create')}
              </Button>
            </div>
          </form>
        </Card>
      )}
      <label className="mb-4 grid max-w-56 gap-1 text-sm">
        <span className="font-medium">{t('adminInvitations.statusFilter')}</span>
        <Select
          value={status}
          onChange={(event) => changeStatus(event.target.value)}
          placeholder={t('adminInvitations.allStatuses')}
          options={statuses.map((value) => ({
            value,
            label: t(`adminInvitations.status.${value}`),
          }))}
        />
      </label>
      {query.isPending ? (
        <p className="text-fg-muted flex items-center gap-2 text-sm" aria-busy="true">
          <Spinner /> {t('common.loading')}
        </p>
      ) : query.isError ? (
        <ProblemAlert
          page
          problem={problemOf(query.error)}
          actions={
            <Button variant="secondary" onClick={() => void query.refetch()}>
              {t('common.retry')}
            </Button>
          }
        />
      ) : query.data.items.length === 0 ? (
        <EmptyState title={t('adminInvitations.empty')} />
      ) : (
        <ul className="grid gap-2 sm:grid-cols-2">
          {query.data.items.map((row) => (
            <li
              key={row.invitationId}
              data-testid="admin-invitation-row"
              className="border-line bg-surface grid min-w-0 gap-2 rounded-lg border p-4 text-sm"
            >
              <div className="flex items-start justify-between gap-2">
                <Link
                  to="/admin/invitations/$invitationId"
                  params={{ invitationId: row.invitationId }}
                  className="text-primary min-w-0 break-all font-semibold hover:underline"
                >
                  {row.maskedRecipient || t('adminInvitations.recipientPurged')}
                </Link>
                <Badge tone={statusTone(row.status)}>
                  {t(`adminInvitations.status.${row.status}`)}
                </Badge>
              </div>
              <span className="text-fg-muted">
                {t('adminInvitations.expiresAt')}: {formatDateTime(row.expiresAt)}
              </span>
              <span className="text-fg-muted">
                {t('adminInvitations.delivery')}:{' '}
                {t(`adminInvitations.deliveryStatus.${row.deliveryStatus}`)}
              </span>
            </li>
          ))}
        </ul>
      )}
      {index > 0 || canNext ? (
        <nav className="mt-4 flex gap-2" aria-label={t('adminInvitations.pages')}>
          <Button
            variant="secondary"
            disabled={index === 0 || query.isPending}
            onClick={() => setIndex(index - 1)}
          >
            {t('adminUsers.previous')}
          </Button>
          <Button
            variant="secondary"
            disabled={!canNext || query.isPending}
            onClick={() => {
              if (nextCursor) {
                setCursors((old) => [...old.slice(0, index + 1), nextCursor]);
                setIndex(index + 1);
              }
            }}
          >
            {t('adminUsers.next')}
          </Button>
        </nav>
      ) : null}
      <StepUpDialog
        open={stepUp.required}
        action={t('adminInvitations.create')}
        busy={stepUp.busy}
        problem={stepUp.error}
        onConfirm={(password) => void stepUp.confirm(password)}
        onCancel={stepUp.cancel}
      />
    </section>
  );
}
