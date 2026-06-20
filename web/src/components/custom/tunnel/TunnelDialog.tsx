import { useState } from 'react';
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
  DialogDescription, DialogFooter, DialogTrigger,
} from '@/components/ui/dialog';
import { AlertTriangle, ChevronDown, GitBranchPlus } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
  InputGroupText,
} from '@/components/ui/input-group';
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip';
import toast from 'react-hot-toast';
import { useCreateTunnel, useUpdateTunnel } from '@/hooks/use-tunnel-mutations';
import {
  getTunnelMutationErrorMessage,
  getTunnelMutationFieldError,
} from '@/lib/tunnel-model';
import { parseMbpsInputToBps } from '@/lib/format';
import { useServerStatus } from '@/hooks/use-server-status';
import { getClientDisplayName } from '@/lib/client-utils';
import { cn } from '@/lib/utils';
import type { Client, PortRange, TunnelFormType, TunnelTopology } from '@/types';
import { i18n } from '@/i18n';
import { useTranslation } from 'react-i18next';
import { getInitialTunnelFormState, type TunnelDialogEditData } from './tunnel-dialog-form';
import {
  preserveLoopbackSourceCIDRsOnFirstRestriction,
  shouldWarnMissingLoopbackSourceCIDRs,
} from '@/lib/source-cidrs';

interface TunnelDialogCreateProps {
  mode: 'create';
  clientId: string;
  clients?: Client[];
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /** 触发按钮（作为 DialogTrigger children） */
  trigger?: React.ReactNode;
  hideTrigger?: boolean;
}

interface TunnelDialogEditProps {
  mode: 'edit';
  tunnel: TunnelDialogEditData | null;
  clients?: Client[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

type TunnelDialogProps = TunnelDialogCreateProps | TunnelDialogEditProps;

const typeOptions: { value: TunnelFormType; label: string }[] = [
  { value: 'tcp', label: 'TCP' },
  { value: 'udp', label: 'UDP' },
  { value: 'http', label: 'HTTP' },
  { value: 'socks5', label: 'SOCKS5' },
];

interface LocalFieldError {
  field: string;
  message: string;
  code?: string;
  source?: 'local' | 'server';
}

function fieldErrorMatches(error: LocalFieldError | null, fields: readonly string[]) {
  return Boolean(error && fields.includes(error.field));
}

function FieldErrorText({
  error,
  fields,
}: {
  error: LocalFieldError | null;
  fields: readonly string[];
}) {
  if (!fieldErrorMatches(error, fields)) {
    return null;
  }
  return (
    <p className="text-[11px] font-medium text-destructive">
      {error?.message}
    </p>
  );
}

function getFormKey(props: TunnelDialogProps, open: boolean) {
  if (props.mode === 'edit') {
    const tunnelKey = props.tunnel
      ? `${props.tunnel.clientId}:${props.tunnel.id}`
      : 'empty';
    return `edit:${tunnelKey}:${open ? 'open' : 'closed'}`;
  }

  return `create:${props.clientId}:${open ? 'open' : 'closed'}`;
}

function isPortAllowedByRanges(port: number, ranges: PortRange[] | undefined) {
  if (!ranges || ranges.length === 0) {
    return true;
  }
  return ranges.some((range) => port >= range.start && port <= range.end);
}

function parsePortInput(value: string) {
  if (!/^\d+$/.test(value.trim())) {
    return null;
  }
  const port = Number.parseInt(value, 10);
  return port >= 1 && port <= 65535 ? port : null;
}

function parseCommaSeparatedList(value: string) {
  return value.split(',').map((item) => item.trim()).filter(Boolean);
}

function parseSourceCIDRInput(value: string) {
  const parsed = parseCommaSeparatedList(value);
  return parsed.length > 0 ? parsed : ['0.0.0.0/0', '::/0'];
}

function parseCommaSeparatedPortList(value: string) {
  const ports: number[] = [];
  for (const item of parseCommaSeparatedList(value)) {
    const port = parsePortInput(item);
    if (port === null) {
      return null;
    }
    ports.push(port);
  }
  return ports;
}

function localFieldError(field: string, message: string): LocalFieldError {
  return { field, message, code: 'invalid_field', source: 'local' };
}

function serverFieldError(error: unknown): LocalFieldError | null {
  return getTunnelMutationFieldError(error);
}

function formatPortRanges(ranges: PortRange[] | undefined) {
  if (!ranges || ranges.length === 0) {
    return i18n.t('tunnels.unrestricted');
  }
  return ranges.map((range) => range.start === range.end ? range.start : `${range.start}-${range.end}`).join(', ');
}

export function ClientToClientTopologyButton({
  selected,
  disabled,
  label,
  tooltip,
  onSelect,
}: {
  selected: boolean;
  disabled: boolean;
  label: string;
  tooltip: string;
  onSelect: () => void;
}) {
  const button = (
    <Button
      type="button"
      variant={selected ? 'default' : 'outline'}
      disabled={disabled}
      onClick={onSelect}
      className="w-full"
    >
      {label}
    </Button>
  );

  if (!disabled) {
    return button;
  }

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="block cursor-not-allowed" tabIndex={0}>
          {button}
        </span>
      </TooltipTrigger>
      <TooltipContent side="top">
        {tooltip}
      </TooltipContent>
    </Tooltip>
  );
}

export function TunnelDialog(props: TunnelDialogProps) {
  const { t } = useTranslation();
  const isEdit = props.mode === 'edit';

  // --- 弹窗开关 ---
  const [internalOpen, setInternalOpen] = useState(false);
  const open = isEdit ? props.open : (props.open ?? internalOpen);
  const setOpen = isEdit
    ? props.onOpenChange
    : (props.onOpenChange ?? setInternalOpen);

  const formKey = getFormKey(props, open);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      {!isEdit && !props.hideTrigger && (
        <DialogTrigger asChild>
          {(props as TunnelDialogCreateProps).trigger ?? (
            <Button>
              <GitBranchPlus className="h-4 w-4 mr-1.5" />
              {t('tunnels.addTunnel')}
            </Button>
          )}
        </DialogTrigger>
      )}
      <TunnelDialogForm
        key={formKey}
        props={props}
        open={open}
        setOpen={setOpen}
      />
    </Dialog>
  );
}

