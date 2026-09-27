import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { api } from '@/lib/api';
import { webhooksQueryKey } from '@/hooks/use-webhooks';

export interface WebhookDeliverySettings {
  allow_private_targets: boolean;
  daily_delivery_cap: number;
}

export interface WebhookDeliverySettingsUpdateResult extends WebhookDeliverySettings {
  disabled_webhooks: number;
}

export const webhookSettingsQueryKey = ['webhook-settings'] as const;

export function useWebhookSettings() {
  return useQuery({
    queryKey: webhookSettingsQueryKey,
    queryFn: () => api.get<WebhookDeliverySettings>('/api/admin/settings/webhooks'),
  });
}

export function useWebhookSettingsMutations() {
  const queryClient = useQueryClient();
  const invalidate = () => queryClient.invalidateQueries({ queryKey: webhookSettingsQueryKey });

  return {
    updateSettings: useMutation({
      mutationFn: (settings: WebhookDeliverySettings) =>
        api.put<WebhookDeliverySettingsUpdateResult>('/api/admin/settings/webhooks', settings),
      onSuccess: (result) => {
        invalidate();
        if (result?.disabled_webhooks > 0) {
          void queryClient.invalidateQueries({ queryKey: webhooksQueryKey });
        }
      },
    }),
  };
}
