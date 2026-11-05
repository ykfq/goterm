/*
 * The SFTP panel's state machine, without the panel.
 *
 * The rules that matter are all in the store: which session the browser is
 * attached to, what one navigation does, that a stale reply cannot overwrite a
 * newer listing, and that a panel whose tab went away closes itself. The Vue
 * component only draws this; if these hold, the panel is safe to click.
 */
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
  sftpList: vi.fn(),
  sftpMkdir: vi.fn(async () => undefined),
  sftpRemove: vi.fn(async () => undefined),
  sftpRename: vi.fn(async () => undefined),
  sftpUpload: vi.fn(async () => [] as string[]),
  sftpDownload: vi.fn(async () => ''),
  closeSession: vi.fn(async () => undefined),
  sendInputB64: vi.fn(async () => undefined),
  resizePTY: vi.fn(async () => undefined),
  setIdleTimeout: vi.fn(async (hours: number) => hours),
  fonts: vi.fn(async () => []),
}));

vi.mock('./backend', () => ({
  backend: backendMock,
  fileBridgeApi: () => ({}),
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import type { SFTPListing } from './protocol';
import { closeTab, openSftp, refreshSftp, sftpGoUp, sftpUpload, state, type Tab } from './store';

function tab(overrides: Partial<Tab> = {}): Tab {
  return {
    id: 'tab-sftp',
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

function listing(path: string, names: string[]): SFTPListing {
  return {
    path,
    entries: names.map((name) => ({
      name,
      path: `${path === '/' ? '' : path}/${name}`,
      dir: name.includes('.') === false,
      size: 12,
      mode: '-rw-r--r--',
      modTime: '2026-01-02T03:04:05Z',
    })),
  };
}

beforeEach(() => {
  state.tabs.splice(0, state.tabs.length);
  state.activeTabId = null;
  state.notices.splice(0);
  state.sftp.open = false;
  state.sftp.tabId = null;
  state.sftp.sessionId = null;
  state.sftp.path = '';
  state.sftp.entries = [];
  state.sftp.error = '';
  state.sftp.transfers = [];
  backendMock.sftpList.mockReset();
  backendMock.sftpUpload.mockReset();
  backendMock.sftpUpload.mockResolvedValue([]);
  backendMock.closeSession.mockClear();
});

describe('sftp store', () => {
  it('opens on a live session and lists the login directory', async () => {
    state.tabs.push(tab());
    backendMock.sftpList.mockResolvedValue(listing('/home/me', ['logs', 'notes.txt']));

    openSftp('tab-sftp');

    expect(state.sftp.open).toBe(true);
    expect(state.sftp.sessionId).toBe('sess-1');
    // "" is the backend's word for "wherever this login starts".
    expect(backendMock.sftpList).toHaveBeenCalledWith('sess-1', '');
    await vi.waitFor(() => expect(state.sftp.entries).toHaveLength(2));
    expect(state.sftp.path).toBe('/home/me');
    expect(state.sftp.loading).toBe(false);
  });

  it('refuses a tab that is not connected', () => {
    state.tabs.push(tab({ status: 'closed' }));

    openSftp('tab-sftp');

    expect(state.sftp.open).toBe(false);
    expect(backendMock.sftpList).not.toHaveBeenCalled();
    expect(state.notices.at(-1)?.level).toBe('warn');
  });

  it('walks up from the listed directory', async () => {
    state.tabs.push(tab());
    backendMock.sftpList.mockResolvedValue(listing('/home/me/work', []));
    openSftp('tab-sftp');
    await vi.waitFor(() => expect(state.sftp.path).toBe('/home/me/work'));

    backendMock.sftpList.mockResolvedValue(listing('/home/me', ['work']));
    sftpGoUp();

    expect(backendMock.sftpList).toHaveBeenLastCalledWith('sess-1', '/home/me');
    await vi.waitFor(() => expect(state.sftp.path).toBe('/home/me'));
  });

  it('surfaces a listing failure instead of an empty folder', async () => {
    state.tabs.push(tab());
    backendMock.sftpList.mockRejectedValue(new Error('permission denied'));
    openSftp('tab-sftp');

    await vi.waitFor(() => expect(state.sftp.error).toBe('permission denied'));
    expect(state.sftp.loading).toBe(false);
  });

  it('ignores a stale listing that lands after a newer one', async () => {
    state.tabs.push(tab());
    // First navigation hangs; the second answers at once. The slow reply must
    // not drag the panel back to where the user no longer is.
    let releaseFirst: (value: SFTPListing) => void = () => {};
    backendMock.sftpList.mockImplementationOnce(
      () => new Promise<SFTPListing>((resolve) => (releaseFirst = resolve)),
    );
    openSftp('tab-sftp');

    backendMock.sftpList.mockResolvedValueOnce(listing('/tmp', []));
    await refreshSftp('/tmp');
    expect(state.sftp.path).toBe('/tmp');

    releaseFirst(listing('/home/me', []));
    await vi.waitFor(() => expect(state.sftp.loading).toBe(false));
    expect(state.sftp.path).toBe('/tmp');
  });

  it('leaves the listing alone when the upload dialog is cancelled', async () => {
    state.tabs.push(tab());
    backendMock.sftpList.mockResolvedValue(listing('/home/me', []));
    openSftp('tab-sftp');
    await vi.waitFor(() => expect(state.sftp.path).toBe('/home/me'));
    backendMock.sftpList.mockClear();

    await sftpUpload();

    expect(backendMock.sftpUpload).toHaveBeenCalledWith('sess-1', '/home/me');
    expect(backendMock.sftpList).not.toHaveBeenCalled();
  });

  it('closes the panel with the tab it belongs to', async () => {
    state.tabs.push(tab());
    state.activeTabId = 'tab-sftp';
    backendMock.sftpList.mockResolvedValue(listing('/home/me', []));
    openSftp('tab-sftp');
    await vi.waitFor(() => expect(state.sftp.path).toBe('/home/me'));

    closeTab('tab-sftp');

    expect(state.sftp.open).toBe(false);
    expect(state.sftp.sessionId).toBeNull();
    expect(state.sftp.entries).toHaveLength(0);
  });
});
