/*
 * The anti-idle: the quietest byte that keeps a `TMOUT` shell alive.
 *
 * SSH keepalives never reach the shell, so they cannot reset bash's `TMOUT`
 * timer — the session still dies with "timed out waiting for input: auto logout".
 * Only something that arrives as input does. The default nudge is a space
 * followed by a backspace: it counts as input, cancels itself out, and cannot
 * submit or run anything — which is what makes it safe to send while the user
 * has half a command on the line, including at a `>` continuation prompt.
 *
 * A completed empty line remains the louder option for a shell that only resets
 * on a finished line; the tests below cover both, and the difference between
 * them is exactly the `pendingInput` guard.
 */
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('trzsz', () => ({
  TrzszFilter: class {
    private readonly options: { writeToTerminal: (output: unknown) => void };
    transferring = false;
    constructor(options: { writeToTerminal: (output: unknown) => void }) {
      this.options = options;
    }
    processServerOutput(bytes: Uint8Array): void {
      this.options.writeToTerminal(bytes);
    }
    processTerminalInput(): void {
      /* the bridge's own onSend is what this file observes */
    }
    processBinaryInput(): void {}
    isTransferringFiles(): boolean {
      return this.transferring;
    }
    setTerminalColumns(): void {}
    stopTransferringFiles(): void {}
  },
}));

import { createTerminalBridge, nudgeBytes, type IdleNudge } from './terminal';

/** What each nudge costs on the wire. */
const SPACE_BACKSPACE = [0x20, 0x7f];
const ENTER = [0x0d];
const BARE_SPACE = [0x20];

function makeBridge(idleKeepAliveSeconds: number, transferring = false, idleNudge?: IdleNudge) {
  const onSend = vi.fn();
  const onNotice = vi.fn();
  const bridge = createTerminalBridge({
    id: `idle-${Math.random().toString(36).slice(2)}`,
    onSend,
    onResize: vi.fn(),
    onNotice,
    idleKeepAliveSeconds,
    ...(idleNudge ? { idleNudge } : {}),
  });
  const trzsz = bridge.trzsz as unknown as { transferring: boolean };
  trzsz.transferring = transferring;
  return { bridge, onSend, onNotice };
}

function sentBytes(onSend: ReturnType<typeof vi.fn>): number[][] {
  return onSend.mock.calls.map(([bytes]) => Array.from(bytes as Uint8Array));
}

function countOf(onSend: ReturnType<typeof vi.fn>, nudge: IdleNudge): number {
  const want = nudge === 'line' ? ENTER : nudge === 'space' ? SPACE_BACKSPACE : BARE_SPACE;
  return sentBytes(onSend).filter((sent) => sent.length === want.length && sent.every((b, i) => b === want[i])).length;
}

/** Nothing but nudges ever leaves on a fresh bridge, so all counters add up. */
function totalNudges(onSend: ReturnType<typeof vi.fn>): number {
  return countOf(onSend, 'space') + countOf(onSend, 'line') + countOf(onSend, 'space-only');
}

const out = (text: string) => new TextEncoder().encode(text);

afterEach(() => {
  vi.useRealTimers();
});

