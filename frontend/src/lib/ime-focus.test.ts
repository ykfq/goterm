/*
 * Where the keyboard is when the user comes back.
 *
 * The bug this file is about: in a TUI, switching to another tab and back left the
 * IME showing a cross �?Windows' 鈥滃凡绂佺敤杈撳叆娉曗€?�?until you clicked into the terminal
 * again. The cause is not the IME: `v-show` hides the pane the keyboard is in, and
 * a textarea inside a `display: none` subtree cannot be focused, so the keyboard
 * stays pointing into a pane nobody is rendering. Nothing to attach an input method
 * to.
 *
 * Two behaviours fix that: focus is only given to a terminal once its pane is on
 * screen, and coming back to the window picks the keyboard up again if it was left
 * somewhere dead.
 *
 * Three more were added for the TUI case, where the cross kept coming back: a
 * context menu or dialog that closes takes its button with it and drops the
 * keyboard on `<body>` (nothing is left for an IME to attach to), the pane itself
 * used to focus before `v-show` had shown it, and a window that was in the
 * background can come back with the webview's input context gone even though the
 * textarea never lost DOM focus.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';

const transferState = vi.hoisted(() => ({ active: false }));

// happy-dom has no WebGL2, and the addon complains asynchronously �?after the
// bridge's own try/catch �?which vitest reports as an unhandled error. The renderer
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
      return transferState.active;
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

import { activateTab, ensureBridge, reclaimFocus, reclaimFocusSoon, resetSettings, state } from './store';
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
  // What TerminalPane.vue does on mount �?including xterm's own focus frame.
  bridge.attach(wrapper);
  return { wrapper, el: bridge.el };
}

/** Let the frame the bridge schedules on attach run before a spy is installed. */
async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 30));
}

/**
 * A fresh tab id per test.
 *
 * Bridges are keyed by tab id and live for the whole file, so reusing `a` would
 * hand one test the bridge another test had already spied on (and left a frame or
 * a watchdog timer on).
 */
function freshTab(): Tab {
  freshTab.serial += 1;
  return tab(`fresh-${freshTab.serial}`, 'web-01');
}
freshTab.serial = 0;

beforeEach(() => {
  resetSettings();
  transferState.active = false;
  document.body.innerHTML = '';
  state.tabs.splice(0, state.tabs.length);
});

