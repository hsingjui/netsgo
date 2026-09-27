import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { ArrowUpRight } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { ActivityFilters } from './ActivityFilters';
import { ActivityTimeline } from './ActivityTimeline';
import { defaultActivityFilter, type ActivityFilterValue } from './severity-meta';
import { Button } from '@/components/ui/button';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { getClientDisplayName } from '@/lib/client-utils';
import type { ActivityQuery, Client } from '@/types';
import type { ResourceScope } from '@/lib/resource-scope';

const allTunnelsValue = '__all__';

export function ClientActivitySheet({
  scope,
  client,
  open,
  onOpenChange,
}: {
  scope: ResourceScope;
  client: Client;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation();
  const [tunnelId, setTunnelId] = useState(allTunnelsValue);
  const [filters, setFilters] = useState<ActivityFilterValue>(defaultActivityFilter);

  const tunnels = client.proxies ?? [];
  const tunnelScoped = tunnelId !== allTunnelsValue;
  const query: ActivityQuery = {
    scope: tunnelScoped ? 'tunnel' : 'client',
    scopeId: tunnelScoped ? tunnelId : client.id,
    limit: 50,
    severities: filters.severities,
    categories: filters.categories,
  };

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-[min(92vw,44rem)] gap-0 sm:max-w-none">
        <SheetHeader className="border-b border-border/50 pr-12">
          <SheetTitle>{t('activity.clientTimelineTitle', { name: getClientDisplayName(client) })}</SheetTitle>
          <SheetDescription>{t('activity.clientTimelineDescription')}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-2 border-b border-border/40 bg-muted/20 px-4 py-2.5">
          <div className="flex min-w-0 items-center gap-1.5">
            {tunnels.length > 0 ? (
              <Select value={tunnelId} onValueChange={setTunnelId}>
                <SelectTrigger size="sm" className="h-7 min-w-0 flex-1 gap-1.5 text-xs shadow-none">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={allTunnelsValue}>{t('activity.allTunnels')}</SelectItem>
                  {tunnels.map((tunnel) => (
                    <SelectItem key={tunnel.id} value={tunnel.id}>{tunnel.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : null}
            <Button
              asChild
              variant="ghost"
              size="sm"
              className="ml-auto h-7 shrink-0 gap-1 whitespace-nowrap px-2 text-xs font-normal text-muted-foreground hover:text-foreground"
            >
              <Link
                to="/dashboard/activity"
                search={{
                  scope: tunnelScoped ? 'tunnel' : 'client',
                  client_id: tunnelScoped ? undefined : client.id,
                  tunnel_id: tunnelScoped ? tunnelId : undefined,
                  user_id: scope.kind === 'admin-user' ? scope.userId : undefined,
                  severity: filters.severities,
                  category: filters.categories,
                }}
              >
                {t('activity.openFullPage')}
                <ArrowUpRight className="size-3.5" />
              </Link>
            </Button>
          </div>
          <ActivityFilters value={filters} onChange={setFilters} showRange={false} />
        </div>
        <ScrollArea className="min-h-0 flex-1 pb-6">
          {open ? <ActivityTimeline readScope={scope} query={query} /> : null}
        </ScrollArea>
      </SheetContent>
    </Sheet>
  );
}
