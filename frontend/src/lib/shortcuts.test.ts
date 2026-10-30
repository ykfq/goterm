/*
 * Who owns a keystroke.
 *
 * The regression this pins: typing Ctrl+W a few times in a remote shell to rub
 * out words used to delete the *tab* along with them. readline has owned Ctrl+W
 * since Unix; the app's close-tab moved to the shifted form, which no shell
 * binds.
 */
import { describe, expect, it } from 'vitest';

import { appOwnsKey, shortcutFor, type KeyStroke } from './shortcuts';

function key(name: string, modifiers: { ctrl?: boolean; meta?: boolean; shift?: boolean } = {}): KeyStroke {
  return {
    key: name,
    ctrlKey: Boolean(modifiers.ctrl),
    metaKey: Boolean(modifiers.meta),
    shiftKey: Boolean(modifiers.shift),
  };
}

/** The terminal has the keyboard: the user is typing at a remote shell. */
const SHELL = { terminalFocused: true };
/** Focus is elsewhere (sidebar, dialog, tab strip): the app's shortcuts apply. */
const APP = { terminalFocused: false };

describe('Ctrl+W', () => {
  it('belongs to the shell while the terminal is focused', () => {
    expect(shortcutFor(key('w', { ctrl: true }), SHELL)).toBeNull();
    expect(shortcutFor(key('W', { ctrl: true }), SHELL)).toBeNull();
    expect(shortcutFor(key('w', { meta: true }), SHELL)).toBeNull();
  });

  it('closes the tab when the terminal does not have the keyboard', () => {
    expect(shortcutFor(key('w', { ctrl: true }), APP)).toBe('close-tab');
  });

  it('is always available as Ctrl+Shift+W', () => {
    expect(shortcutFor(key('w', { ctrl: true, shift: true }), SHELL)).toBe('close-tab');
    expect(shortcutFor(key('W', { ctrl: true, shift: true }), APP)).toBe('close-tab');
  });
});

describe('the rest of the shell’s control keys', () => {
  it('leaves Ctrl+C and Ctrl+V to the terminal (SIGINT, bracketed paste)', () => {
    expect(shortcutFor(key('c', { ctrl: true }), SHELL)).toBeNull();
    expect(shortcutFor(key('v', { ctrl: true }), SHELL)).toBeNull();
    expect(shortcutFor(key('c', { ctrl: true }), APP)).toBeNull();
  });

  it('takes the shifted variants for copy and paste', () => {
    expect(shortcutFor(key('c', { ctrl: true, shift: true }), SHELL)).toBe('copy');
    expect(shortcutFor(key('v', { ctrl: true, shift: true }), SHELL)).toBe('paste');
  });
});

describe('the shell’s own control keys', () => {
  // readline, bash and tmux all bind unshifted control keys. Stealing them
  // breaks the program the user is actually typing into.
  const contested: [string, { ctrl: boolean }][] = [
    ['w', { ctrl: true }], // unix-word-rubout
    ['k', { ctrl: true }], // kill-line
    ['t', { ctrl: true }], // transpose-chars
    ['b', { ctrl: true }], // tmux prefix
  ];

  it('reach the shell while the terminal is focused', () => {
    for (const [name, modifiers] of contested) {
      expect(shortcutFor(key(name, modifiers), SHELL), `Ctrl+${name.toUpperCase()}`).toBeNull();
    }
    // Ctrl+C (SIGINT) and Ctrl+V (bracketed paste) were never ours at all.
    expect(shortcutFor(key('c', { ctrl: true }), SHELL)).toBeNull();
    expect(shortcutFor(key('v', { ctrl: true }), SHELL)).toBeNull();
    expect(shortcutFor(key('c', { ctrl: true }), APP)).toBeNull();
  });

  it('still do the app thing when the terminal does not have the keyboard', () => {
    expect(shortcutFor(key('w', { ctrl: true }), APP)).toBe('close-tab');
    expect(shortcutFor(key('k', { ctrl: true }), APP)).toBe('clear');
    expect(shortcutFor(key('t', { ctrl: true }), APP)).toBe('quick-connect');
    expect(shortcutFor(key('b', { ctrl: true }), APP)).toBe('sidebar');
  });

  it('are always available to the app in their shifted form', () => {
    const shifted: [string, string][] = [
      ['w', 'close-tab'],
      ['k', 'clear'],
      ['t', 'quick-connect'],
      ['b', 'sidebar'],
      ['c', 'copy'],
      ['v', 'paste'],
      ['f', 'find'],
    ];
    for (const [name, command] of shifted) {
      expect(shortcutFor(key(name, { ctrl: true, shift: true }), SHELL), `Ctrl+Shift+${name.toUpperCase()}`).toBe(command);
      expect(shortcutFor(key(name.toUpperCase(), { ctrl: true, shift: true }), APP), name).toBe(command);
    }
  });
});