function TunnelDialogForm({
  props,
  open,
  setOpen,
}: {
  props: TunnelDialogProps;
  open: boolean;
  setOpen: (open: boolean) => void;
}) {
  const { t } = useTranslation();
  const isEdit = props.mode === 'edit';
  const initialForm = getInitialTunnelFormState(props);
  const [name, setName] = useState(initialForm.name);
  const [topology, setTopology] = useState<TunnelTopology>(initialForm.topology);
  const [targetClientId, setTargetClientId] = useState(initialForm.targetClientId);
  const [ingressClientId, setIngressClientId] = useState(initialForm.ingressClientId);
  const [bindIp, setBindIp] = useState(initialForm.bindIp);
  const [type, setType] = useState<TunnelFormType>(initialForm.type);
  const [localIp, setLocalIp] = useState(initialForm.localIp);
  const [localPort, setLocalPort] = useState(initialForm.localPort);
  const [remotePort, setRemotePort] = useState(initialForm.remotePort);
  const [domain, setDomain] = useState(initialForm.domain);
  const [ingressBps, setIngressBps] = useState(initialForm.ingressBps);
  const [egressBps, setEgressBps] = useState(initialForm.egressBps);
  const [fieldError, setFieldError] = useState<LocalFieldError | null>(null);
  const [sourceCidrs, setSourceCidrs] = useState(initialForm.sourceCidrs);
  const [socks5AuthEnabled, setSocks5AuthEnabled] = useState(initialForm.socks5AuthEnabled);
  const [socks5Username, setSocks5Username] = useState(initialForm.socks5Username);
  const [socks5Password, setSocks5Password] = useState(initialForm.socks5Password);
  const [httpAuthEnabled, setHttpAuthEnabled] = useState(initialForm.httpAuthEnabled);
  const [httpUsername, setHttpUsername] = useState(initialForm.httpUsername);
  const [httpPassword, setHttpPassword] = useState(initialForm.httpPassword);
  const [socks5TargetCidrs, setSocks5TargetCidrs] = useState(initialForm.socks5TargetCidrs);
  const [socks5TargetHosts, setSocks5TargetHosts] = useState(initialForm.socks5TargetHosts);
  const [socks5TargetPorts, setSocks5TargetPorts] = useState(initialForm.socks5TargetPorts);
  const [socks5DialTimeout, setSocks5DialTimeout] = useState(initialForm.socks5DialTimeout);
  const [confirmNoAuthRisk, setConfirmNoAuthRisk] = useState(initialForm.confirmNoAuthRisk);
  const [advancedOpen, setAdvancedOpen] = useState(false);

  const clients = props.clients ?? [];
  const selectedTargetClientId = targetClientId || (props.mode === 'create' ? props.clientId : props.tunnel?.target?.client_id ?? props.tunnel?.owner_client_id ?? props.tunnel?.clientId ?? '');
  const sourceClient = clients.find((client) => client.id === selectedTargetClientId);
  const ingressClientOptions = clients.filter((client) => client.id !== selectedTargetClientId);
  const selectedIngressClientId = ingressClientId && ingressClientId !== selectedTargetClientId
    ? ingressClientId
    : ingressClientOptions[0]?.id || '';
  const isClientToClient = topology === 'client_to_client';
  const isHttp = type === 'http';
  const isSocks5 = type === 'socks5';
  const showLoopbackCIDRWarning = shouldWarnMissingLoopbackSourceCIDRs(sourceCidrs);
  const isEditing = props.mode === 'edit';
  const canUseClientToClient = ingressClientOptions.length > 0;
  const parsedLocalPort = parsePortInput(localPort);
  const parsedRemotePort = isHttp ? 0 : parsePortInput(remotePort);
  const parsedSocks5DialTimeout = Number.parseInt(socks5DialTimeout, 10);
  const selectClassName = cn(
    'h-8 w-full rounded-lg border border-input bg-background px-2.5 py-1 text-sm outline-none transition-colors',
    'focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50',
    'disabled:pointer-events-none disabled:cursor-not-allowed disabled:bg-input/50 disabled:opacity-50',
  );

  const createTunnel = useCreateTunnel();
  const updateTunnel = useUpdateTunnel();
  const mutation = isEdit ? updateTunnel : createTunnel;
  const portErrorMessage = t('tunnels.portInvalid');

  const clearMutationFeedback = () => {
    if (fieldError) {
      setFieldError(null);
    }
    if (mutation.isError) {
      mutation.reset();
    }
  };

  const { data: status } = useServerStatus({
    enabled: open,
    refetchOnMount: 'always',
    staleTime: 0,
  });

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    setFieldError(null);
    const parsedIngressBps = parseMbpsInputToBps(ingressBps);
    const parsedEgressBps = parseMbpsInputToBps(egressBps);

    if (!isSocks5 && !parsedLocalPort) {
      setFieldError(localFieldError('local_port', portErrorMessage));
      return;
    }

    if (!isHttp && !parsedRemotePort) {
      setFieldError(localFieldError('remote_port', portErrorMessage));
      return;
    }

    if (isClientToClient && !canUseClientToClient) {
      setFieldError(localFieldError('ingress.client_id', t('tunnels.c2cRequiresTwoClients')));
      return;
    }

    if (parsedIngressBps == null || parsedEgressBps == null) {
      toast.error(t('tunnels.bandwidthNonNegative'));
      return;
    }

    const allowedSourceCIDRs = parseSourceCIDRInput(sourceCidrs);
    const socks5AllowedTargetPorts = parseCommaSeparatedPortList(socks5TargetPorts);

    if (isSocks5) {
      const effectiveSocks5AuthType = socks5AuthEnabled ? 'username_password' : 'none';
      if (effectiveSocks5AuthType === 'username_password' && (!socks5Username.trim() || (!isEditing && !socks5Password))) {
        setFieldError(localFieldError('ingress.config.auth', t('tunnels.socks5AuthRequired')));
        return;
      }
      if (!isClientToClient && effectiveSocks5AuthType === 'none' && !confirmNoAuthRisk) {
        setFieldError(localFieldError('confirm_no_auth_risk', t('tunnels.socks5NoAuthRequired')));
        return;
      }
      if (!Number.isInteger(parsedSocks5DialTimeout) || parsedSocks5DialTimeout < 1 || parsedSocks5DialTimeout > 120) {
        setFieldError(localFieldError('target.config.dial_timeout_seconds', t('tunnels.socks5DialTimeoutInvalid')));
        return;
      }
      if (socks5AllowedTargetPorts === null) {
        setFieldError(localFieldError('target.config.allowed_target_ports', portErrorMessage));
        return;
      }
    }
    if (isHttp && httpAuthEnabled && (!httpUsername.trim() || (!isEditing && !httpPassword))) {
      setFieldError(localFieldError('ingress.config.auth', t('tunnels.httpAuthRequired')));
      return;
    }

    if (!isClientToClient && !isHttp && parsedRemotePort && !isPortAllowedByRanges(parsedRemotePort, status?.allowed_ports)) {
      const message = t('tunnels.portMustBeAllowed', { ranges: formatPortRanges(status?.allowed_ports) });
      setFieldError({ field: 'remote_port', message, code: 'port_not_allowed' });
      toast.error(message);
      return;
    }

    if (props.mode === 'edit') {
      const tunnel = props.tunnel;
      if (!tunnel) return;

      updateTunnel.mutate(
        {
          clientId: tunnel.owner_client_id ?? tunnel.client_id ?? tunnel.clientId,
          tunnelId: tunnel.id,
          expected_revision: tunnel.revision,
          topology,
          ingress_client_id: isClientToClient ? selectedIngressClientId : undefined,
          bind_ip: isClientToClient
            ? bindIp
            : undefined,
          name,
          type,
          local_ip: isSocks5 ? '' : localIp,
          local_port: parsedLocalPort ?? 0,
          remote_port: parsedRemotePort ?? 0,
          domain,
          allowed_source_cidrs: allowedSourceCIDRs,
          ingress_bps: parsedIngressBps,
          egress_bps: parsedEgressBps,
          http_auth: isHttp ? {
            enabled: httpAuthEnabled,
            username: httpUsername,
            password: httpPassword,
          } : undefined,
          socks5: isSocks5 ? {
            auth_type: socks5AuthEnabled ? 'username_password' : 'none',
            username: socks5Username,
            password: socks5Password,
            allowed_target_cidrs: parseCommaSeparatedList(socks5TargetCidrs),
            allowed_target_hosts: parseCommaSeparatedList(socks5TargetHosts),
            allowed_target_ports: socks5AllowedTargetPorts ?? [],
            dial_timeout_seconds: parsedSocks5DialTimeout,
          } : undefined,
          confirm_no_auth_risk: isSocks5 ? confirmNoAuthRisk : undefined,
        },
        {
          onSuccess: () => {
            setFieldError(null);
            setOpen(false);
            toast.success(t('tunnels.updated', { name }));
          },
          onError: (err) => {
            setFieldError(serverFieldError(err));
            toast.error(getTunnelMutationErrorMessage(err));
          },
        },
      );
      return;
    }

    createTunnel.mutate(
      {
        clientId: selectedTargetClientId,
        topology,
        ingress_client_id: isClientToClient ? selectedIngressClientId : undefined,
        bind_ip: isClientToClient ? bindIp : undefined,
        name,
        type,
        local_ip: isSocks5 ? '' : localIp,
        local_port: parsedLocalPort ?? 0,
        remote_port: parsedRemotePort ?? 0,
        domain,
        allowed_source_cidrs: allowedSourceCIDRs,
        ingress_bps: parsedIngressBps,
        egress_bps: parsedEgressBps,
        http_auth: isHttp ? {
          enabled: httpAuthEnabled,
          username: httpUsername,
          password: httpPassword,
        } : undefined,
        socks5: isSocks5 ? {
          auth_type: socks5AuthEnabled ? 'username_password' : 'none',
          username: socks5Username,
          password: socks5Password,
          allowed_target_cidrs: parseCommaSeparatedList(socks5TargetCidrs),
          allowed_target_hosts: parseCommaSeparatedList(socks5TargetHosts),
          allowed_target_ports: socks5AllowedTargetPorts ?? [],
          dial_timeout_seconds: parsedSocks5DialTimeout,
        } : undefined,
        confirm_no_auth_risk: isSocks5 ? confirmNoAuthRisk : undefined,
      },
      {
        onSuccess: () => {
          setFieldError(null);
          setOpen(false);
          toast.success(t('tunnels.created', { name }));
        },
        onError: (err) => {
          setFieldError(serverFieldError(err));
          toast.error(getTunnelMutationErrorMessage(err));
        },
      },
    );
  };

  const parsedIngressBps = parseMbpsInputToBps(ingressBps);
  const parsedEgressBps = parseMbpsInputToBps(egressBps);
  const effectiveTypeOptions = isClientToClient
    ? typeOptions.filter((opt) => opt.value !== 'http')
    : typeOptions;
  const isValid = Boolean(
    name.trim()
    && selectedTargetClientId
    && (isSocks5 || parsedLocalPort !== null)
    && (isClientToClient ? canUseClientToClient && selectedIngressClientId && bindIp.trim() && type !== 'http' : true)
    && (isHttp ? domain.trim() : parsedRemotePort !== null)
    && (isClientToClient || isHttp || (parsedRemotePort !== null && isPortAllowedByRanges(parsedRemotePort, status?.allowed_ports)))
    && (!isHttp || !httpAuthEnabled || (httpUsername.trim() && (isEditing || httpPassword)))
    && (!isSocks5 || (!socks5AuthEnabled || (socks5Username.trim() && (isEditing || socks5Password))))
    && (!isSocks5 || isClientToClient || socks5AuthEnabled || confirmNoAuthRisk)
    && (!isSocks5 || parseCommaSeparatedPortList(socks5TargetPorts) !== null)
    && parsedIngressBps !== null
    && parsedEgressBps !== null,
  );

  return (
    <DialogContent className="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{isEdit ? t('tunnels.editTitle') : t('tunnels.createTitle')}</DialogTitle>
        {props.mode === 'edit' && (
          <DialogDescription>
            {t('tunnels.editDescription', { name: props.tunnel?.name ?? '' })}
          </DialogDescription>
        )}
      </DialogHeader>

      <form onSubmit={handleSubmit} className="space-y-4">
        {/* 隧道名称 */}
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t('tunnels.name')}</label>
          <Input
            aria-label={t('tunnels.name')}
            placeholder={t('tunnels.namePlaceholder')}
            value={name}
            onChange={(e) => {
              clearMutationFeedback();
              setName(e.target.value);
            }}
            autoFocus
          />
          <FieldErrorText error={fieldError} fields={['name']} />
        </div>

        {/* 协议类型 */}
        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t('tunnels.topology')}</label>
          <div className="grid grid-cols-2 gap-2">
            <Button
              type="button"
              variant={topology === 'server_expose' ? 'default' : 'outline'}
              onClick={() => {
                clearMutationFeedback();
                setTopology('server_expose');
              }}
            >
              {t('tunnels.serverExpose')}
            </Button>
            <ClientToClientTopologyButton
              selected={topology === 'client_to_client'}
              disabled={!canUseClientToClient}
              label={t('tunnels.clientToClient')}
              tooltip={t('tunnels.c2cRequiresTwoClients')}
              onSelect={() => {
                if (!canUseClientToClient) {
                  return;
                }
                clearMutationFeedback();
                setTopology('client_to_client');
                if (type === 'http') setType('tcp');
              }}
            />
          </div>
          <FieldErrorText error={fieldError} fields={['topology', 'transport_policy']} />
        </div>

        {(isClientToClient || clients.length > 1) && (
          <div className={cn('grid gap-3', isClientToClient ? 'grid-cols-2' : 'grid-cols-1')}>
            <div className="space-y-1.5">
              <label className="text-sm font-medium">{t('tunnels.sourceClient')}</label>
              {clients.length > 0 ? (
                <select
                  aria-label={t('tunnels.sourceClient')}
                  className={selectClassName}
                  value={selectedTargetClientId}
                  disabled={isEdit}
                  onChange={(e) => {
                    clearMutationFeedback();
                    const nextTargetClientId = e.target.value;
                    setTargetClientId(nextTargetClientId);
                    if (ingressClientId === nextTargetClientId) {
                      setIngressClientId('');
                    }
                  }}
                >
                  {clients.map((client) => (
                    <option key={client.id} value={client.id}>
                      {getClientDisplayName(client)}
                    </option>
                  ))}
                </select>
              ) : (
                <Input value={sourceClient ? getClientDisplayName(sourceClient) : selectedTargetClientId} disabled />
              )}
              <FieldErrorText error={fieldError} fields={['target.client_id', 'client_id']} />
            </div>
            {isClientToClient && (
              <div className="space-y-1.5">
                <label className="text-sm font-medium">{t('tunnels.ingressClient')}</label>
                <select
                  aria-label={t('tunnels.ingressClient')}
                  className={selectClassName}
                  value={selectedIngressClientId}
                  onChange={(e) => {
                    clearMutationFeedback();
                    setIngressClientId(e.target.value);
                  }}
                >
                  {ingressClientOptions.map((client) => (
                    <option key={client.id} value={client.id}>
                      {getClientDisplayName(client)}
                    </option>
                  ))}
                </select>
                <FieldErrorText error={fieldError} fields={['ingress.client_id']} />
              </div>
            )}
          </div>
        )}

        <div className="space-y-1.5">
          <label className="text-sm font-medium">{t('tunnels.protocolType')}</label>
          <div className="flex gap-2">
            {effectiveTypeOptions.map((opt) => (
              <Button
                key={opt.value}
                type="button"
                variant={type === opt.value ? 'default' : 'outline'}
                className="flex-1"
                onClick={() => {
                  clearMutationFeedback();
                  setType(opt.value);
                }}
              >
                {opt.label}
              </Button>
            ))}
          </div>
          <FieldErrorText error={fieldError} fields={['target.type', 'ingress.type']} />
        </div>

        {!isSocks5 && (
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <label className="text-sm font-medium">{isClientToClient ? t('tunnels.targetAddress') : t('tunnels.localIp')}</label>
              <Input
                aria-label={isClientToClient ? t('tunnels.targetAddress') : t('tunnels.localIp')}
                placeholder="127.0.0.1"
                value={localIp}
                onChange={(e) => {
                  clearMutationFeedback();
                  setLocalIp(e.target.value);
                }}
              />
              <FieldErrorText error={fieldError} fields={['target.config.ip', 'target.config.host', 'target.config', 'local_ip']} />
            </div>
            <div className="space-y-1.5">
              <label className="text-sm font-medium">{isClientToClient ? t('tunnels.targetPort') : t('tunnels.localPort')}</label>
              <Input
                aria-label={isClientToClient ? t('tunnels.targetPort') : t('tunnels.localPort')}
                type="number"
                placeholder="e.g. 22"
                value={localPort}
                onChange={(e) => {
                  clearMutationFeedback();
                  setLocalPort(e.target.value);
                }}
                min={1}
                max={65535}
              />
              <FieldErrorText error={fieldError} fields={['target.config.port', 'local_port']} />
              {localPort && !parsedLocalPort && (
                <p className="text-[11px] font-medium text-destructive">{portErrorMessage}</p>
              )}
            </div>
          </div>
        )}

        {isHttp ? (
          <div className="space-y-1.5">
            <label className="text-sm font-medium">{t('tunnels.domain')}</label>
            <Input
              aria-label={t('tunnels.domain')}
              placeholder="e.g. app.example.com"
              value={domain}
              onChange={(e) => {
                clearMutationFeedback();
                setDomain(e.target.value);
              }}
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
            />
            <FieldErrorText error={fieldError} fields={['domain', 'ingress.config.domain']} />
            <p className="text-[11px] text-muted-foreground mt-1.5">
              {t('tunnels.httpDomainHelp')}
            </p>
          </div>
        ) : (
          <div className={cn('grid gap-3', isClientToClient ? 'grid-cols-2' : 'grid-cols-1')}>
            {isClientToClient && (
              <div className="space-y-1.5">
                <label className="text-sm font-medium">{t('tunnels.bindAddress')}</label>
                <Input
                  aria-label={t('tunnels.bindAddress')}
                  placeholder="0.0.0.0"
                  value={bindIp}
                  onChange={(e) => {
                    clearMutationFeedback();
                    setBindIp(e.target.value);
                  }}
                  autoCapitalize="none"
                  autoCorrect="off"
                  spellCheck={false}
                />
                <FieldErrorText error={fieldError} fields={['ingress.config.bind_ip', 'bind_ip']} />
              </div>
            )}
            <div className="space-y-1.5">
              <label className="text-sm font-medium">{isClientToClient ? t('tunnels.bindPort') : t('tunnels.publicPort')}</label>
              <Input
                aria-label={isClientToClient ? t('tunnels.bindPort') : t('tunnels.publicPort')}
                type="number"
                placeholder="e.g. 18080"
                value={remotePort}
                onChange={(e) => {
                  clearMutationFeedback();
                  setRemotePort(e.target.value);
                }}
                min={1}
                max={65535}
              />
              <FieldErrorText error={fieldError} fields={['remote_port', 'ingress.config.port']} />
              {remotePort && !parsedRemotePort && (
                <p className="text-[11px] font-medium text-destructive">{portErrorMessage}</p>
              )}
              {!isClientToClient && (
                <p className="text-[11px] text-muted-foreground mt-1.5">
                  {t('tunnels.portRangeAllowed')}
                  {status?.allowed_ports === undefined
                    ? t('common.loading')
                    : formatPortRanges(status.allowed_ports)}
                </p>
              )}
            </div>
          </div>
        )}

        <div className="rounded-lg border border-border bg-muted/20">
          <button
            type="button"
            className="flex w-full items-center justify-between px-3 py-2 text-left text-sm font-medium"
            onClick={() => setAdvancedOpen((value) => !value)}
            aria-expanded={advancedOpen}
          >
            <span>{t('tunnels.advancedSettings')}</span>
            <ChevronDown className={cn('h-4 w-4 transition-transform', advancedOpen && 'rotate-180')} />
          </button>
          {advancedOpen && (
            <div className="space-y-4 border-t border-border px-3 py-3">
              <div className="space-y-1.5">
                <label className="text-sm font-medium">{t('tunnels.sourceCidrs')}</label>
                <Input
                  aria-label={t('tunnels.sourceCidrs')}
                  placeholder="0.0.0.0/0, ::/0"
                  value={sourceCidrs}
                  onChange={(e) => {
                    clearMutationFeedback();
                    setSourceCidrs(preserveLoopbackSourceCIDRsOnFirstRestriction(sourceCidrs, e.target.value));
                  }}
                />
                <FieldErrorText error={fieldError} fields={['ingress.config.allowed_source_cidrs', 'ingress.config']} />
                <p className="text-[11px] text-muted-foreground">
                  {t('tunnels.sourceCidrsHelp')}
                </p>
                {showLoopbackCIDRWarning && (
                  <p className="text-[11px] text-amber-600 dark:text-amber-400">
                    {t('tunnels.sourceCidrsLoopbackWarning')}
                  </p>
                )}
                {isHttp && (
                  <p className="text-[11px] text-muted-foreground">
                    {t('tunnels.httpSourceCidrsProxyHelp')}
                  </p>
                )}
              </div>

              {isHttp && (
                <div className="space-y-2">
                  <label className="flex items-center gap-2 text-sm font-medium">
                    <input
                      type="checkbox"
                      checked={httpAuthEnabled}
                      onChange={(e) => {
                        clearMutationFeedback();
                        setHttpAuthEnabled(e.target.checked);
                      }}
                    />
                    <span>{t('tunnels.httpAuth')}</span>
                  </label>
                  {httpAuthEnabled && (
                    <div className="grid grid-cols-2 gap-3">
                      <Input
                        aria-label={t('tunnels.httpUsername')}
                        placeholder={t('tunnels.httpUsername')}
                        value={httpUsername}
                        onChange={(e) => {
                          clearMutationFeedback();
                          setHttpUsername(e.target.value);
                        }}
                      />
                      <Input
                        aria-label={t('tunnels.httpPassword')}
                        placeholder={t('tunnels.httpPassword')}
                        type="password"
                        value={httpPassword}
                        onChange={(e) => {
                          clearMutationFeedback();
                          setHttpPassword(e.target.value);
                        }}
                      />
                    </div>
                  )}
                  <FieldErrorText error={fieldError} fields={['ingress.config.auth', 'ingress.config.auth.username', 'ingress.config.auth.password']} />
                </div>
              )}

              {isSocks5 && (
                <div className="space-y-3">
                  <div className="space-y-1.5">
                    <label className="text-sm font-medium">{t('tunnels.socks5DialTimeout')}</label>
                    <Input
                      aria-label={t('tunnels.socks5DialTimeout')}
                      type="number"
                      value={socks5DialTimeout}
                      onChange={(e) => {
                        clearMutationFeedback();
                        setSocks5DialTimeout(e.target.value);
                      }}
                      min={1}
                      max={120}
                    />
                    <FieldErrorText error={fieldError} fields={['target.config.dial_timeout_seconds']} />
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div className="space-y-1.5">
                      <label className="text-sm font-medium">{t('tunnels.socks5TargetCidrs')}</label>
                      <Input
                        aria-label={t('tunnels.socks5TargetCidrs')}
                        value={socks5TargetCidrs}
                        onChange={(e) => {
                          clearMutationFeedback();
                          setSocks5TargetCidrs(e.target.value);
                        }}
                      />
                      <FieldErrorText error={fieldError} fields={['target.config.allowed_target_cidrs', 'target.config']} />
                    </div>
                    <div className="space-y-1.5">
                      <label className="text-sm font-medium">{t('tunnels.socks5TargetPorts')}</label>
                      <Input
                        aria-label={t('tunnels.socks5TargetPorts')}
                        value={socks5TargetPorts}
                        onChange={(e) => {
                          clearMutationFeedback();
                          setSocks5TargetPorts(e.target.value);
                        }}
                      />
                      <FieldErrorText error={fieldError} fields={['target.config.allowed_target_ports']} />
                    </div>
                  </div>
                  <div className="space-y-1.5">
                    <label className="text-sm font-medium">{t('tunnels.socks5TargetHosts')}</label>
                    <Input
                      aria-label={t('tunnels.socks5TargetHosts')}
                      value={socks5TargetHosts}
                      onChange={(e) => {
                        clearMutationFeedback();
                        setSocks5TargetHosts(e.target.value);
                      }}
                    />
                    <FieldErrorText error={fieldError} fields={['target.config.allowed_target_hosts']} />
                  </div>
                  <div className="space-y-2">
                    <label className="flex items-center gap-2 text-sm font-medium">
                      <input
                        type="checkbox"
                        checked={socks5AuthEnabled}
                        onChange={(e) => {
                          clearMutationFeedback();
                          setSocks5AuthEnabled(e.target.checked);
                        }}
                      />
                      <span>{t('tunnels.socks5Auth')}</span>
                    </label>
                    {socks5AuthEnabled && (
                      <div className="grid grid-cols-2 gap-3">
                        <Input
                          aria-label={t('tunnels.socks5Username')}
                          placeholder={t('tunnels.socks5Username')}
                          value={socks5Username}
                          onChange={(e) => {
                            clearMutationFeedback();
                            setSocks5Username(e.target.value);
                          }}
                        />
                        <Input
                          aria-label={t('tunnels.socks5Password')}
                          placeholder={t('tunnels.socks5Password')}
                          type="password"
                          value={socks5Password}
                          onChange={(e) => {
                            clearMutationFeedback();
                            setSocks5Password(e.target.value);
                          }}
                        />
                      </div>
                    )}
                    <FieldErrorText error={fieldError} fields={['ingress.config.auth', 'ingress.config.auth.username', 'ingress.config.auth.password']} />
                    {!isClientToClient && !socks5AuthEnabled && (
                      <label className="flex items-start gap-2 text-xs text-muted-foreground">
                        <input
                          type="checkbox"
                          className="mt-0.5"
                          checked={confirmNoAuthRisk}
                          onChange={(e) => {
                            clearMutationFeedback();
                            setConfirmNoAuthRisk(e.target.checked);
                          }}
                        />
                        <span>{t('tunnels.socks5NoAuthConfirm')}</span>
                      </label>
                    )}
                    <FieldErrorText error={fieldError} fields={['confirm_no_auth_risk']} />
                  </div>
                </div>
              )}

              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <label className="text-sm font-medium">{t('tunnels.ingressLimit')}</label>
                  <InputGroup>
                    <InputGroupInput
                      aria-label={t('tunnels.ingressLimit')}
                      type="number"
                      step="any"
                      placeholder="0"
                      value={ingressBps}
                      onChange={(e) => {
                        clearMutationFeedback();
                        setIngressBps(e.target.value);
                      }}
                      min={0}
                    />
                    <InputGroupAddon align="inline-end">
                      <InputGroupText>MiB/s</InputGroupText>
                    </InputGroupAddon>
                  </InputGroup>
                  <FieldErrorText error={fieldError} fields={['ingress_bps', 'bandwidth_settings.ingress_bps']} />
                </div>
                <div className="space-y-1.5">
                  <label className="text-sm font-medium">{t('tunnels.egressLimit')}</label>
                  <InputGroup>
                    <InputGroupInput
                      aria-label={t('tunnels.egressLimit')}
                      type="number"
                      step="any"
                      placeholder="0"
                      value={egressBps}
                      onChange={(e) => {
                        clearMutationFeedback();
                        setEgressBps(e.target.value);
                      }}
                      min={0}
                    />
                    <InputGroupAddon align="inline-end">
                      <InputGroupText>MiB/s</InputGroupText>
                    </InputGroupAddon>
                  </InputGroup>
                  <FieldErrorText error={fieldError} fields={['egress_bps', 'bandwidth_settings.egress_bps']} />
                </div>
              </div>
            </div>
          )}
        </div>

        {mutation.isError && (
          <div className="flex items-center gap-2 text-sm text-destructive bg-destructive/10 px-3 py-2 rounded-lg mt-2">
            <AlertTriangle className="w-4 h-4 shrink-0" />
            {getTunnelMutationErrorMessage(mutation.error)}
          </div>
        )}

        <DialogFooter>
          {isEdit && (
            <Button
              type="button"
              variant="outline"
              onClick={() => setOpen(false)}
            >
              {t('common.cancel')}
            </Button>
          )}
          <Button
            type="submit"
            disabled={!isValid || mutation.isPending}
          >
            {mutation.isPending
              ? (isEdit ? t('tunnels.updating') : t('tunnels.creating'))
              : (isEdit ? t('tunnels.saveChanges') : t('tunnels.createTunnel'))}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  );
}
