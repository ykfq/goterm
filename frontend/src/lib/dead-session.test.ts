/*
 * Input for a tab whose session has ended.
 *
 * A closed tab stays mounted — it shows the "connection closed" strip over the
 * terminal — so it still receives every signal a live one does: the focus-in
 * sequence xterm emits when you switch back to it, mouse reports if the program
 * had asked for the mouse, and the anti-idle's nudge. Each of those used to go
 * through `onSend`, fail with "session is closed", and raise its own toast, so
 * coming back to a dead tab painted the corner with copies of the same sentence.
 *
 * The rule is now one function (`acceptsInput`) and one exit (`markSessionGone`),
 * both exported: reaching `onSend` itself needs an opened terminal with a real
 * input handler, which happy-dom cannot provide, so the rule and the exit are
 * tested where they live instead of through a simulated keystroke that would
 * never arrive.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

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

vi.mock('./backend', () => ({
  backend: {
    sendInputB64: vi.fn(async () => undefined),
    resizePTY: vi.fn(async () => undefined),
    setSessionLifetime: vi.fn(async (hours: number) => hours),
    fonts: vi.fn(async () => []),
  },
  fileBridgeApi: () => ({}),
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import { acceptsInput, ensureBridge, markSessionGone, pushNotice, resetSettings, state, type Tab } from './store';

let counter = 0;

function makeTab(status: Tab['status'], sessionId = 's_live'): Tab {
  counter += 1;
  return {
    id: `tab-dead-${counter}`,
    title: 'web-01',
    target: 'root@10.0.0.5:22',
    status,
    sessionId,
    bytesIn: 0,
    bytesOut: 0,
    startedAt: Date.now(),
  };
}

beforeEach(() => {
  resetSettings();
  localStorage.clear();
  state.notices.splice(0);
  state.tabs.splice(0);
});

describe('acceptsInput', () => {
  it('lets exactly one shape through: a live session on an open tab', () => {
    expect(acceptsInput(makeTab('open'))).toBe(true);
    // Everything else is somewhere to type into nothing.
    expect(acceptsInput(makeTab('closed'))).toBe(false);
    expect(acceptsInput(makeTab('error'))).toBe(false);
    expect(acceptsInput(makeTab('connecting'))).toBe(false);
    expect(acceptsInput({ status: 'open', sessionId: undefined })).toBe(false);
  });
});

describe('a session that Go reports as gone', () => {
  it('is marked once, said in the pane, and never toasted', () => {
    const tab = makeTab('open');
    const bridge = ensureBridge(tab);
    state.tabs.push(tab);
    const written: (string | Uint8Array)[] = [];
    bridge.term.write = ((input: string | Uint8Array) => {
      written.push(input);
    }) as typeof bridge.term.write;

    markSessionGone(tab, 'session is closed');

    expect(tab.status).toBe('closed');
    expect(tab.detail).toBe('session is closed');
    // Where the session was, not in the corner: the tab strip and the pane
    // already carry the reason, and a toast would be a third copy of it.
    expect(written.join('')).toContain('[goterm] session is closed');
    expect(state.notices).toHaveLength(0);
    // And the guard now drops every later focus-in sequence / nudge silently.
    expect(acceptsInput(tab)).toBe(false);
  });
});

describe('notice de-duplication', () => {
  it('collapses an echo but not a different fact', () => {
    pushNotice('warn', 'transport broke');
    pushNotice('warn', 'transport broke');
    expect(state.notices).toHaveLength(1);

    // A different level is a different thing to say, even with the same words.
    pushNotice('error', 'transport broke');
    expect(state.notices).toHaveLength(2);

    pushNotice('warn', 'another problem');
    expect(state.notices).toHaveLength(3);
  });
});