describe('appOwnsKey', () => {
  it('claims what the app acts on while the terminal is focused', () => {
    expect(appOwnsKey(key('W', { ctrl: true, shift: true }))).toBe(true);
    expect(appOwnsKey(key('C', { ctrl: true, shift: true }))).toBe(true);
    // Ctrl+F is `` (move forward a char) to readline; only the shifted form is
    // safe to take, and it has to be taken or xterm eats it first.
    expect(appOwnsKey(key('F', { ctrl: true, shift: true }))).toBe(true);
    expect(appOwnsKey(key('f', { ctrl: true }))).toBe(false);
    expect(appOwnsKey(key(',', { ctrl: true }))).toBe(true);
    expect(appOwnsKey(key('Tab', { ctrl: true }))).toBe(true);
  });

  it('leaves the shell’s keys, and plain typing, to xterm', () => {
    expect(appOwnsKey(key('w', { ctrl: true }))).toBe(false);
    expect(appOwnsKey(key('b', { ctrl: true }))).toBe(false);
    expect(appOwnsKey(key('k', { ctrl: true }))).toBe(false);
    expect(appOwnsKey(key('c', { ctrl: true }))).toBe(false);
    expect(appOwnsKey(key('a'))).toBe(false);
    expect(appOwnsKey(key('Enter'))).toBe(false);
  });
});

describe('app shortcuts', () => {
  it('map the rest of the table', () => {
    expect(shortcutFor(key(',', { ctrl: true }), SHELL)).toBe('settings');
    expect(shortcutFor(key('Tab', { ctrl: true }), SHELL)).toBe('next-tab');
    expect(shortcutFor(key('Tab', { ctrl: true, shift: true }), SHELL)).toBe('previous-tab');
    expect(shortcutFor(key('=', { ctrl: true }), SHELL)).toBe('zoom-in');
    expect(shortcutFor(key('-', { ctrl: true }), SHELL)).toBe('zoom-out');
    expect(shortcutFor(key('0', { ctrl: true }), SHELL)).toBe('zoom-reset');
  });

  it('maps plain Ctrl+F to nothing, so the shell keeps \x06', () => {
    expect(shortcutFor(key('f', { ctrl: true }), SHELL)).toBeNull();
    expect(shortcutFor(key('f', { ctrl: true }), APP)).toBeNull();
  });

  it('ignores plain keys and combinations nobody bound', () => {
    expect(shortcutFor(key('w'), APP)).toBeNull();
    expect(shortcutFor(key('w'), SHELL)).toBeNull();
    expect(shortcutFor(key('p', { ctrl: true, shift: true }), APP)).toBeNull();
    expect(shortcutFor(key('z', { ctrl: true }), SHELL)).toBeNull();
    expect(shortcutFor(key('Enter', { ctrl: true }), SHELL)).toBeNull();
  });
});

describe('while an IME is composing', () => {
  const composing = (name: string, modifiers: { ctrl?: boolean; shift?: boolean } = {}): KeyStroke => ({
    ...key(name, modifiers),
    isComposing: true,
  });

  it('has no app command, however the keys are pressed', () => {
    // The letters arriving here are pinyin on their way to 中文字, and the IME
    // owns the keyboard until it commits. Acting on one would eat the input and
    // cancel the composition — which is how an input method ends up stuck in its
    // English mode.
    expect(shortcutFor(composing('w', { ctrl: true, shift: true }), SHELL)).toBeNull();
    expect(shortcutFor(composing('w', { ctrl: true, shift: true }), APP)).toBeNull();
    expect(shortcutFor(composing('t', { ctrl: true, shift: true }), APP)).toBeNull();
    expect(shortcutFor(composing(',', { ctrl: true }), SHELL)).toBeNull();
    expect(shortcutFor(composing('Tab', { ctrl: true }), SHELL)).toBeNull();
    // xterm is told the same thing: not ours, let the IME have it.
    expect(appOwnsKey(composing('w', { ctrl: true, shift: true }))).toBe(false);
  });

  it('goes back to normal the moment the composition ends', () => {
    expect(shortcutFor({ ...key('w', { ctrl: true, shift: true }) }, SHELL)).toBe('close-tab');
    expect(shortcutFor({ ...key('Tab', { ctrl: true }), isComposing: false }, SHELL)).toBe('next-tab');
  });
});