describe('anti-idle: the space nudge (the default)', () => {
  it('sends a space and a backspace, never an Enter', () => {
    vi.useFakeTimers();
    const { bridge, onSend, onNotice } = makeBridge(60);

    vi.advanceTimersByTime(59_000);
    expect(totalNudges(onSend)).toBe(0);

    vi.advanceTimersByTime(2_000);
    expect(countOf(onSend, 'space')).toBe(1);
    expect(countOf(onSend, 'line')).toBe(0);
    // It says so once, not every time.
    expect(onNotice).toHaveBeenCalledTimes(1);
    expect(String(onNotice.mock.calls[0]?.[1] ?? '')).toMatch(/space and a backspace/);

    vi.advanceTimersByTime(120_000);
    expect(countOf(onSend, 'space')).toBe(3);
    expect(countOf(onSend, 'line')).toBe(0);
    expect(onNotice).toHaveBeenCalledTimes(1);

    bridge.dispose();
  });

  it('sends it anyway while the line holds a command that was not submitted', () => {
    // The case that motivated the change: a `for` loop left at the `>` prompt.
    // A completed line would run the half-written loop; a rubbed-out space does
    // nothing, and an Enter-only nudge would simply never fire and let TMOUT win.
    vi.useFakeTimers();
    const { bridge, onSend } = makeBridge(60);

    bridge.term.input('for i in 1 2 3; do');
    vi.advanceTimersByTime(300_000);
    expect(countOf(onSend, 'space')).toBeGreaterThan(2);
    expect(countOf(onSend, 'line')).toBe(0);

    bridge.dispose();
  });

  it('stays out of full-screen programs', () => {
    vi.useFakeTimers();
    const { bridge, onSend } = makeBridge(60);

    bridge.write(out('\x1b[?1049h'));
    vi.advanceTimersByTime(300_000);
    expect(totalNudges(onSend)).toBe(0);

    // Back to the shell: the timer works again.
    bridge.write(out('\x1b[?1049l'));
    vi.advanceTimersByTime(61_000);
    expect(totalNudges(onSend)).toBe(1);

    bridge.dispose();
  });

  it('stays out of a running transfer', () => {
    vi.useFakeTimers();
    const { bridge, onSend } = makeBridge(60, true);
    vi.advanceTimersByTime(300_000);
    expect(totalNudges(onSend)).toBe(0);
    bridge.dispose();
  });

  it('is off when the setting is 0', () => {
    vi.useFakeTimers();
    const { bridge, onSend } = makeBridge(0);
    vi.advanceTimersByTime(600_000);
    expect(onSend).not.toHaveBeenCalled();
    bridge.dispose();
  });

  it('can be switched on and off at runtime', () => {
    vi.useFakeTimers();
    const { bridge, onSend } = makeBridge(0);
    vi.advanceTimersByTime(120_000);
    expect(totalNudges(onSend)).toBe(0);

    bridge.setIdleKeepAlive(60);
    vi.advanceTimersByTime(61_000);
    expect(totalNudges(onSend)).toBe(1);

    bridge.setIdleKeepAlive(0);
    vi.advanceTimersByTime(600_000);
    expect(totalNudges(onSend)).toBe(1);

    bridge.dispose();
  });
});

describe('anti-idle: the empty-line nudge', () => {
  it('sends Enter and nothing else', () => {
    vi.useFakeTimers();
    const { bridge, onSend, onNotice } = makeBridge(60, false, 'line');

    vi.advanceTimersByTime(61_000);
    expect(countOf(onSend, 'line')).toBe(1);
    expect(countOf(onSend, 'space')).toBe(0);
    expect(String(onNotice.mock.calls[0]?.[1] ?? '')).toMatch(/empty line/);

    bridge.dispose();
  });

  it('never fires while there is text that has not been submitted', () => {
    vi.useFakeTimers();
    const { bridge, onSend } = makeBridge(60, false, 'line');

    bridge.term.input('rm -rf /tmp/important');
    vi.advanceTimersByTime(300_000);
    expect(totalNudges(onSend)).toBe(0);

    // Pressing Enter (from the user) clears the line — and re-arms the timer.
    bridge.term.input('\r');
    vi.advanceTimersByTime(61_000);
    expect(countOf(onSend, 'line')).toBe(1);

    bridge.dispose();
  });

  it('treats Ctrl-C and Ctrl-D as clearing the line', () => {
    vi.useFakeTimers();
    for (const control of ['\x03', '\x04']) {
      const { bridge, onSend } = makeBridge(60, false, 'line');
      bridge.term.input('half typed');
      bridge.term.input(control);
      vi.advanceTimersByTime(61_000);
      expect(countOf(onSend, 'line')).toBe(1);
      bridge.dispose();
    }
  });

  it('follows setIdleNudge both ways', () => {
    vi.useFakeTimers();
    const { bridge, onSend } = makeBridge(60);

    bridge.setIdleNudge('line');
    vi.advanceTimersByTime(61_000);
    expect(countOf(onSend, 'line')).toBe(1);

    bridge.term.input('git commit -m "draft"');
    vi.advanceTimersByTime(120_000);
    expect(countOf(onSend, 'line')).toBe(1);

    bridge.setIdleNudge('space');
    vi.advanceTimersByTime(61_000);
    expect(countOf(onSend, 'space')).toBe(1);

    bridge.dispose();
  });
});