describe('focus after a tab switch', () => {
  // `activateTab` used to focus synchronously, while `v-show` had not swapped the
  // panes yet �?so the focus call landed on a hidden element and did nothing, and
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

describe('the keyboard follows the active tab, whoever moved it', () => {
  it('focuses a tab that was activated without activateTab', async () => {
    // Connecting a host, duplicating a tab and reconnecting all assign
    // `state.activeTabId` directly. They used to rely on the pane noticing, and the
    // pane noticed too early �?see the synchronous-focus test above.
    const first = tab('a', 'web-01');
    const second = tab('b', 'web-02');
    state.tabs.push(first, second);
    state.activeTabId = first.id;

    const a = pane(first);
    const b = pane(second);
    b.wrapper.style.display = 'none';
    await settle();

    const focusB = vi.spyOn(ensureBridge(second), 'focus');
    state.activeTabId = second.id;
    a.wrapper.style.display = 'none';
    b.wrapper.style.display = '';
    await nextTick();

    expect(focusB).toHaveBeenCalled();
  });
});

describe('when the keyboard falls nowhere', () => {
  /** One shown pane whose terminal has the keyboard, ready for a blur. */
  async function focusedPane() {
    const only = freshTab();
    state.tabs.push(only);
    state.activeTabId = only.id;
    const { wrapper } = pane(only);
    await settle();
    const bridge = ensureBridge(only);
    bridge.focus();
    const textarea = bridge.term.textarea;
    expect(textarea).toBeTruthy();
    expect(document.activeElement).toBe(textarea);
    return { bridge, wrapper, textarea: textarea as HTMLTextAreaElement };
  }

  it('takes it back for the terminal that lost it to nothing', async () => {
    const { textarea } = await focusedPane();

    // What closing a context menu or a dialog leaves behind: the button that had
    // the keyboard went away with it and focus landed on <body>. Nothing editable
    // is focused, so Windows paints the IME as disabled and no keystroke can switch
    // to Chinese.
    textarea.blur();
    expect(document.activeElement).toBe(document.body);

    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(document.activeElement).toBe(textarea);
  });

  it('leaves the keyboard with the field that took it', async () => {
    const { textarea } = await focusedPane();

    const field = document.createElement('input');
    document.body.appendChild(field);
    field.focus();

    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(document.activeElement).toBe(field);
    expect(document.activeElement).not.toBe(textarea);
  });

  it('does not pull the keyboard into a pane nobody renders', async () => {
    const { wrapper, textarea } = await focusedPane();

    // `v-show` hides the old pane on the same flush the new one appears. Taking the
    // keyboard back into it is the no-op that started all of this.
    wrapper.style.display = 'none';
    textarea.blur();

    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(document.activeElement).not.toBe(textarea);
  });

  it('gives it back after a menu that was open long enough to look fine', async () => {
    const { textarea } = await focusedPane();

    // A context menu: the entry takes the keyboard off the terminal...
    const item = document.createElement('button');
    document.body.appendChild(item);
    item.focus();
    // ...and stays open long enough for the terminal's own blur check to have run
    // and concluded the keyboard was in good hands.
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(document.activeElement).toBe(item);

    // Then it is activated, and the menu �?with the entry �?goes away.
    item.remove();
    reclaimFocusSoon();

    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(document.activeElement).toBe(textarea);
  });
});

describe('coming back to the window', () => {
  async function focusedBridge() {
    const only = freshTab();
    state.tabs.push(only);
    state.activeTabId = only.id;
    pane(only);
    await settle();
    const bridge = ensureBridge(only);
    bridge.focus();
    return bridge;
  }

  it('rebuilds the input method when the terminal never lost DOM focus', async () => {
    // While the window is in the background the webview can lose its input context
    // without the textarea ever reporting a blur, so nothing looks wrong to the app
    // and only a focus handoff puts the IME back.
    const bridge = await focusedBridge();
    const reattach = vi.spyOn(bridge, 'reattachKeyboard');

    reclaimFocus({ reattach: true });

    expect(reattach).toHaveBeenCalledTimes(1);
  });

  it('does not disturb a terminal that was never left', async () => {
    const bridge = await focusedBridge();
    const reattach = vi.spyOn(bridge, 'reattachKeyboard');
    const focus = vi.spyOn(bridge, 'focus');

    reclaimFocus();

    expect(reattach).not.toHaveBeenCalled();
    expect(focus).not.toHaveBeenCalled();
  });

  it('never blurs a composition, and never writes while a file is moving', async () => {
    const bridge = await focusedBridge();
    const textarea = bridge.term.textarea as HTMLTextAreaElement;
    const blur = vi.spyOn(bridge.term, 'blur');
    const focus = vi.spyOn(bridge.term, 'focus');

    // A handoff mid-composition throws the pinyin away and leaves the IME in
    // whatever mode the interruption left.
    textarea.dispatchEvent(new Event('compositionstart'));
    bridge.reattachKeyboard();
    expect(blur).not.toHaveBeenCalled();

    // Focus-out/in are escape sequences written to the PTY, and red line 4 says
    // nothing enters the stream while a transfer is running.
    textarea.dispatchEvent(new Event('compositionend'));
    transferState.active = true;
    bridge.reattachKeyboard();
    expect(blur).not.toHaveBeenCalled();
    expect(focus).toHaveBeenCalled();
  });
});

describe('a tab switch whose focus did not take', () => {
  /**
   * The click that moved the tab can end with the keyboard on <body>: the pane
   * is on screen and the terminal asked for it, but the focus call landed while
   * the browser was still finishing the click. One frame later the answer is
   * final, and the terminal asks once more �� which is the difference between
   * "click the tab and type" and "click the tab, then click the terminal".
   */
  it('asks once more on the next frame when the first focus did not take', async () => {
    const first = freshTab();
    const second = freshTab();
    state.tabs.push(first, second);
    state.activeTabId = first.id;

    const a = pane(first);
    const b = pane(second);
    b.wrapper.style.display = 'none';
    await settle();

    const bridgeB = ensureBridge(second);
    // The first attempt is refused, the way a focus call during the click's own
    // default handling can be.
    const focusB = vi.spyOn(bridgeB, 'focus').mockImplementationOnce(() => undefined);
    // `attach` focused this terminal when its pane mounted; a tab that is not
    // showing must not be holding the keyboard while we test the switch.
    (document.activeElement as HTMLElement | null)?.blur?.();
    expect(document.activeElement).toBe(document.body);

    activateTab(second.id);
    a.wrapper.style.display = 'none';
    b.wrapper.style.display = '';
    await nextTick();

    expect(focusB).toHaveBeenCalledTimes(1);
    expect(b.el.contains(document.activeElement as Node)).toBe(false);

    await new Promise((resolve) => setTimeout(resolve, 60));

    expect(focusB).toHaveBeenCalledTimes(2);
    expect(b.el.contains(document.activeElement as Node)).toBe(true);
  });

  it('leaves a field that took the keyboard alone', async () => {
    const first = freshTab();
    const second = freshTab();
    state.tabs.push(first, second);
    state.activeTabId = first.id;

    const a = pane(first);
    const b = pane(second);
    b.wrapper.style.display = 'none';
    await settle();

    const focusB = vi.spyOn(ensureBridge(second), 'focus').mockImplementationOnce(() => undefined);
    (document.activeElement as HTMLElement | null)?.blur?.();

    activateTab(second.id);
    a.wrapper.style.display = 'none';
    b.wrapper.style.display = '';
    await nextTick();

    // A prompt opened by the switch is a real user of the keyboard; taking it
    // back would be the app deciding it knows better.
    const field = document.createElement('input');
    document.body.appendChild(field);
    field.focus();

    await new Promise((resolve) => setTimeout(resolve, 60));

    expect(focusB).toHaveBeenCalledTimes(1);
    expect(document.activeElement).toBe(field);
    expect(b.el.contains(field)).toBe(false);
  });
});

describe('a pane that becomes visible after the focus callback', () => {
  /**
   * Measured in Chromium: on a tab click, the post-flush callback runs while the
   * wrapper being switched to still has `display: none` (the swap lands on a
   * later flush). Focusing there was a no-op �� the `focus()` call itself never
   * happened on the real pane �� so the keyboard stayed on <body> and typing went
   * nowhere until the terminal was clicked again.
   */
  it('takes the keyboard once the pane is actually on screen', async () => {
    const first = freshTab();
    const second = freshTab();
    state.tabs.push(first, second);
    state.activeTabId = first.id;

    const a = pane(first);
    const b = pane(second);
    b.wrapper.style.display = 'none';
    await settle();

    const bridgeB = ensureBridge(second);
    const focusB = vi.spyOn(bridgeB, 'focus');

    // Collect the frame the store schedules instead of racing it: what matters
    // is that it asks again once the pane has appeared, not how the test
    // environment times frames.
    const frames: FrameRequestCallback[] = [];
    const raf = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      frames.push(callback);
      return frames.length;
    });
    try {
      activateTab(second.id);
      // The callback runs now, with the pane still hidden.
      await nextTick();
      expect(focusB).not.toHaveBeenCalled();
      expect(frames).toHaveLength(1);

      // The pane swap lands after it, which is what happens in the real app —
      // and Chromium drops the keyboard to <body> when the focused pane is
      // hidden (happy-dom leaves it on the hidden textarea, so say it here).
      a.wrapper.style.display = 'none';
      b.wrapper.style.display = '';
      (document.activeElement as HTMLElement | null)?.blur?.();
      frames[0]?.(0);

      expect(focusB).toHaveBeenCalledTimes(1);
      expect(b.el.contains(document.activeElement as Node)).toBe(true);
    } finally {
      raf.mockRestore();
    }
  });
});