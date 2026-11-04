/*
 * Copying out of a full-screen program.
 *
 * Two separate wires have to be right, and both failed in the same session:
 *
 *  - a TUI's own right-click menu prints `ESC ] 52 ; c ; <base64> BEL` and
 *    expects the terminal to copy it (it has no clipboard, and no `xclip`, on
 *    the host it runs on). xterm drops OSC 52 as unknown, so the item did
 *    nothing at all;
 *  - select-to-copy short-circuits on the press. With a program owning the
 *    mouse, xterm stops the propagation of the very press that starts a
 *    Shift-selection, so the release copied nothing — the toast even promised
 *    "Shift+drag selects text (and still copies it)".
 *
 * Both are checked through a real bridge, because both live in its wiring: a
 * unit test on the parser cannot see a handler that was never registered, and a
 * component test mocks the bridge away.
 */
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

// What the copy actually lands in.
const clipboard = vi.hoisted(() => ({
  writeClipboard: vi.fn(async () => true),
  readClipboard: vi.fn(async () => ''),
}));
vi.mock('./clipboard', () => clipboard);

// trzsz refuses to construct under Node; here the question is only what reaches
// xterm, so the filter is reduced to "pass server output through".
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

import { createTerminalBridge } from './terminal';

function base64(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

const bridge = createTerminalBridge({
  id: 'clipboard-pipe',
  onSend: vi.fn(),
  onResize: vi.fn(),
  copyOnSelect: true,
});

/** Server output in through the pipe, then xterm's parser caught up. */
function feed(text: string): Promise<void> {
  return new Promise((resolve) => {
    bridge.write(new TextEncoder().encode(text));
    bridge.term.write('', () => resolve());
  });
}

beforeAll(() => {
  // The copy-on-select listeners hang off the pane element; it only needs a
  // parent for events to travel through.
  document.body.appendChild(bridge.el);
});

beforeEach(() => {
  clipboard.writeClipboard.mockClear();
});

afterAll(() => {
  bridge.dispose();
});

describe('a program copying through the terminal', () => {
  it('puts what a TUI asked to copy on the clipboard', async () => {
    await feed(`\x1b]52;c;${base64('deploy finished on prod-2')}\x07`);
    expect(clipboard.writeClipboard).toHaveBeenCalledWith('deploy finished on prod-2');
  });

  it('accepts the ST terminator as well as BEL', async () => {
    await feed(`\x1b]52;c;${base64('job 431\nfailed')}\x1b\\`);
    expect(clipboard.writeClipboard).toHaveBeenCalledWith('job 431\nfailed');
  });

  it('copies UTF-8 out of a Chinese TUI', async () => {
    await feed(`\x1b]52;c;${base64('部署完成')}\x07`);
    expect(clipboard.writeClipboard).toHaveBeenCalledWith('部署完成');
  });

  it('does not hand the clipboard to a read query', async () => {
    // `c;?` asks the terminal to print this machine's clipboard back to the
    // host. Silence is the whole answer.
    await feed('\x1b]52;c;?\x07');
    expect(clipboard.writeClipboard).not.toHaveBeenCalled();
  });
});

describe('select-to-copy while a program owns the mouse', () => {
  const term = bridge.term as unknown as { hasSelection: () => boolean; getSelection: () => string };

  beforeEach(() => {
    term.hasSelection = () => true;
    term.getSelection = () => 'line from the TUI';
  });

  it('copies on release even though xterm hides the press', async () => {
    // What xterm's SelectionService does when a program has the mouse and Shift
    // forces a local selection: the press must not reach the program, so it is
    // stopped where it was delivered. A bubbling listener on the pane never saw
    // this, and the release then had nothing to copy.
    const inner = document.createElement('div');
    bridge.el.appendChild(inner);
    inner.addEventListener('mousedown', (event) => event.stopPropagation());

    inner.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0, shiftKey: true }));
    window.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));

    await vi.waitFor(() => expect(clipboard.writeClipboard).toHaveBeenCalledWith('line from the TUI'));
    inner.remove();
  });

  it('still copies a selection dragged in an ordinary shell', async () => {
    bridge.el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 }));
    window.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));

    await vi.waitFor(() => expect(clipboard.writeClipboard).toHaveBeenCalledWith('line from the TUI'));
  });

  it('does not copy a selection nobody dragged', async () => {
    // A repaint can leave a selection behind; the gesture is what says "copy
    // this", so a release with no press in the pane must stay quiet.
    window.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }));
    await Promise.resolve();
    expect(clipboard.writeClipboard).not.toHaveBeenCalled();
  });
});
