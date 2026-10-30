/*
 * Where the keyboard is when the user comes back.
 *
 * The bug this file is about: in a TUI, switching to another tab and back left the
 * IME showing a cross — Windows' “已禁用输入法” — until you clicked into the terminal
 * again. The cause is not the IME: `v-show` hides the pane the keyboard is in, and
 * a textarea inside a `display: none` subtree cannot be focused, so the keyboard
 * stays pointing into a pane nobody is rendering. Nothing to attach an input method
 * to.
 *
 * Two behaviours fix that: focus is only given to a terminal once its pane is on
 * screen, and coming back to the window picks the keyboard up again if it was left
 * somewhere dead.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';

// happy-dom has no WebGL2, and the addon complains asynchronously — after the
// bridge's own try/catch — which vitest reports as an unhandled error. The renderer
// is not what this file is about.
vi.mock('@xterm/addon-webgl', () => ({
  WebglAddon: class {
    constructor(..._args: unknown[]) {}
    onContextLoss(_handler: () => void): void {
      /* nothing */
    }
    dispose(): void {
      /* nothing */
    }
  },
}));

vi.mock('trzsz', () => ({
  TrzszFilter: class {
    private readonly options: { writeToTerminal: (output: unknown) => void };
    constructor(options: { writeToTerminal: (output: unknown) => void }) {
      this.options = options;
    }
    processServerOutput(bytes: Uint8Array): void {
      this.options.writeToTerminal(bytes);
    }
    processTerminalInput(): void {
      /* not what this file is about */
    }
    processBinaryInput(): void {
      /* nothing */
    }
    isTransferringFiles(): boolean {
      return false;
    }
    setTerminalColumns(): void {
      /* nothing */
    }
    stopTransferringFiles(): void {
      /* nothing */
    }
  },
}));

vi.mock('./clipboard', () => ({
  writeClipboard: vi.fn(async () => true),
  readClipboard: vi.fn(async () => ''),
}));

vi.mock('./backend', () => ({
  backend: {
    sendInputB64: vi.fn(async () => undefined),
    resizePTY: vi.fn(async () => undefined),
  },
  fileBridgeApi: () => ({}),
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import { activateTab, ensureBridge, reclaimFocus, resetSettings, state } from './store';
import type { Tab } from './store';

function tab(id: string, title: string): Tab {
  return {
    id,
    title,
    target: `root@${title}:22`,
    hostId: `h_${id}`,
    sessionId: `sess-${id}`,
    status: 'open',
    bytesIn: 0,
    bytesOut: 0,
    startedAt: Date.now(),
  };
}

/** A pane the way App.vue renders one: a wrapper that `v-show` can hide. */
function pane(entry: Tab): { wrapper: HTMLDivElement; el: HTMLDivElement } {
  const wrapper = document.createElement('div');
  document.body.appendChild(wrapper);
  const bridge = ensureBridge(entry);
  // What TerminalPane.vue does on mount — including xterm's own focus frame.
  bridge.attach(wrapper);
  return { wrapper, el: bridge.el };
}

/** Let the frame the bridge schedules on attach run before a spy is installed. */
async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 30));
}

beforeEach(() => {
  resetSettings();
  document.body.innerHTML = '';
  state.tabs.splice(0, state.tabs.length);
});

describe('focus after a tab switch', () => {
  // `activateTab` used to focus synchronously, while `v-show` had not swapped the
  // panes yet — so the focus call landed on a hidden element and did nothing, and
  // the keyboard stayed in the pane that was about to be hidden.
  it('waits for the pane to be on screen before giving it the keyboard', async () => {
    const first = tab('a', 'web-01');
    const second = tab('b', 'web-02');
    state.tabs.push(first, second);
    state.activeTabId = first.id;

    const a = pane(first);
    const b = pane(second);
    b.wrapper.style.display = 'none';
    await settle();

    const focusB = vi.spyOn(ensureBridge(second), 'focus');

    activateTab(second.id);
    // Still hidden on this tick: focusing now would be the no-op that caused the bug.
    expect(focusB).not.toHaveBeenCalled();

    // What `v-show` does on the next flush.
    a.wrapper.style.display = 'none';
    b.wrapper.style.display = '';
    await nextTick();

    expect(focusB).toHaveBeenCalled();
  });

  it('focuses at once for a click inside the terminal that is already showing', async () => {
    const only = tab('a', 'web-01');
    state.tabs.push(only);
    state.activeTabId = only.id;
    pane(only);
    await settle();

    const focus = vi.spyOn(ensureBridge(only), 'focus');
    activateTab(only.id); // not a switch: this is the pointerdown path
    expect(focus).toHaveBeenCalled();
  });
});

describe('reclaimFocus', () => {
  it('takes the keyboard back from a pane that is no longer rendered', async () => {
    const first = tab('a', 'web-01');
    const second = tab('b', 'web-02');
    state.tabs.push(first, second);
    state.activeTabId = first.id;

    const a = pane(first);
    const b = pane(second);
    b.wrapper.style.display = 'none';
    await settle();

    // The state the bug leaves behind: the keyboard is inside the hidden pane.
    ensureBridge(second).focus();
    const hidden = document.activeElement;
    expect(hidden).not.toBeNull();
    expect(b.el.contains(hidden as Node)).toBe(true);

    const focusA = vi.spyOn(ensureBridge(first), 'focus');
    reclaimFocus();

    expect(focusA).toHaveBeenCalled();
    expect(a.el.contains(document.activeElement as Node)).toBe(true);
  });

  it('leaves a dialog field alone', async () => {
    const only = tab('a', 'web-01');
    state.tabs.push(only);
    state.activeTabId = only.id;
    pane(only);
    await settle();

    const field = document.createElement('input');
    document.body.appendChild(field);
    field.focus();

    const focus = vi.spyOn(ensureBridge(only), 'focus');
    reclaimFocus();

    expect(document.activeElement).toBe(field);
    expect(focus).not.toHaveBeenCalled();
  });

  it('does nothing when there is no terminal to give the keyboard to', () => {
    expect(() => reclaimFocus()).not.toThrow();
  });
});