describe('when a nudge is allowed to land', () => {
  it('waits for output to stop, then does not wait for the whole window', () => {
    // The old rule was "no output at all for the whole idle window", which meant
    // a session that was printing never got a nudge and still got logged out.
    // What is left is a short grace so a keystroke never lands mid-burst.
    vi.useFakeTimers();
    const { bridge, onSend } = makeBridge(60);

    for (let i = 0; i < 20; i += 1) {
      vi.advanceTimersByTime(3_000);
      bridge.write(out('log line\n'));
    }
    expect(totalNudges(onSend)).toBe(0);

    // The stream stops: one idle window later the session is nudged.
    vi.advanceTimersByTime(60_000);
    expect(totalNudges(onSend)).toBe(1);

    bridge.dispose();
  });

  it('gives up for the session when one nudge reprints a screenful', () => {
    vi.useFakeTimers();
    const { bridge, onSend, onNotice } = makeBridge(60);

    vi.advanceTimersByTime(61_000);
    expect(totalNudges(onSend)).toBe(1);

    // The echo: a bastion's asset menu, not a prompt.
    const menu = Array.from({ length: 10 }, (_, i) => `  12) host-${i}\r\n`).join('');
    bridge.write(out(menu));
    expect(String(onNotice.mock.calls.at(-1)?.[1] ?? '')).toMatch(/stopped for this session/i);

    vi.advanceTimersByTime(600_000);
    expect(totalNudges(onSend)).toBe(1);
    bridge.dispose();
  });

  it('starts nudging again once a real command has been run', () => {
    vi.useFakeTimers();
    const { bridge, onSend } = makeBridge(60);

    vi.advanceTimersByTime(61_000);
    bridge.write(out(Array.from({ length: 10 }, (_, i) => `menu ${i}\r\n`).join('')));
    expect(totalNudges(onSend)).toBe(1);

    // A person sits down at a real prompt: the give-up was about a menu, not
    // about this session.
    bridge.term.input('ls\r');
    vi.advanceTimersByTime(70_000);
    expect(totalNudges(onSend)).toBe(2);

    bridge.dispose();
  });

  it('keeps nudging after an ordinary prompt echo', () => {
    vi.useFakeTimers();
    const { bridge, onSend } = makeBridge(60);

    vi.advanceTimersByTime(61_000);
    expect(totalNudges(onSend)).toBe(1);

    // One line back: `\r\n` and a prompt. That is what a nudge should cost.
    bridge.write(out('\r\nuser@host:~$ '));
    vi.advanceTimersByTime(60_000);
    expect(totalNudges(onSend)).toBe(2);

    bridge.dispose();
  });

  it('does not mind the readline redraw a space-and-backspace causes', () => {
    // bash echoes ` \b \b` — no newlines, so the screenful guard stays asleep.
    vi.useFakeTimers();
    const { bridge, onSend, onNotice } = makeBridge(60);

    vi.advanceTimersByTime(61_000);
    bridge.write(out(' \b \b'));
    vi.advanceTimersByTime(120_000);
    expect(String(onNotice.mock.calls.at(-1)?.[1] ?? '')).not.toMatch(/stopped for this session/i);
    expect(totalNudges(onSend)).toBeGreaterThan(1);

    bridge.dispose();
  });
});

describe('anti-idle: the bare-space nudge (space-only)', () => {
  it('maps every mode to the bytes it sends', () => {
    expect(Array.from(nudgeBytes('space'))).toEqual(SPACE_BACKSPACE);
    expect(Array.from(nudgeBytes('line'))).toEqual(ENTER);
    expect(Array.from(nudgeBytes('space-only'))).toEqual(BARE_SPACE);
  });

  it('fires inside a full-screen program, where the other two stay silent', () => {
    // The reason this mode exists: a TUI with an idle timer of its own never sees
    // the other nudges. A space on its own is a real keystroke, so it does.
    vi.useFakeTimers();
    const { bridge, onSend, onNotice } = makeBridge(60, false, 'space-only');

    bridge.write(out('\x1b[?1049h'));
    vi.advanceTimersByTime(61_000);
    expect(countOf(onSend, 'space-only')).toBe(1);
    // A bare space is not a space-and-backspace, and it is never an Enter.
    expect(countOf(onSend, 'space')).toBe(0);
    expect(countOf(onSend, 'line')).toBe(0);
    // The once-per-session notice says the price out loud.
    expect(onNotice).toHaveBeenCalledTimes(1);
    expect(String(onNotice.mock.calls[0]?.[1] ?? '')).toMatch(/stays in whatever input box/);

    vi.advanceTimersByTime(120_000);
    expect(countOf(onSend, 'space-only')).toBe(3);
    expect(onNotice).toHaveBeenCalledTimes(1);

    bridge.dispose();
  });

  it('still keeps out of a transfer', () => {
    vi.useFakeTimers();
    const { bridge, onSend } = makeBridge(60, true, 'space-only');
    vi.advanceTimersByTime(300_000);
    expect(totalNudges(onSend)).toBe(0);
    bridge.dispose();
  });
});
