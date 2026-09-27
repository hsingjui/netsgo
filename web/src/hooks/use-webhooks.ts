import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import type { QueryClient, UseMutationOptions } from '@tanstack/react-query';

import { webhookApi } from '@/lib/api';
import type { ActivityWebhookConfig, WebhookEventKey, WebhookInvocation, WebhookInvocationStatus } from '@/types/webhook';

export const webhookCatalogQueryKey = ['webhook-catalog'] as const;
export const webhooksQueryKey = ['webhooks'] as const;

export function webhookDeliveriesQueryKey(webhookId: string, status?: WebhookInvocationStatus) {
  return ['webhook-deliveries', webhookId, status ?? 'all'] as const;
}

export function webhookDeliveryQueryKey(deliveryId: string) {
  return ['webhook-delivery', deliveryId] as const;
}

export function upsertWebhookInCache(queryClient: QueryClient, saved: ActivityWebhookConfig): void {
  queryClient.setQueryData<ActivityWebhookConfig[]>(webhooksQueryKey, (current = []) => (
    current.some((item) => item.id === saved.id)
      ? current.map((item) => item.id === saved.id ? saved : item)
      : [...current, saved]
  ));
}

export function removeWebhookFromCache(queryClient: QueryClient, webhookId: string): void {
  queryClient.setQueryData<ActivityWebhookConfig[]>(webhooksQueryKey, (current = []) => (
    current.filter((item) => item.id !== webhookId)
  ));
  queryClient.removeQueries({ queryKey: ['webhook-deliveries', webhookId] });
}

export function cacheDeliveryRefresh(queryClient: QueryClient, delivery: WebhookInvocation): void {
  queryClient.setQueryData<WebhookInvocation>(webhookDeliveryQueryKey(delivery.id), delivery);
  void queryClient.invalidateQueries({ queryKey: ['webhook-deliveries', delivery.webhookId] });
}

export function saveWebhookMutationOptions(queryClient: QueryClient): UseMutationOptions<ActivityWebhookConfig, Error, ActivityWebhookConfig> {
  return {
    mutationFn: (config) => (config.revision === 0 ? webhookApi.create(config) : webhookApi.update(config)),
    onSuccess: (saved) => {
      upsertWebhookInCache(queryClient, saved);
    },
  };
}

export function deleteWebhookMutationOptions(queryClient: QueryClient): UseMutationOptions<void, Error, string> {
  return {
    mutationFn: (webhookId) => webhookApi.delete(webhookId),
    onSuccess: (_, webhookId) => {
      removeWebhookFromCache(queryClient, webhookId);
    },
  };
}

export function testWebhookMutationOptions(queryClient: QueryClient): UseMutationOptions<WebhookInvocation, Error, { config: ActivityWebhookConfig; event: WebhookEventKey }> {
  return {
    mutationFn: ({ config, event }) => webhookApi.test(config, event),
    onSuccess: (delivery) => {
      cacheDeliveryRefresh(queryClient, delivery);
    },
  };
}

export function replayWebhookDeliveryMutationOptions(queryClient: QueryClient): UseMutationOptions<WebhookInvocation, Error, string> {
  return {
    mutationFn: (deliveryId) => webhookApi.replay(deliveryId),
    onSuccess: (delivery) => {
      cacheDeliveryRefresh(queryClient, delivery);
      void queryClient.invalidateQueries({ queryKey: webhooksQueryKey });
    },
  };
}

export function toggleWebhookMutationOptions(queryClient: QueryClient): UseMutationOptions<ActivityWebhookConfig, Error, { webhookId: string; enabled: boolean }> {
  return {
    mutationFn: ({ webhookId, enabled }) => webhookApi.setEnabled(webhookId, enabled),
    onSuccess: (saved) => {
      upsertWebhookInCache(queryClient, saved);
    },
  };
}

export function useWebhookCatalog() {
  return useQuery({
    queryKey: webhookCatalogQueryKey,
    queryFn: () => webhookApi.catalog(),
    staleTime: Infinity,
  });
}

export function useWebhooks() {
  return useQuery({
    queryKey: webhooksQueryKey,
    queryFn: () => webhookApi.list(),
  });
}

export function useSaveWebhook() {
  const queryClient = useQueryClient();
  return useMutation(saveWebhookMutationOptions(queryClient));
}

export function useDeleteWebhook() {
  const queryClient = useQueryClient();
  return useMutation(deleteWebhookMutationOptions(queryClient));
}

export function useToggleWebhook() {
  const queryClient = useQueryClient();
  return useMutation(toggleWebhookMutationOptions(queryClient));
}

export function useWebhookDeliveries(webhookId: string | null, status?: WebhookInvocationStatus) {
  return useInfiniteQuery({
    queryKey: webhookDeliveriesQueryKey(webhookId ?? 'none', status),
    enabled: Boolean(webhookId),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) => {
      if (!webhookId) throw new Error('webhook id is required');
      return webhookApi.deliveries(webhookId, status, pageParam);
    },
    getNextPageParam: (lastPage) => (lastPage.has_more ? lastPage.next_cursor : undefined),
    refetchInterval: (query) => {
      const items = query.state.data?.pages.flatMap((page) => page.items) ?? [];
      return items.some((item) => item.status === 'queued' || item.status === 'retrying') ? 1500 : false;
    },
  });
}

export function useWebhookDelivery(deliveryId: string | null) {
  return useQuery({
    queryKey: webhookDeliveryQueryKey(deliveryId ?? 'none'),
    enabled: Boolean(deliveryId),
    queryFn: () => {
      if (!deliveryId) throw new Error('delivery id is required');
      return webhookApi.delivery(deliveryId);
    },
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      return status === 'queued' || status === 'retrying' ? 1000 : false;
    },
  });
}

export function useTestWebhook() {
  const queryClient = useQueryClient();
  return useMutation(testWebhookMutationOptions(queryClient));
}

export function useReplayWebhookDelivery() {
  const queryClient = useQueryClient();
  return useMutation(replayWebhookDeliveryMutationOptions(queryClient));
}
