/*
 * The find bar's state, as the store keeps it.
 *
 * The keystroke can arrive from anywhere, so `openFind` is global while the bar
 * itself is per tab. Two things follow, and both used to be wrong in a way a
 * component test cannot see: the bar must not open when there is nothing to
 * search, and leaving a tab must *close* it properly — `find.open = false` alone
 * left the previous tab's highlights on screen, because the store only clears the
 * search of whichever tab is active and by then the id had already moved.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

import type { Tab } from './store';

async function freshStore() {
  vi.resetModules();
  return import('./store');
}

function tab(id: string): Tab {
  return {
    id,
    title: id,
    target: `tester@127.0.0.1/${id}`,
    status: 'open',
    bytesIn: 0,
    bytesOut: 0,
    startedAt: Date.now(),
  };
}

beforeEach(() => {
  localStorage.clear();
});

describe('find state', () => {
  it('does not open when there is no tab to search', async () => {
    const store = await freshStore();
    expect(store.state.activeTabId).toBeNull();
    store.openFind();
    expect(store.find.open).toBe(false);
  });

  it('closes when the tab changes — and only then', async () => {
    const store = await freshStore();
    const [a, b] = [tab('a'), tab('b')];
    store.state.tabs.push(a, b);

    store.activateTab('a');
    store.openFind();
    expect(store.find.open).toBe(true);

    // A pointerdown inside the pane already in front is not a switch: the bar the
    // user just opened must not vanish under their cursor.
    store.activateTab('a');
    expect(store.find.open).toBe(true);

    store.activateTab('b');
    expect(store.find.open).toBe(false);
    expect(store.find.index).toBe(0);
    expect(store.find.count).toBe(0);
  });

  it('closes when a tab is opened without going through activateTab', async () => {
    // Connect, Duplicate and Reconnect all assign `state.activeTabId` themselves;
    // the watcher is what makes that safe. Before it, the bar stayed open and
    // carried the previous tab's count onto the new one.
    const store = await freshStore();
    store.state.tabs.push(tab('a'), tab('b'));
    store.activateTab('a');
    store.openFind();
    store.find.index = 3;
    store.find.count = 12;

    store.state.activeTabId = 'b';
    expect(store.find.open).toBe(false);
    expect(store.find.count).toBe(0);
  });

  it('closes when the last tab goes away', async () => {
    const store = await freshStore();
    store.state.tabs.push(tab('only'));
    store.activateTab('only');
    store.openFind();

    store.closeTab('only');
    expect(store.state.activeTabId).toBeNull();
    expect(store.find.open).toBe(false);
  });

  it('reports a count, and forgets it once closed', async () => {
    const store = await freshStore();
    store.state.tabs.push(tab('a'));
    store.activateTab('a');
    store.openFind();
    store.find.index = 2;
    store.find.count = 7;

    store.closeFind();
    expect(store.find).toMatchObject({ open: false, index: 0, count: 0 });
  });
});
