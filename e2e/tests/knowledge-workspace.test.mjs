import { after, test } from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import { chromium } from 'playwright-core';

import { startFakeProvider } from '../harness/fake-provider.mjs';
import { startBackend, stopAll } from '../harness/process-manager.mjs';
import { copyFixtureWorkspace } from '../harness/workspace-manager.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const focusableSelector = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
const cleanup = [];

after(async () => {
  await stopAll(cleanup);
});

test('Explore traps focus and restores it to its launcher', { timeout: 60_000 }, async () => {
  const provider = await startFakeProvider({ scenario: 'happy-path' });
  cleanup.push(provider);
  const workspace = await copyFixtureWorkspace({
    root,
    fixture: 'examples/tinycommerce',
    prefix: 'codeatlas-knowledge-focus-e2e-',
  });
  cleanup.push(workspace);
  const backend = await startBackend({
    root,
    workspaceDir: workspace.path,
    providerBaseURL: provider.baseURL,
  });
  cleanup.push(backend);

  const browser = await chromium.launch({ channel: 'chrome', headless: true });
  cleanup.push({ close: () => browser.close() });
  const page = await browser.newPage();
  await page.goto(backend.baseURL, { waitUntil: 'domcontentloaded' });

  const launcher = page.locator('[data-action="open-explore"]');
  await launcher.focus();
  await launcher.click();
  await page.locator('.knowledge-shell[role="dialog"][aria-modal="true"]').waitFor({ state: 'visible' });
  assert.equal(await page.evaluate(() => document.activeElement?.getAttribute('data-mode')), 'wiki');
  assert.equal(await page.locator('.app-shell').evaluate((element) => element.inert), true);

  await focusBoundary(page, 'first');
  await page.keyboard.press('Shift+Tab');
  assert.equal(await activeAtBoundary(page, 'last'), true);
  await page.keyboard.press('Tab');
  assert.equal(await activeAtBoundary(page, 'first'), true);

  await page.locator('.knowledge-header [data-action="close"]').click();
  assert.equal(await page.evaluate(() => document.activeElement?.getAttribute('data-action')), 'open-explore');
  assert.equal(await page.locator('.app-shell').evaluate((element) => element.inert), false);
});

async function focusBoundary(page, boundary) {
  await page.evaluate(({ selector, wanted }) => {
    const overlay = document.querySelector('.knowledge-overlay');
    const focusable = [...overlay.querySelectorAll(selector)]
      .filter((element) => element.getClientRects().length > 0);
    (wanted === 'first' ? focusable[0] : focusable.at(-1))?.focus();
  }, { selector: focusableSelector, wanted: boundary });
}

async function activeAtBoundary(page, boundary) {
  return page.evaluate(({ selector, wanted }) => {
    const overlay = document.querySelector('.knowledge-overlay');
    const focusable = [...overlay.querySelectorAll(selector)]
      .filter((element) => element.getClientRects().length > 0);
    return document.activeElement === (wanted === 'first' ? focusable[0] : focusable.at(-1));
  }, { selector: focusableSelector, wanted: boundary });
}
