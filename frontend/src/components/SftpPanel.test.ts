/*
 * The panel renders the store and nothing else, which is exactly what these
 * tests pin down: directories sort first, opening one navigates, the toolbar
 * buttons call the store's actions, and a failure is shown rather than a
 * misleading empty folder.
 */
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const backendMock = vi.hoisted(() => ({
  sftpList: vi.fn(),
  sftpMkdir: vi.fn(async () => undefined),
  sftpRemove: vi.fn(async () => undefined),
  sftpRename: vi.fn(async () => undefined),
  sftpUpload: vi.fn(async () => [] as string[]),
  sftpDownload: vi.fn(async () => ''),
}));

vi.mock('../lib/backend', () => ({
  backend: backendMock,
  fileBridgeApi: () => ({}),
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import SftpPanel from './SftpPanel.vue';
import { refreshSftp, state, type Tab } from '../lib/store';

function seedSession(): void {
  const tab: Tab = {
    id: 'tab-sftp',
    title: 'web-01',
    target: 'root@10.0.0.5:22',
    hostId: 'h_1',
    sessionId: 'sess-1',
    status: 'open',
    bytesIn: 0,
    bytesOut: 0,
    startedAt: Date.now(),
  };
  state.tabs.splice(0, state.tabs.length, tab);
  state.activeTabId = tab.id;
  state.notices.splice(0);
  state.sftp.open = true;
  state.sftp.tabId = tab.id;
  state.sftp.sessionId = 'sess-1';
  state.sftp.path = '/home/me';
  state.sftp.entries = [
    { name: 'notes.txt', path: '/home/me/notes.txt', dir: false, size: 2048, mode: '-rw-r--r--', modTime: '2026-01-02T03:04:05Z' },
    { name: 'work', path: '/home/me/work', dir: true, size: 0, mode: 'drwxr-xr-x', modTime: '2026-01-02T03:04:05Z' },
  ];
  state.sftp.loading = false;
  state.sftp.error = '';
  state.sftp.transfers = [];
}

beforeEach(() => {
  backendMock.sftpList.mockReset();
  backendMock.sftpList.mockResolvedValue({ path: '/home/me/work', entries: [] });
  seedSession();
});

describe('SftpPanel', () => {
  it('lists directories before files and opens one on double click', async () => {
    const wrapper = mount(SftpPanel);

    const names = wrapper.findAll('[data-test="sftp-entry"]').map((row) => row.find('button').text());
    expect(names).toEqual(['work', 'notes.txt']);

    await wrapper.findAll('[data-test="sftp-entry"]')[0]?.trigger('dblclick');
    await flushPromises();

    expect(backendMock.sftpList).toHaveBeenCalledWith('sess-1', '/home/me/work');
    expect(state.sftp.path).toBe('/home/me/work');
  });

  it('uploads into the listed directory', async () => {
    const wrapper = mount(SftpPanel);

    await wrapper.find('[data-test="sftp-upload"]').trigger('click');
    await flushPromises();

    expect(backendMock.sftpUpload).toHaveBeenCalledWith('sess-1', '/home/me');
  });

  it('shows the backend error and can be closed', async () => {
    backendMock.sftpList.mockRejectedValue(new Error('permission denied'));
    const wrapper = mount(SftpPanel);

    await refreshSftp('/root');
    await flushPromises();

    expect(wrapper.find('[data-test="sftp-error"]').text()).toContain('permission denied');

    await wrapper.find('[data-test="sftp-close"]').trigger('click');
    expect(state.sftp.open).toBe(false);
  });
});
