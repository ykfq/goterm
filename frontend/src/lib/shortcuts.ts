/*
 * Key handling policy: which keystroke is ours, and which belongs to the remote
 * shell.
 *
 * A terminal is a keyboard application, and the shell (or the multiplexer
 * running inside it) has first claim on the control keys it has always used:
 *
 *   Ctrl+W  readline    delete the word before the cursor
 *   Ctrl+K  readline    kill to end of line
 *   Ctrl+T  bash        transpose the last two characters
 *   Ctrl+B  tmux/screen the prefix — every tmux command starts with it
 *   Ctrl+C  SIGINT      Ctrl+V  bracketed paste
 *
 * Intercepting those is not a small annoyance. Press Ctrl+W three times to fix
 * a typo and the *tab* used to disappear, taking the session with it; Ctrl+B in
 * a tmux session never reached tmux at all.
 *
 * The rule: while the terminal has the keyboard, those keys go to the shell and
 * the app says nothing. Each app command keeps a shifted form (Ctrl+Shift+…),
 * which no shell binds, and the plain form still works whenever focus is
 * somewhere else (sidebar, dialog, tab strip).
 */
export interface KeyStroke {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  shiftKey: boolean;
  /** True while an IME is composing — the keystroke belongs to the IME. */
  isComposing?: boolean;
}

export type Shortcut =
  | 'copy'
  | 'paste'
  | 'close-tab'
  | 'quick-connect'
  | 'sidebar'
  | 'clear'
  | 'next-tab'
  | 'previous-tab'
  | 'settings'
  | 'zoom-in'
  | 'zoom-out'
  | 'zoom-reset'
  | 'find';

/**
 * The unshifted control keys a shell owns, and the app command that gains a
 * shifted form because of it. Lower-cased key names.
 */
const SHELL_KEYS: Record<string, Shortcut> = {
  b: 'sidebar',
  k: 'clear',
  t: 'quick-connect',
  w: 'close-tab',
};

/**
 * What this keystroke means to the app — `null` means "not ours, let the shell
 * have it".
 *
 * `terminalFocused` decides the contested keys above; everything else is either
 * shifted (no shell binds it) or has no readline meaning.
 */
export function shortcutFor(event: KeyStroke, options: { terminalFocused: boolean }): Shortcut | null {
  // Mid-composition the keyboard belongs to the IME: the letters on the way in
  // are pinyin, not commands. Acting on them would eat the user's input *and*
  // leave the IME with a cancelled composition, which on Windows shows up as an
  // input method stuck in English mode.
  if (event.isComposing) return null;
  const mod = event.ctrlKey || event.metaKey;
  if (!mod) return null;

  const lower = event.key.toLowerCase();

  // Shifted forms are the app's: a shell does not bind Ctrl+Shift+<letter>.
  if (event.shiftKey) {
    if (event.key === 'Tab') return 'previous-tab';
    if (lower === 'c') return 'copy';
    if (lower === 'v') return 'paste';
    // Ctrl+Shift+F is "find" everywhere it exists, and a shell binds neither F
    // nor Ctrl+F, so the plain form stays Ctrl+F-free on purpose: readline's
    // Ctrl+F is forward-char.
    if (lower === 'f') return 'find';
    return SHELL_KEYS[lower] ?? null;
  }

  switch (event.key) {
    case 'Tab':
      return 'next-tab';
    case ',':
      return 'settings';
    case '=':
    case '+':
      return 'zoom-in';
    case '-':
    case '_':
      return 'zoom-out';
    case '0':
      return 'zoom-reset';
    default:
      break;
  }

  const contested = SHELL_KEYS[lower];
  if (!contested) return null;
  // The shell has the keyboard: this key is a readline/tmux command, not ours.
  return options.terminalFocused ? null : contested;
}

/**
 * True when the app will act on this keystroke while the terminal has focus.
 *
 * xterm is told to ignore exactly these, so a shortcut does not also reach the
 * remote shell as a control character — Ctrl+Shift+W should close the tab, not
 * close it *and* rub out a word first.
 */
export function appOwnsKey(event: KeyStroke): boolean {
  return shortcutFor(event, { terminalFocused: true }) !== null;
}
