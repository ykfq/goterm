/*
 * The tab strip: the right-click menu (Reconnect re-dials a saved host,
 * Duplicate opens a second terminal on the connection that is already up, both
 * only offered when they can work — a disabled entry with a reason beats a click
 * that does nothing) and dragging a tab along the strip to reorder the tabs.
 */
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const backendMock = vi.hoisted(() => ({
  duplicateSession: vi.fn(async () => 'sess-dup'),
  listSessions: vi.fn(async () => []),
  knownHosts: vi.fn(async () => []),
  listHosts: vi.fn(async () => []),
}));

vi.mock('../lib/backend', () => ({
  backend: backendMock,
  fileBridgeApi: () => backendMock,
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import TabBar from './TabBar.vue';
import { resetSettings, state } from '../lib/store';
import type { Tab } from '../lib/store';

function tab(overrides: Partial<Tab> = {}): Tab {
  return {
    id: 'tab-1',
    title: 'web-01',
    target: 'root@10.0.0.5:22',
    hostId: 'h_1',
    sessionId: 'sess-1',
    status: 'open',
    bytesIn: 0,
    bytesOut: 0,
    startedAt: Date.now(),
    ...overrides,
  };
}

function seed(...tabs: Tab[]): void {
  state.tabs.splice(0, state.tabs.length, ...tabs);
  state.activeTabId = tabs[0]?.id ?? null;
  state.notices.splice(0);
}

async function openMenu(wrapper: ReturnType<typeof mount>, index = 0) {
  await wrapper.findAll('[data-test="tab"]')[index]?.trigger('contextmenu');
  await flushPromises();
  return wrapper.find('[data-test="tab-menu"]');
}

beforeEach(() => {
  resetSettings();
  backendMock.duplicateSession.mockClear();
  seed(tab());
});

describe('TabBar colours', () => {
  // The strip is window chrome, not canvas: it follows the theme in both modes.
  //
  // It used to paint the active tab with the terminal's own colour (`--term-bg`)
  // to make it merge with the pane. That looked right until a *custom* terminal
  // background was set — and a custom background is kept across theme switches on
  // purpose — at which point the active tab stayed dark under the light theme
  // while every other tab (and the rest of the shell) went light. A canvas colour
  // must not be able to reach the chrome, so the guard here is a blunt one: no
  // `--term-` token may appear on a tab at all.
  it('paints the active tab from the theme, never from the terminal canvas', () => {
    const wrapper = mount(TabBar);
    const classes = wrapper.find('[data-test="tab"]').classes();

    expect(classes).toContain('bg-[var(--bg)]');
    expect(classes).toContain('text-[var(--text)]');
    expect(classes.some((name) => name.includes('--term-'))).toBe(false);
  });

  it('leaves inactive tabs on the theme palette', () => {
    seed(tab({ id: 'a', title: 'first' }), tab({ id: 'b', title: 'second' }));
    const wrapper = mount(TabBar);
    const classes = wrapper.findAll('[data-test="tab"]')[1]?.classes() ?? [];

    expect(classes).toContain('text-[var(--text-dim)]');
    expect(classes.some((name) => name.includes('--term-'))).toBe(false);
  });
});

describe('TabBar menu', () => {
  it('offers Reconnect and Duplicate on a connected tab', async () => {
    const wrapper = mount(TabBar);
    await openMenu(wrapper);
    const reconnect = wrapper.get('[data-test="tab-reconnect"]');
    const duplicate = wrapper.get('[data-test="tab-duplicate"]');
    expect(reconnect.text()).toContain('Reconnect');
    expect(duplicate.text()).toContain('Duplicate');
    expect(reconnect.attributes('disabled')).toBeUndefined();
    expect(duplicate.attributes('disabled')).toBeUndefined();
    // Duplicate first: it is what an open tab gets asked for most, and it is the
    // one that costs nothing (no second login). Pinned because order is the kind
    // of thing a later refactor swaps back without any test noticing.
    expect(wrapper.findAll('[data-test="tab-menu"] button').map((item) => item.attributes('data-test'))).toEqual([
      'tab-duplicate',
      'tab-reconnect',
    ]);
  });

  it('duplicates onto the same connection instead of logging in again', async () => {
    const wrapper = mount(TabBar);
    await openMenu(wrapper);
    await wrapper.get('[data-test="tab-duplicate"]').trigger('click');
    await flushPromises();

    expect(backendMock.duplicateSession).toHaveBeenCalledWith('sess-1');
    expect(state.tabs).toHaveLength(2);
    const copy = state.tabs[1];
    expect(copy?.sessionId).toBe('sess-dup');
    expect(copy?.status).toBe('open');
    expect(copy?.hostId).toBe('h_1');
    // Its own title, so two identical tabs stay tellable apart.
    expect(copy?.title).toBe('web-01 (2)');
    // A duplicate is instant, so the menu closes and the tab is already active.
    expect(wrapper.find('[data-test="tab-menu"]').exists()).toBe(false);
    expect(state.activeTabId).toBe(copy?.id);
    // And it says nothing about itself. A tab that appears already connected and
    // focused is the whole message; a toast for it was noise on every duplicate.
    expect(state.notices).toEqual([]);
  });

  it('disables Duplicate when there is no live session to share', async () => {
    seed(tab({ status: 'closed' }));
    const wrapper = mount(TabBar);
    await openMenu(wrapper);
    expect(wrapper.get('[data-test="tab-duplicate"]').attributes('disabled')).toBeDefined();
    // Reconnect is still available: that is what a closed saved host needs most.
    expect(wrapper.get('[data-test="tab-reconnect"]').attributes('disabled')).toBeUndefined();
  });

  it('disables Reconnect for an ad-hoc session, which kept no credential', async () => {
    seed(tab({ hostId: undefined, title: 'adhoc' }));
    const wrapper = mount(TabBar);
    await openMenu(wrapper);
    expect(wrapper.get('[data-test="tab-reconnect"]').attributes('disabled')).toBeDefined();
  });

  it('closes the menu on Escape', async () => {
    const wrapper = mount(TabBar);
    await openMenu(wrapper);
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(wrapper.find('[data-test="tab-menu"]').exists()).toBe(false);
  });
});

describe('TabBar drag to reorder', () => {
  function threeTabs() {
    seed(tab({ id: 'a', title: 'a' }), tab({ id: 'b', title: 'b' }), tab({ id: 'c', title: 'c' }));
    return mount(TabBar);
  }

  it('moves a tab to the right of the one it is dropped on', async () => {
    const wrapper = threeTabs();
    const items = () => wrapper.findAll('[data-test="tab"]');

    await items()[0]?.trigger('dragstart');
    await items()[2]?.trigger('dragover');

    // Live, not on drop: the strip always shows the result.
    expect(state.tabs.map((current) => current.id)).toEqual(['b', 'c', 'a']);
    // The dragged tab follows the cursor and is faded while it travels; the tab it
    // is over shows the edge it came from.
    expect(items()[2]?.classes()).toContain('opacity-50');
    expect(items()[1]?.classes().join(' ')).toContain('shadow-[inset_2px_0_0_var(--accent)]');

    await items()[2]?.trigger('drop');
    expect(items()[2]?.classes()).not.toContain('opacity-50');
  });

  it('moves a tab to the left of the one it is dropped on', async () => {
    const wrapper = threeTabs();
    const items = () => wrapper.findAll('[data-test="tab"]');

    await items()[2]?.trigger('dragstart');
    await items()[0]?.trigger('dragover');

    expect(state.tabs.map((current) => current.id)).toEqual(['c', 'a', 'b']);
  });

  it('does nothing until a tab is actually being dragged', async () => {
    const wrapper = threeTabs();
    await wrapper.findAll('[data-test="tab"]')[2]?.trigger('dragover');
    expect(state.tabs.map((current) => current.id)).toEqual(['a', 'b', 'c']);
  });

  it('leaves the active tab and its id alone', async () => {
    // Reordering is a paint order, not a session change: the pane behind a tab is
    // keyed by id, so nothing reconnects or repaints because a tab moved.
    const wrapper = threeTabs();
    const items = () => wrapper.findAll('[data-test="tab"]');

    await items()[1]?.trigger('dragstart');
    await items()[0]?.trigger('dragover');

    expect(state.tabs.map((current) => current.id)).toEqual(['b', 'a', 'c']);
    expect(state.activeTabId).toBe('a');
  });
});
