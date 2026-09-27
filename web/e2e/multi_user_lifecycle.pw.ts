import { expect, test } from './fixtures';
import { e2eURL, login, uniqueTunnelName } from './helpers';

type ManagedUser = {
  id: string;
  username: string;
  status: string;
};

async function findManagedUser(page: import('@playwright/test').Page, username: string): Promise<ManagedUser | undefined> {
  const response = await page.request.get(e2eURL(`/api/admin/users?query=${encodeURIComponent(username)}`));
  if (!response.ok()) {
    throw new Error(`list managed users failed: ${response.status()} ${await response.text()}`);
  }
  const body = await response.json() as { items?: ManagedUser[] };
  return body.items?.find((user) => user.username === username);
}

async function cleanupManagedUser(page: import('@playwright/test').Page, user: ManagedUser | undefined) {
  if (!user) return;
  const current = await page.request.get(e2eURL(`/api/admin/users/${encodeURIComponent(user.id)}`));
  if (current.status() === 404) return;
  if (!current.ok()) {
    throw new Error(`load managed user for cleanup failed: ${current.status()} ${await current.text()}`);
  }
  const latest = await current.json() as ManagedUser;
  if (latest.status === 'active') {
    const disable = await page.request.post(e2eURL(`/api/admin/users/${encodeURIComponent(user.id)}/disable`));
    if (!disable.ok() && disable.status() !== 404) {
      throw new Error(`disable managed user during cleanup failed: ${disable.status()} ${await disable.text()}`);
    }
  }
  const deleted = await page.request.delete(e2eURL(`/api/admin/users/${encodeURIComponent(user.id)}`));
  if (!deleted.ok() && deleted.status() !== 404) {
    throw new Error(`delete managed user during cleanup failed: ${deleted.status()} ${await deleted.text()}`);
  }
}

test('admin can create, scope, disable, re-enable, and delete a user from the console @multi-user @lifecycle', async ({ page, request }) => {
  const username = uniqueTunnelName('playwright-user').replaceAll('-', '').slice(0, 24);
  const password = 'PlaywrightUser123!';
  let user: ManagedUser | undefined;

  try {
    await login(page);
    await page.goto(e2eURL('/#/dashboard/users'));
    await expect(page.getByRole('heading', { name: 'Users' })).toBeVisible();
    user = await findManagedUser(page, username);
    await page.getByRole('button', { name: 'Add user' }).click();
    const createDialog = page.getByRole('dialog', { name: 'Add user' });
    await expect(createDialog).toBeVisible();
    await createDialog.getByPlaceholder('Username').fill(username);
    await createDialog.getByPlaceholder('Password').fill(password);
    await createDialog.getByRole('button', { name: 'Add user' }).click();
    await expect(createDialog).toBeHidden();

    await expect.poll(async () => {
      user = await findManagedUser(page, username);
      return user?.status ?? 'missing';
    }).toBe('active');
    expect(user).toBeDefined();

    const userID = user!.id;
    const snapshotResponse = await page.request.get(e2eURL(`/api/admin/users/${encodeURIComponent(userID)}/console/snapshot`));
    expect(snapshotResponse.ok()).toBeTruthy();
    const snapshot = await snapshotResponse.json() as {
      clients?: unknown[];
      summary?: { total_clients?: number; total_tunnels?: number };
      bootstrap?: Record<string, unknown>;
      server_status?: unknown;
    };
    expect(snapshot.clients).toEqual([]);
    expect(snapshot.summary).toMatchObject({ total_clients: 0, total_tunnels: 0 });
    expect(Object.keys(snapshot.bootstrap ?? {}).sort()).toEqual([
      'allowed_ports',
      'server_addr',
      'version',
    ]);
    expect(snapshot.server_status).toBeUndefined();

    await page.getByRole('row').filter({ hasText: username }).getByRole('button', { name: 'Actions' }).click();
    await page.getByRole('menuitem', { name: 'Disable user' }).click();
    const disableDialog = page.getByRole('dialog', { name: 'Disable user' });
    await disableDialog.getByRole('button', { name: 'Confirm' }).click();
    await expect.poll(async () => (await findManagedUser(page, username))?.status ?? 'missing').toBe('disabled');

    const disabledLogin = await request.post(e2eURL('/api/auth/login'), {
      headers: { 'content-type': 'application/json' },
      data: JSON.stringify({ username, password }),
    });
    expect(disabledLogin.status()).toBe(401);
    expect((await disabledLogin.json()).code).toBe('user_disabled');

    await page.getByRole('row').filter({ hasText: username }).getByRole('button', { name: 'Actions' }).click();
    await page.getByRole('menuitem', { name: 'Enable user' }).click();
    const enableDialog = page.getByRole('dialog', { name: 'Enable user' });
    await enableDialog.getByRole('button', { name: 'Confirm' }).click();
    await expect.poll(async () => (await findManagedUser(page, username))?.status ?? 'missing').toBe('active');

    const enabledLogin = await request.post(e2eURL('/api/auth/login'), {
      headers: { 'content-type': 'application/json' },
      data: JSON.stringify({ username, password }),
    });
    expect(enabledLogin.status()).toBe(200);

    await page.getByRole('row').filter({ hasText: username }).getByRole('button', { name: 'Actions' }).click();
    await page.getByRole('menuitem', { name: 'Disable user' }).click();
    await page.getByRole('dialog', { name: 'Disable user' }).getByRole('button', { name: 'Confirm' }).click();
    await expect.poll(async () => (await findManagedUser(page, username))?.status ?? 'missing').toBe('disabled');

    await page.getByRole('row').filter({ hasText: username }).getByRole('button', { name: 'Actions' }).click();
    await page.getByRole('menuitem', { name: 'Delete user' }).click();
    const deleteDialog = page.getByRole('dialog', { name: 'Delete user' });
    await deleteDialog.getByRole('button', { name: 'Delete' }).click();
    await expect.poll(async () => (await findManagedUser(page, username)) ? 'present' : 'missing').toBe('missing');
    user = undefined;
  } finally {
    await cleanupManagedUser(page, user);
  }
});
