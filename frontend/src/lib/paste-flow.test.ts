/*
 * What a paste does, and what a selection does.
 *
 * A single line goes to the shell immediately — that is what "copy a command,
 * paste it, it runs" means. A block of lines is a script nobody has read, so it
 * goes through an editable dialog first. And a finished mouse selection lands on
 * the clipboard, while a selection nobody dragged does not.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Terminal } from '@xterm/xterm';

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

const writeClipboard = vi.fn<(text: string) => Promise<boolean>>(async () => true);

vi.mock('./clipboard', () => ({
  writeClipboard: (text: string) => writeClipboard(text),
  readClipboard: vi.fn(async () => ''),
}));

vi.mock('./backend', () => ({
  backend: {
    sendInputB64: vi.fn(async () => undefined),
    resizePTY: vi.fn(async () => undefined),
    fonts: vi.fn(async () => []),
  },
  fileBridgeApi: () => ({}),
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import {
  answerPrompt,
  ensureBridge,
  pasteIntoTerminal,
  prompt,
  resetSettings,
  setCopyOnSelect,
  state,
  terminalHasFocus,
  type Tab,
} from './store';

let counter = 0;

function makeTab(): Tab {
  counter += 1;
  return {
    id: `tab-paste-${counter}`,
    title: 'web-01',
    target: 'root@10.0.0.5:22',
    status: 'open',
    bytesIn: 0,
    bytesOut: 0,
    startedAt: Date.now(),
  };
}

function fakeSelection(tab: Tab) {
  const bridge = ensureBridge(tab);
  state.tabs.push(tab);
  vi.spyOn(bridge.term, 'hasSelection').mockReturnValue(true);
  vi.spyOn(bridge.term, 'getSelection').mockReturnValue('picked text');
  return bridge;
}

beforeEach(() => {
  resetSettings();
  localStorage.clear();
  writeClipboard.mockClear();
  state.notices.splice(0);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('multi-line paste review', () => {
  it('sends one line straight through, without a dialog', async () => {
    const tab = makeTab();
    const bridge = ensureBridge(tab);
    state.tabs.push(tab);
    const paste = vi.spyOn(bridge.term, 'paste').mockImplementation(() => undefined);

    // The leading blank line is a copy artefact and never reaches the shell.
    await pasteIntoTerminal(tab, '\nls -l\n');

    expect(prompt.open).toBe(false);
    expect(paste).toHaveBeenCalledWith('ls -l\n');
  });

  it('stops several lines and shows them, editable, with the leading blanks stripped', async () => {
    const tab = makeTab();
    const bridge = ensureBridge(tab);
    state.tabs.push(tab);
    const paste = vi.spyOn(bridge.term, 'paste').mockImplementation(() => undefined);

    const pending = pasteIntoTerminal(tab, '\r\nrm -rf /tmp/a\nls\r\n\r\n');
    // The prompt is raised synchronously: nothing is sent before it is answered.
    expect(paste).not.toHaveBeenCalled();
    expect(prompt.open).toBe(true);
    expect(prompt.multiline).toBe(true);
    expect(prompt.initial).toBe('rm -rf /tmp/a\nls');
    expect(prompt.label).toBe('2 lines');

    answerPrompt('rm -rf /tmp/keep\nls');
    await pending;
    // What the dialog holds is what gets sent, edits included, Enter and all.
    expect(paste).toHaveBeenCalledWith('rm -rf /tmp/keep\nls');
  });

  it('sends nothing when the review is cancelled', async () => {
    const tab = makeTab();
    const bridge = ensureBridge(tab);
    state.tabs.push(tab);
    const paste = vi.spyOn(bridge.term, 'paste').mockImplementation(() => undefined);

    const pending = pasteIntoTerminal(tab, 'a\nb\n');
    expect(prompt.open).toBe(true);
    answerPrompt(null);
    await pending;

    expect(paste).not.toHaveBeenCalled();
    expect(state.notices.some((notice) => notice.message.includes('cancelled'))).toBe(true);
  });

  it('sends nothing when the user clears the box and confirms', async () => {
    const tab = makeTab();
    const bridge = ensureBridge(tab);
    state.tabs.push(tab);
    const paste = vi.spyOn(bridge.term, 'paste').mockImplementation(() => undefined);

    const pending = pasteIntoTerminal(tab, 'a\nb\n');
    answerPrompt('   \n  ');
    await pending;

    expect(paste).not.toHaveBeenCalled();
  });
});

describe('native paste', () => {
  function pasteEvent(text: string): Event {
    const event = new Event('paste', { bubbles: true, cancelable: true });
    (event as unknown as { clipboardData: { getData: (kind: string) => string } }).clipboardData = {
      getData: () => text,
    };
    return event;
  }

  it('routes the terminal’s own paste through the same rules', async () => {
    const tab = makeTab();
    const bridge = ensureBridge(tab);
    state.tabs.push(tab);
    const paste = vi.spyOn(bridge.term, 'paste').mockImplementation(() => undefined);

    bridge.el.dispatchEvent(pasteEvent('ls -l\n'));
    await Promise.resolve();

    // One line: straight to the shell, and xterm's own handler never saw it.
    expect(paste).toHaveBeenCalledWith('ls -l\n');
    expect(prompt.open).toBe(false);

    bridge.el.dispatchEvent(pasteEvent('cd /tmp\nrm -rf build\n'));
    await Promise.resolve();
    expect(prompt.open).toBe(true);
    expect(prompt.initial).toBe('cd /tmp\nrm -rf build');
    answerPrompt(null);
  });
});

describe('keyboard ownership', () => {
  it('counts focus inside the pane as the shell having the keyboard', () => {
    const tab = makeTab();
    const bridge = ensureBridge(tab);
    state.tabs.push(tab);
    state.activeTabId = tab.id;

    expect(terminalHasFocus()).toBe(false);

    // What xterm does when you click into it: focus on a descendant. The pane
    // has to be in the document for focus to land at all — that is what
    // `attach()` does for a real tab.
    document.body.appendChild(bridge.el);
    const typing = document.createElement('textarea');
    bridge.el.appendChild(typing);
    typing.focus();
    expect(terminalHasFocus()).toBe(true);
    expect(terminalHasFocus(tab.id)).toBe(true);

    // Focus moving to the sidebar hands the keys back to the app.
    const elsewhere = document.createElement('input');
    document.body.appendChild(elsewhere);
    elsewhere.focus();
    expect(terminalHasFocus()).toBe(false);
  });

  it('is false when there is no terminal at all', () => {
    state.activeTabId = null;
    expect(terminalHasFocus()).toBe(false);
    expect(terminalHasFocus('tab-that-never-existed')).toBe(false);
  });

  it('tells xterm to leave the app’s own shortcuts alone', () => {
    const registered: ((event: KeyboardEvent) => boolean)[] = [];
    vi.spyOn(Terminal.prototype, 'attachCustomKeyEventHandler').mockImplementation((handler) => {
      registered.push(handler as (event: KeyboardEvent) => boolean);
    });

    ensureBridge(makeTab());
    expect(registered).toHaveLength(1);
    const handler = registered[0] as (event: KeyboardEvent) => boolean;
    const press = (init: Partial<KeyboardEvent>): KeyboardEvent =>
      ({ type: 'keydown', key: '', ctrlKey: false, metaKey: false, shiftKey: false, ...init }) as KeyboardEvent;

    // The shell's keys go through to xterm (true = xterm keeps the event).
    expect(handler(press({ key: 'w', ctrlKey: true }))).toBe(true);
    expect(handler(press({ key: 'b', ctrlKey: true }))).toBe(true);
    expect(handler(press({ key: 'k', ctrlKey: true }))).toBe(true);
    expect(handler(press({ key: 'c', ctrlKey: true }))).toBe(true);

    // The app's shifted shortcuts are swallowed, so they do not also reach the
    // session as a control character.
    expect(handler(press({ key: 'W', ctrlKey: true, shiftKey: true }))).toBe(false);
    expect(handler(press({ key: 'B', ctrlKey: true, shiftKey: true }))).toBe(false);
    expect(handler(press({ key: 'K', ctrlKey: true, shiftKey: true }))).toBe(false);
    expect(handler(press({ key: 'T', ctrlKey: true, shiftKey: true }))).toBe(false);
  });
});

describe('copy on select', () => {
  it('copies when the gesture that started in the pane ends', () => {
    const tab = makeTab();
    const bridge = fakeSelection(tab);
    expect(state.settings.copyOnSelect).toBe(true);

    bridge.el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    window.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));

    expect(writeClipboard).toHaveBeenCalledWith('picked text');
  });

  it('ignores a release with no press in the pane, so repaints cannot steal the clipboard', () => {
    const tab = makeTab();
    fakeSelection(tab);

    window.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));

    expect(writeClipboard).not.toHaveBeenCalled();
  });

  it('does nothing once the setting is off', () => {
    const tab = makeTab();
    const bridge = fakeSelection(tab);
    setCopyOnSelect(false);

    bridge.el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    window.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));

    expect(writeClipboard).not.toHaveBeenCalled();
    expect(state.settings.copyOnSelect).toBe(false);
  });

  it('leaves an empty selection alone', () => {
    const tab = makeTab();
    const bridge = ensureBridge(tab);
    state.tabs.push(tab);
    vi.spyOn(bridge.term, 'hasSelection').mockReturnValue(false);

    bridge.el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    window.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));

    expect(writeClipboard).not.toHaveBeenCalled();
  });

  it('carries the choice to tabs opened afterwards', () => {
    setCopyOnSelect(false);
    const tab = makeTab();
    const bridge = fakeSelection(tab);

    bridge.el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    window.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));

    expect(writeClipboard).not.toHaveBeenCalled();
  });
});
