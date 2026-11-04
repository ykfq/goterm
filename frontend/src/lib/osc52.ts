/*
 * OSC 52: the clipboard a program inside the terminal does not have.
 *
 * A full-screen program on the far side of an SSH session cannot reach this
 * machine's clipboard — `clip.exe`, `xclip` and `pbcopy` are not on that host —
 * so its own "Copy" item prints the text out as
 * `ESC ] 52 ; c ; <base64> BEL` and trusts the terminal to do the copying. Every
 * desktop emulator people use (Windows Terminal, iTerm2, kitty, WezTerm, VS
 * Code, tmux's own passthrough) implements that half of the protocol. xterm.js
 * deliberately does not, which is why the Copy item in a TUI's own right-click
 * menu did nothing in GoTerm: the bytes reached the parser and were dropped as
 * an unknown OSC.
 *
 * Only the write half is implemented, on purpose. The read query (`Pd` = `?`)
 * would let whatever the host runs read this machine's clipboard, and no
 * terminal session legitimately needs that for a menu item; a query is ignored
 * instead of answered, which is what a program should read as "copy is
 * write-only here".
 */

import { base64ToBytes, utf8ToText } from './bytes';

/**
 * Largest payload accepted, in base64 characters — about 3 MiB of text.
 * xterm's own OSC buffer gives up at 10 MB; a clip that big is a select-all
 * accident, and decoding it happens on the terminal's input path.
 */
export const MAX_OSC52_CHARS = 4 << 20;

export type Osc52Copy = { kind: 'copy'; text: string } | { kind: 'ignore' };

const IGNORE: Osc52Copy = { kind: 'ignore' };

/**
 * The text an OSC 52 payload asks to put on the clipboard, or `ignore`.
 *
 * `data` is what xterm hands an OSC-52 handler: the sequence's payload with the
 * `52;` identifier already removed, i.e. `Pc;Pd`. `Pc` names one or more
 * selections (`c`, `p`, `s0`…`s7`, `c;p`, or empty for the default one); Windows
 * has a single clipboard, so every selection means the same thing here.
 */
export function parseOsc52(data: string): Osc52Copy {
  // Base64 has no `;` in its alphabet, so the last field is the payload whatever
  // the selection list looks like.
  const split = data.lastIndexOf(';');
  if (split < 0) return IGNORE;

  // Whitespace outside the fields is noise a shell or a log may have wrapped in;
  // it is not part of any base64 string.
  const payload = data.slice(split + 1).replace(/\s+/g, '');
  if (!payload) return IGNORE;
  if (payload === '?') return IGNORE; // a read query: never answered
  if (payload.length > MAX_OSC52_CHARS) return IGNORE;
  // A base64 string can never be 4n+1 characters long, padded or not.
  if (payload.length % 4 === 1) return IGNORE;
  if (!/^[A-Za-z0-9+/]+={0,2}$/.test(payload)) return IGNORE;

  let text: string;
  try {
    text = utf8ToText(base64ToBytes(payload));
  } catch {
    // Malformed base64 is a broken sender, not a reason to take the terminal's
    // input path down with it.
    return IGNORE;
  }
  // Copying nothing would silently wipe whatever the user had; leave it alone.
  if (!text) return IGNORE;
  return { kind: 'copy', text };
}
