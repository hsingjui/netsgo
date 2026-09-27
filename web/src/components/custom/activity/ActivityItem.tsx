import { AlertTriangle, CircleAlert } from 'lucide-react';
import type { LucideIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';

import { Badge } from '@/components/ui/badge';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { formatActivityAbsoluteTime, formatActivityClock, formatActivityRelativeTime } from '@/lib/activity-format';
import { cn } from '@/lib/utils';
import type {
  ActivityClientSubject,
  ActivityItem as ActivityItemType,
  ActivitySeverity,
} from '@/types';

const severityAccentIcon: Partial<Record<ActivitySeverity, LucideIcon>> = {
  warning: AlertTriangle,
  error: CircleAlert,
};

const severityMarkerClass: Record<ActivitySeverity, string> = {
  debug: 'bg-muted-foreground/40',
  info: 'bg-sky-500/80',
  warning: 'text-amber-500',
  error: 'text-rose-500',
};

interface ActivityReference {
  key: string;
  relation: string;
  name: string;
}

function readableClientName(item: ActivityItemType, clientId: string, t: TFunction) {
  const subject = item.clients.find((candidate) => candidate.client_id === clientId);
  return subject?.display_name?.trim() || subject?.hostname?.trim() || t('activity.unknownClient');
}

function readableTunnelName(item: ActivityItemType, tunnelId: string, t: TFunction) {
  const subject = item.tunnels.find((candidate) => candidate.tunnel_id === tunnelId);
  return subject?.name?.trim() || t('activity.unknownTunnel');
}

function activitySummary(item: ActivityItemType, t: TFunction) {
  if (item.payload_version !== 1 || !item.payload.summary_key) return t('activity.unknownSummary');
  const args = { ...item.payload.summary_args };
  // 主题资源的当前名称优先；缺失时也回填，兼容未携带名称的旧记录
  // （如 P2P attach/detach 事件），模板没有对应占位符时不会受影响。
  const client = item.clients.find((subject) => subject.relation === 'subject');
  const tunnel = item.tunnels.find((subject) => subject.relation === 'subject');
  if (client) args.client_name = readableClientName(item, client.client_id, t);
  if (tunnel) args.tunnel_name = readableTunnelName(item, tunnel.tunnel_id, t);
  return t(item.payload.summary_key, {
    ...args,
    defaultValue: t('activity.unknownSummary'),
  });
}

// 摘要通常已经点名了操作方；system/unknown 只是记录方占位，没有信息量。
function actorLabel(item: ActivityItemType, summary: string, t: TFunction) {
  if (item.actor.type === 'system' || item.actor.type === 'unknown') return '';
  const typeLabel = t(`activity.actor.${item.actor.type}`);
  const subject = item.actor.type === 'client' && item.actor.id
    ? item.clients.find((candidate) => candidate.client_id === item.actor.id)
    : undefined;
  const rawName = (subject ? readableClientName(item, subject.client_id, t) : '') || item.actor.name?.trim();
  const name = rawName && rawName !== item.actor.id ? rawName : '';
  const echoesCategory = item.actor.type === item.category;
  if (!name || summary.includes(name)) {
    return echoesCategory ? '' : typeLabel;
  }
  return echoesCategory || !typeLabel ? name : `${typeLabel} ${name}`;
}

const clientRelationPriority: Record<ActivityClientSubject['relation'], number> = {
  target: 0,
  ingress: 1,
  owner: 2,
  peer: 3,
  subject: 4,
  related: 5,
};

function clientRelationLabel(item: ActivityItemType, subject: ActivityClientSubject, t: TFunction) {
  if (item.category === 'tunnel' && item.action === 'migrated' && subject.relation === 'related') {
    return t('activity.relation.client.previousTarget');
  }
  return t(`activity.relation.client.${subject.relation}`);
}

function clientReferences(item: ActivityItemType, summary: string, omitSubjectId: string | undefined, t: TFunction) {
  const references: ActivityReference[] = [];
  const seen = new Set<string>();
  const subjects = [...item.clients].sort(
    (left, right) => clientRelationPriority[left.relation] - clientRelationPriority[right.relation],
  );
  for (const subject of subjects) {
    if (subject.client_id === omitSubjectId || seen.has(subject.client_id)) continue;
    seen.add(subject.client_id);
    const name = subject.display_name?.trim() || subject.hostname?.trim() || t('activity.unknownClient');
    if (summary.includes(name)) continue;
    references.push({
      key: `client:${subject.client_id}`,
      relation: clientRelationLabel(item, subject, t),
      name,
    });
  }
  return references;
}

function tunnelReferences(item: ActivityItemType, summary: string, omitSubjectId: string | undefined, t: TFunction) {
  const references: ActivityReference[] = [];
  const seen = new Set<string>();
  for (const subject of item.tunnels) {
    if (subject.tunnel_id === omitSubjectId || seen.has(subject.tunnel_id)) continue;
    seen.add(subject.tunnel_id);
    const name = subject.name?.trim() || t('activity.unknownTunnel');
    if (summary.includes(name)) continue;
    references.push({
      key: `tunnel:${subject.tunnel_id}`,
      relation: t(`activity.relation.tunnel.${subject.relation}`),
      name,
    });
  }
  return references;
}

function activityReferences(item: ActivityItemType, summary: string, omitSubjectId: string | undefined, t: TFunction) {
  return [
    ...clientReferences(item, summary, omitSubjectId, t),
    ...tunnelReferences(item, summary, omitSubjectId, t),
  ];
}
// 离线原因只展示可归因的条目。链路中断（断网、进程退出、代理断开）无法归因，
// 只显示“已离线”，不把断开表现当成原因；服务端仍完整记录 reason_code 供审计。
const offlineCauseCode: Record<string, true> = {
  normal_closure: true,
  server_shutdown: true,
  timeout: true,
  user_disabled: true,
  replaced: true,
};

function reasonText(item: ActivityItemType, t: TFunction) {
  const code = item.payload.reason_code;
  if (!code || code === 'unknown') return '';
  if (item.category === 'client' && item.action === 'offline' && !offlineCauseCode[code]) return '';
  return t(`activity.reason.${code}`, { defaultValue: '' });
}

export function ActivityItem({ item, omitSubjectId }: { item: ActivityItemType; omitSubjectId?: string }) {
  const { t } = useTranslation();
  const summary = activitySummary(item, t);
  const AccentIcon = severityAccentIcon[item.severity];
  const reason = reasonText(item, t);
  const reasonLabel = item.category === 'client' && item.action === 'offline'
    ? t('activity.diagnostic.clientOffline')
    : t('activity.diagnostic.reason');
  const actor = actorLabel(item, summary, t);
  const references = activityReferences(item, summary, omitSubjectId, t);
  const hasMetadata = Boolean(actor) || references.length > 0;

  return (
    <article className="relative grid grid-cols-[5rem_1rem_minmax(0,1fr)] items-start gap-x-2 px-3 py-3 transition-colors hover:bg-muted/30 sm:px-4">
      <div className="flex min-w-0 flex-col items-end gap-1">
        <Tooltip>
          <TooltipTrigger asChild>
            <time className="cursor-default text-right text-xs leading-5 tabular-nums text-muted-foreground/80" dateTime={item.occurred_at}>
              {formatActivityClock(item.occurred_at)}
            </time>
          </TooltipTrigger>
          <TooltipContent>
            {formatActivityAbsoluteTime(item.occurred_at)} · {formatActivityRelativeTime(item.occurred_at)}
          </TooltipContent>
        </Tooltip>
        <Badge variant="secondary">{t(`activity.category.${item.category}`)}</Badge>
      </div>
      <div className="relative min-h-5 self-stretch">
        <span className="absolute bottom-[-0.75rem] left-1/2 top-[-0.75rem] w-px -translate-x-1/2 bg-muted-foreground/20" aria-hidden />
        {AccentIcon ? (
          <AccentIcon className={cn('absolute left-1/2 top-0.5 size-4 -translate-x-1/2', severityMarkerClass[item.severity])} strokeWidth={2.25} />
        ) : (
          <span className={cn('absolute left-1/2 top-1.5 size-2.5 -translate-x-1/2 rounded-full', severityMarkerClass[item.severity])} />
        )}
        <span className="sr-only">{t(`activity.severity.${item.severity}`)}</span>
      </div>
      <div className="min-w-0">
        <p className="text-sm font-medium leading-5 text-foreground">{summary}</p>
        {reason ? (
          <p className="mt-0.5 text-xs leading-5 text-muted-foreground">
            <span className="font-medium text-foreground/70">{reasonLabel}{t('activity.metadata.separator')}</span>
            {reason}
          </p>
        ) : null}
        {hasMetadata ? (
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs leading-5 text-muted-foreground">
            {actor ? <span>{t('activity.metadata.actor', { actor })}</span> : null}
            {references.map((reference) => (
              <span key={reference.key} className="min-w-0">
                {reference.relation}{t('activity.metadata.separator')}<span className="font-medium text-foreground/75">{reference.name}</span>
              </span>
            ))}
          </div>
        ) : null}
      </div>
    </article>
  );
}
