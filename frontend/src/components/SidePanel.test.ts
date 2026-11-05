/*
 * Where the file browser is entered from.
 *
 * SFTP only works on a live session, so the sidebar lists exactly those — a
 * closed tab must not offer a folder that cannot be listed, and an empty list
 * has to say why rather than look broken.
 */
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('trzsz', () => ({
  TrzszFilter: class {
    processServerOutput(): void {}
    processTerminalInput(): void {}
    processBinaryInput(): void {}
    isTransferringFiles(): boolean {
      return false;
    }
    setTerminalColumns(): void {}
    stopTransferringFiles(): void {}
  },
}));

const backendMock = vi.hoisted(() => ({
  sftpList: vi.fn(async () => ({ path: '/home/me', entries: [] })),
  deleteHost: vi.fn(async () => undefined),
  listHosts: vi.fn(async () => []),
}));

vi.mock('../lib/backend', () => ({
  backend: backendMock,
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import SidePanel from './SidePanel.vue';
import { SIDEBAR_WIDTH_DEFAULT, state, type Tab } from '../lib/store';

function tab(overrides: Partial<Tab> = {}): Tab {
  return {
    id: 'tab-live',
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
  state.hosts.splice(0);
  state.notices.splice(0);
  state.sftp.open = false;
  state.sftp.tabId = null;
  state.sftp.sessionId = null;
  state.settings.sidebarWidth = SIDEBAR_WIDTH_DEFAULT;
  state.sidebarOpen = true;
  localStorage.removeItem('goterm.settings');
  backendMock.sftpList.mockClear();
}

beforeEach(() => seed());

describe('SidePanel files section', () => {
  it('offers one entry per live session and opens the browser on it', async () => {
    seed(tab(), tab({ id: 'tab-dead', title: 'old-box', status: 'closed' }));
    const wrapper = mount(SidePanel);

    const entries = wrapper.findAll('[data-test="sidebar-sftp-entry"]');
    expect(entries).toHaveLength(1);
    expect(entries[0]?.text()).toContain('web-01');

    await entries[0]?.trigger('click');
    await flushPromises();

    expect(state.sftp.open).toBe(true);
    expect(state.sftp.sessionId).toBe('sess-1');
    expect(backendMock.sftpList).toHaveBeenCalledWith('sess-1', '');
  });

  it('says what to do when nothing is connected', () => {
    seed(tab({ status: 'closed' }));
    const wrapper = mount(SidePanel);

    expect(wrapper.findAll('[data-test="sidebar-sftp-entry"]')).toHaveLength(0);
    expect(wrapper.find('[data-test="sidebar-files"]').text()).toContain('Connect a host');
  });

  it('marks the session whose panel is already open', async () => {
    seed(tab());
    const wrapper = mount(SidePanel);
    await wrapper.find('[data-test="sidebar-sftp-entry"]').trigger('click');
    await flushPromises();

    expect(wrapper.find('[data-test="sidebar-sftp-entry"]').classes()).toContain('text-[var(--text)]');
  });
});

describe('SidePanel width', () => {
  it('follows the pointer while dragging and remembers where it landed', async () => {
    seed(tab());
    const wrapper = mount(SidePanel);
    const aside = wrapper.get('[data-test="sidebar"]');
    const handle = wrapper.get('[data-test="sidebar-resizer"]');

    expect((aside.element as HTMLElement).style.width).toBe(`${SIDEBAR_WIDTH_DEFAULT}px`);

    await handle.trigger('pointerdown', { button: 0, pointerId: 1, clientX: 300 });
    await handle.trigger('pointermove', { pointerId: 1, clientX: 380 });
    // Wider by exactly the distance the pointer moved.
    expect((aside.element as HTMLElement).style.width).toBe('348px');
    // Nothing written mid-drag: localStorage gets one value, not sixty a second.
    expect(localStorage.getItem('goterm.settings')).toBeNull();

    await handle.trigger('pointerup', { pointerId: 1, clientX: 380 });
    expect(localStorage.getItem('goterm.settings')).toContain('"sidebarWidth":348');
  });

  it('caps the drag at half the window', async () => {
    seed(tab());
    const wrapper = mount(SidePanel);
    const aside = wrapper.get('[data-test="sidebar"]');
    const handle = wrapper.get('[data-test="sidebar-resizer"]');
    const cap = Math.floor(window.innerWidth / 2);

    await handle.trigger('pointerdown', { button: 0, pointerId: 1, clientX: 300 });
    await handle.trigger('pointermove', { pointerId: 1, clientX: 5000 });
    expect((aside.element as HTMLElement).style.width).toBe(`${cap}px`);
  });

  it('folds the sidebar away when dragged to the edge, keeping the width it had', async () => {
    seed(tab());
    state.settings.sidebarWidth = 300;
    const wrapper = mount(SidePanel);
    const aside = wrapper.get('[data-test="sidebar"]');
    const handle = wrapper.get('[data-test="sidebar-resizer"]');

    await handle.trigger('pointerdown', { button: 0, pointerId: 1, clientX: 300 });
    await handle.trigger('pointermove', { pointerId: 1, clientX: 250 });
    expect((aside.element as HTMLElement).style.width).toBe('250px');

    // 300 + (20 - 300) is past the snap point: collapse, not a sliver.
    await handle.trigger('pointermove', { pointerId: 1, clientX: 20 });
    expect(state.sidebarOpen).toBe(false);
    // Reopening must not restore a 20px panel.
    expect(state.settings.sidebarWidth).toBe(300);
    expect(JSON.parse(localStorage.getItem('goterm.settings') ?? '{}').sidebarWidth).toBe(300);
  });

  it('gives up width when the window shrinks', async () => {
    seed(tab());
    const wrapper = mount(SidePanel);
    const original = window.innerWidth;
    try {
      const half = Math.floor(original / 2);
      state.settings.sidebarWidth = half;
      await wrapper.vm.$nextTick();

      Object.defineProperty(window, 'innerWidth', { configurable: true, value: half });
      window.dispatchEvent(new Event('resize'));

      await vi.waitFor(() => expect(state.settings.sidebarWidth).toBe(Math.floor(half / 2)));
    } finally {
      Object.defineProperty(window, 'innerWidth', { configurable: true, value: original });
    }
  });

  it('resets to the default on double-click', async () => {
    seed(tab());
    const wrapper = mount(SidePanel);
    const aside = wrapper.get('[data-test="sidebar"]');
    const handle = wrapper.get('[data-test="sidebar-resizer"]');

    state.settings.sidebarWidth = 400;
    await wrapper.vm.$nextTick();
    await handle.trigger('dblclick');

    expect((aside.element as HTMLElement).style.width).toBe(`${SIDEBAR_WIDTH_DEFAULT}px`);
  });
});
