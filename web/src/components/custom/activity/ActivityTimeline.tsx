import { useEffect, useMemo, useRef } from 'react';
import { Activity, LoaderCircle, ScanLine, TriangleAlert } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { ActivityItem } from './ActivityItem';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { useActivity } from '@/hooks/use-activity';
import type { ActivityReadScope } from '@/lib/api';
import { activityDayKey, formatActivityDay } from '@/lib/activity-format';
import type { ActivityItem as ActivityItemType, ActivityQuery } from '@/types';

function TimelineSkeleton({ rows }: { rows: number }) {
  return (
    <div className="divide-y divide-border/40">
      {Array.from({ length: rows }, (_, index) => (
        <div key={index} className="grid grid-cols-[5rem_1rem_minmax(0,1fr)] items-start gap-x-2 px-3 py-3 sm:px-4">
          <div className="flex flex-col items-end gap-1">
            <Skeleton className="h-4 w-11" />
            <Skeleton className="h-5 w-14 rounded-full" />
          </div>
          <div className="flex h-5 justify-center"><Skeleton className="mt-1.5 size-2.5 rounded-full" /></div>
          <div className="flex flex-col gap-1.5 py-0.5">
            <Skeleton className="h-3.5 w-[45%]" />
            <Skeleton className="h-2.5 w-24" />
          </div>
        </div>
      ))}
    </div>
  );
}

function TimelineState({ icon: Icon, title, description, action }: {
  icon: typeof Activity;
  title: string;
  description: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex flex-col items-center gap-2.5 px-6 py-14 text-center">
      <span className="flex size-9 items-center justify-center rounded-full bg-muted/60 text-muted-foreground">
        <Icon className="size-4" strokeWidth={1.75} />
      </span>
      <p className="text-sm font-medium text-foreground">{title}</p>
      <p className="max-w-xs text-xs leading-5 text-muted-foreground">{description}</p>
      {action ? <div className="mt-1">{action}</div> : null}
    </div>
  );
}

export function ActivityTimeline({
  readScope,
  query,
  compact = false,
}: {
  readScope: ActivityReadScope;
  query: ActivityQuery;
  compact?: boolean;
}) {
  const { t } = useTranslation();
  const activity = useActivity(readScope, query);
  const loadMoreRef = useRef<HTMLDivElement>(null);
  const {
    fetchNextPage,
    hasNextPage,
    isFetchNextPageError,
    isFetchingNextPage,
  } = activity;

  const groups = useMemo(() => {
    const byDay = new Map<string, ActivityItemType[]>();
    for (const item of activity.items) {
      const key = activityDayKey(item.occurred_at);
      const bucket = byDay.get(key);
      if (bucket) bucket.push(item);
      else byDay.set(key, [item]);
    }
    return [...byDay.entries()];
  }, [activity.items]);

  useEffect(() => {
    const target = loadMoreRef.current;
    if (
      compact
      || !target
      || !hasNextPage
      || isFetchingNextPage
      || isFetchNextPageError
      || typeof IntersectionObserver === 'undefined'
    ) return;

    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        observer.disconnect();
        void fetchNextPage();
      }
    }, { rootMargin: '320px 0px' });
    observer.observe(target);
    return () => observer.disconnect();
  }, [compact, fetchNextPage, hasNextPage, isFetchNextPageError, isFetchingNextPage]);

  if (activity.isLoading) {
    return <TimelineSkeleton rows={compact ? 4 : 8} />;
  }
  if (activity.isError && activity.items.length === 0) {
    return (
      <TimelineState
        icon={TriangleAlert}
        title={t('activity.loadFailed')}
        description={t('activity.loadFailedHelp')}
        action={<Button variant="outline" size="sm" onClick={() => activity.refetch()}>{t('common.retry')}</Button>}
      />
    );
  }
  if (activity.items.length === 0) {
    return (
      <TimelineState
        icon={ScanLine}
        title={t('activity.emptyTitle')}
        description={t('activity.emptyDescription')}
      />
    );
  }

  return (
    <div className="divide-y divide-border/40">
      {groups.map(([day, items]) => (
        <section key={day}>
          <header className="sticky top-0 z-10 border-b border-border/40 bg-card/85 px-3 py-1.5 backdrop-blur-sm sm:px-4">
            <span className="text-xs font-medium text-muted-foreground">{formatActivityDay(items[0].occurred_at)}</span>
          </header>
          <div className="divide-y divide-border/40">
            {items.map((item) => <ActivityItem key={item.id} item={item} omitSubjectId={query.scopeId} />)}
          </div>
        </section>
      ))}
      {compact ? null : (
        <div ref={loadMoreRef} className="flex min-h-14 items-center justify-center px-4 py-3" aria-live="polite">
          {isFetchNextPageError ? (
            <div className="flex items-center gap-2">
              <span className="text-xs text-muted-foreground">{t('activity.loadMoreFailed')}</span>
              <Button variant="outline" size="sm" onClick={() => fetchNextPage()}>{t('common.retry')}</Button>
            </div>
          ) : isFetchingNextPage ? (
            <span className="flex items-center gap-2 text-xs text-muted-foreground">
              <LoaderCircle className="size-4 animate-spin" />
              {t('activity.loadingOlder')}
            </span>
          ) : hasNextPage ? (
            <span className="text-xs text-muted-foreground/70">{t('activity.scrollForMore')}</span>
          ) : (
            <span className="text-xs text-muted-foreground/60">{t('activity.endOfRecord')}</span>
          )}
        </div>
      )}
    </div>
  );
}
