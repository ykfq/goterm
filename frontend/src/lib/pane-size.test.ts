/*
 * The PTY has to be told the size of the pane that is looking at it.
 *
 * The failure this pins down: a tab is created and its connection starts in the
 * same tick, but the pane is only measured a frame later — so the PTY is dialled
 * at the last known geometry (or 100x30 on the very first tab) and the fit that
 * happens while the dial is in flight goes nowhere, because there is no session
 * to tell yet. When the session arrived, the terminal was *already* the right
 * size, so "send a resize if the fit changed something" sent nothing: `htop`
 * and `systemctl status` drew a 100x30 screen inside a much bigger window until
 * the user resized it and forced a change.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('trzsz', () => ({
  TrzszFilter: class {
    private readonly options: { writeToTerminal: (output: unknown) => void };
    constructor(options: { writeToTerminal: (output: unknown) => void }) {
      this.options = options;
    }
    processServerOutput(bytes: Uint8Array): void {
      this.options.writeToTerminal(bytes);
    }
    processTerminalInput(): void {}
    processBinaryInput(): void {}
    isTransferringFiles(): boolean {
      return false;
    }
    setTerminalColumns(): void {}
    stopTransferringFiles(): void {}
  },
}));

vi.mock('./clipboard', () => ({
  writeClipboard: vi.fn(async () => true),
  readClipboard: vi.fn(async () => ''),
}));

const connect = vi.fn<(config: { cols: number; rows: number }) => Promise<string>>();
const connectHost = vi.fn<(hostId: string, cols: number, rows: number) => Promise<string>>();
const resizePTY = vi.fn<(sessionId: string, cols: number, rows: number) => Promise<void>>(async () => undefined);

vi.mock('./backend', () => ({
  backend: {
    connect: (config: { cols: number; rows: number }) => connect(config),
    connectHost: (hostId: string, cols: number, rows: number) => connectHost(hostId, cols, rows),
    resizePTY: (sessionId: string, cols: number, rows: number) => resizePTY(sessionId, cols, rows),
    listSessions: vi.fn(async () => []),
    knownHosts: vi.fn(async () => []),
    sendInputB64: vi.fn(async () => undefined),
    openURL: vi.fn(async () => undefined),
  },
  fileBridgeApi: () => ({}),
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import { activateTab, connectAdhoc, ensureBridge, resetSettings, state } from './store';

const ADHOC = {
  host: '10.0.0.5',
  port: 22,
  user: 'root',
  authType: 'password' as const,
  password: 'hunter2',
};

/** A dial that finishes when the test says so, like a real one against a host. */
function deferredDial() {
  let settle: (sessionId: string) => void = () => undefined;
  const promise = new Promise<string>((resolve) => {
    settle = resolve;
  });
  return { promise, settle: (id: string) => settle(id) };
}

beforeEach(() => {
  resetSettings();
  localStorage.clear();
  state.tabs.splice(0);
  state.notices.splice(0);
  connect.mockReset();
  connectHost.mockReset();
  resizePTY.mockReset();
  resizePTY.mockResolvedValue(undefined);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('the PTY is sized to its pane', () => {
  it('resizes the session on adoption even though the pane was already fitted', async () => {
    const dial = deferredDial();
    connect.mockReturnValue(dial.promise);

    const pending = connectAdhoc(ADHOC);
    // The tab exists before the dial resolves — that is the whole problem: the
    // pane mounts and fits while the connection is still being made.
    const tab = state.tabs.at(-1);
    expect(tab).toBeDefined();
    if (!tab) return;

    const bridge = ensureBridge(tab);
    bridge.term.resize(150, 45);

    dial.settle('session-1');
    await pending;

    expect(resizePTY).toHaveBeenCalledWith('session-1', 150, 45);
  });

  it('dials with the geometry the last pane had, not a hard-coded 100x30', async () => {
    connect.mockResolvedValue('session-1');
    await connectAdhoc(ADHOC);
    const first = state.tabs.at(-1);
    if (!first) throw new Error('no tab');

    // A pane is laid out at 132x38; the next dial starts there instead of making
    // the remote shell wrap its banner for a window that is not that size.
    ensureBridge(first).term.resize(132, 38);

    connect.mockResolvedValue('session-2');
    await connectAdhoc(ADHOC);
    expect(connect.mock.calls[1]?.[0]).toMatchObject({ cols: 132, rows: 38 });
    expect(connect.mock.calls[0]?.[0]).not.toMatchObject({ cols: 132, rows: 38 });
  });

  it('re-sends the size when you switch back to a tab', async () => {
    connect.mockResolvedValue('session-1');
    await connectAdhoc(ADHOC);
    const first = state.tabs.at(-1);
    if (!first) throw new Error('no tab');
    ensureBridge(first).term.resize(120, 40);
    const firstSession = first.sessionId;

    // A second tab takes the keyboard; coming back has to re-state the geometry.
    connect.mockResolvedValue('session-2');
    await connectAdhoc(ADHOC);
    resizePTY.mockClear();

    activateTab(first.id);
    expect(resizePTY).toHaveBeenCalledWith(firstSession, 120, 40);
  });

  it('sends nothing when you click inside the session you are already in', async () => {
    // `TerminalPane`'s pointerdown calls activateTab on every click in the
    // terminal, so an unconditional resize here sent WindowChange (SIGWINCH) to
    // the remote shell on each click — a full-screen program repaints the whole
    // screen for that, and it is not what clicking means.
    connect.mockResolvedValue('session-1');
    await connectAdhoc(ADHOC);
    const tab = state.tabs.at(-1);
    if (!tab) throw new Error('no tab');
    ensureBridge(tab);
    resizePTY.mockClear();

    activateTab(tab.id);
    expect(resizePTY).not.toHaveBeenCalled();
    expect(state.activeTabId).toBe(tab.id);
  });
});
