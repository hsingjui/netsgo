import { type Page } from '@playwright/test';

import { expect, test } from './fixtures';
import { e2eURL, login, uniqueTunnelName } from './helpers';

type ActivityWebhookAPI = {
  id: string;
  name: string;
};

async function listWebhooks(page: Page): Promise<ActivityWebhookAPI[]> {
  const response = await page.request.get(e2eURL('/api/webhooks'), { timeout: 10_000 });
  if (!response.ok()) {
    throw new Error(`list Webhooks failed: ${response.status()} ${await response.text()}`);
  }
  return response.json();
}

async function cleanupWebhook(page: Page, name: string) {
  const webhook = (await listWebhooks(page)).find((item) => item.name === name);
  if (!webhook) return;
  const response = await page.request.delete(e2eURL(`/api/webhooks/${encodeURIComponent(webhook.id)}`), { timeout: 10_000 });
  if (!response.ok() && response.status() !== 404) {
    throw new Error(`delete Webhook during cleanup failed: ${response.status()} ${await response.text()}`);
  }
}

test('user can create, test-deliver, inspect, and delete an activity Webhook @webhook', async ({ page }, testInfo) => {
  const name = uniqueTunnelName('playwright-webhook');
  let deleted = false;

  try {
    page.setDefaultTimeout(15_000);
    page.setDefaultNavigationTimeout(20_000);
    await login(page);
    const policy = await page.request.get(e2eURL('/api/admin/settings/webhooks'));
    if (policy.ok()) {
      const enable = await page.request.put(e2eURL('/api/admin/settings/webhooks'), {
        data: { allow_private_targets: true, daily_delivery_cap: 50 },
      });
      if (!enable.ok()) {
        throw new Error(`enable private webhook targets failed: ${enable.status()} ${await enable.text()}`);
      }
    }
    await page.goto(e2eURL('/#/dashboard/webhooks'));
    await expect(page.getByRole('heading', { name: 'Webhooks' })).toBeVisible();
    await page.getByRole('button', { name: 'New webhook' }).click();

    const sheet = page.getByRole('dialog', { name: 'Activity log webhooks' });
    await expect(sheet).toBeVisible();
    await sheet.getByLabel('Webhook name').fill(name);

    await sheet.getByRole('radio', { name: 'Tunnel', exact: true }).click();
    const tunnelChangeDialog = page.getByRole('alertdialog', { name: 'Change listening object?' });
    await expect(tunnelChangeDialog).toBeVisible();
    await tunnelChangeDialog.getByRole('button', { name: 'Change object' }).click();

    const tunnelEventCheckboxes = sheet.locator('[data-webhook-field="events"] [role="checkbox"]');
    await expect(tunnelEventCheckboxes).toHaveCount(10);
    for (const checkbox of await tunnelEventCheckboxes.all()) {
      await expect(checkbox).not.toBeChecked();
    }
    await sheet.getByLabel('Tunnel runtime error').check();

    const urlInput = sheet.getByPlaceholder('https://example.com/webhooks/netsgo');
    await page.context().grantPermissions(['clipboard-write']);
    await urlInput.locator('..').getByRole('button', { name: 'Copy variable' }).click();
    const variableList = page.locator('[data-slot="webhook-variable-list"]');
    await expect(variableList).toBeVisible();
    await expect(variableList.getByRole('button', { name: /Client ID/ })).toBeVisible();
    await expect(variableList.getByRole('button', { name: /Client name/ })).toBeVisible();
    await expect(variableList.getByRole('button', { name: /Client hostname/ })).toHaveCount(0);
    await expect(variableList.getByRole('button', { name: /Activity event ID/ })).toHaveCount(0);
    await expect(variableList.getByRole('button', { name: /Event type/ })).toHaveCount(0);
    await expect(variableList.getByRole('button', { name: /Webhook ID/ })).toHaveCount(0);
    await variableList.getByRole('button', { name: /Client ID/ }).click();
    await expect(page.getByText('Variable {{client.id}} copied to clipboard.')).toBeVisible();
    await page.keyboard.press('Escape');

    await sheet.getByRole('radio', { name: 'Client', exact: true }).click();
    const clientChangeDialog = page.getByRole('alertdialog', { name: 'Change listening object?' });
    await expect(clientChangeDialog).toBeVisible();
    await clientChangeDialog.getByRole('button', { name: 'Change object' }).click();
    await sheet.getByRole('radio', { name: 'All', exact: true }).click();
    await sheet.getByLabel('Client online').check();

    await urlInput.locator('..').getByRole('button', { name: 'Copy variable' }).click();
    await expect(variableList).toBeVisible();
    await variableList.getByRole('button', { name: /Client name/ }).click();
    await expect(page.getByText('Variable {{client.name}} copied to clipboard.')).toBeVisible();
    await page.keyboard.press('Escape');

    await urlInput.fill('http://webhook-receiver:18085/hook?client={{client.id}}&delivery={{delivery.id}}');
    await sheet.getByRole('button', { name: 'Save and enable' }).click();
    await expect(page.getByText('Webhook created.', { exact: true })).toBeVisible();
    await expect.poll(async () => (await listWebhooks(page)).some((item) => item.name === name)).toBe(true);

    await sheet.getByRole('button', { name: 'Test request', exact: true }).click();
    const testDialog = page.getByRole('dialog', { name: 'Test webhook' });
    await expect(testDialog).toBeVisible();
    await testDialog.getByRole('button', { name: 'Send test request' }).click();
    await expect(testDialog.getByText('Delivered', { exact: true })).toBeVisible({ timeout: 30_000 });
    await expect(testDialog.getByText('200', { exact: true })).toBeVisible();
    await testDialog.getByRole('button', { name: 'Close', exact: true }).first().click();

    await sheet.getByRole('tab', { name: /Delivery log/ }).click();
    const row = sheet.getByRole('row').filter({ hasText: 'Client online' }).filter({ hasText: 'Test call' });
    await expect(row).toBeVisible();
    await expect(row).toContainText('Delivered');
    await expect(row).toContainText('200');
    await row.getByRole('button', { name: 'Details' }).click();

    const detail = page.getByRole('dialog', { name: 'Delivery details' });
    await expect(detail).toBeVisible();
    await expect(detail).toContainText('http://webhook-receiver:18085/hook');
    await expect(detail).toContainText('playwright webhook response');
    await expect(detail).toContainText('#1');
    await page.keyboard.press('Escape');
    await expect(detail).toBeHidden();

    await sheet.getByRole('button', { name: 'Delete webhook' }).last().click();
    const deleteDialog = page.getByRole('alertdialog', { name: 'Delete this webhook?' });
    await expect(deleteDialog).toBeVisible();
    await deleteDialog.getByRole('button', { name: 'Delete webhook' }).click();
    await expect(page.getByText('Webhook deleted.', { exact: true })).toBeVisible();
    await expect.poll(async () => (await listWebhooks(page)).some((item) => item.name === name)).toBe(false);
    deleted = true;
  } finally {
    if (!deleted) {
      try {
        await cleanupWebhook(page, name);
      } catch (error) {
        await testInfo.attach('webhook-cleanup-error', {
          body: String(error),
          contentType: 'text/plain',
        });
      }
    }
  }
});
