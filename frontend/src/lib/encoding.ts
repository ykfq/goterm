/*
 * What encoding the remote writes in.
 *
 * The byte pipe is faithful — the tests assert all of 0x00-0xff and a UTF-8
 * character cut in half arrive unchanged — so when Chinese shows up as mojibake the
 * bytes really are not UTF-8. Plenty of older Linux hosts are still set to GBK
 * (`zh_CN.GBK`, and lrzsz-era boxes especially), and xterm decodes UTF-8 only, so
 * the translation has to happen here, on the way in.
 *
 * UTF-8 stays the identity path: the terminal gets the very bytes the server sent,
 * which is both the cheap path and the only one that can pass through output that
 * is not text at all (a `cat` of a gzipped file). Everything else goes through a
 * `TextDecoder` in **streaming** mode — a two-byte GBK character split across two
 * 64 KiB output chunks must not become two replacement characters.
 *
 * Typed input is not translated: it is sent as UTF-8 (see `toSendBytes`). Reading a
 * GBK host's output is the problem this solves; a GBK host that expects GBK *input*
 * still wants `export LANG=zh_CN.UTF-8` on the far side.
 */
export type TerminalEncoding = 'utf-8' | 'gb18030' | 'big5' | 'shift_jis';

export interface EncodingChoice {
  id: TerminalEncoding;
  label: string;
  hint: string;
}

export const TERMINAL_ENCODINGS: EncodingChoice[] = [
  { id: 'utf-8', label: 'UTF-8', hint: 'every modern Linux and macOS host (default)' },
  { id: 'gb18030', label: 'GB18030 · GBK · GB2312', hint: 'older Chinese hosts; also reads GB2312 and GBK' },
  { id: 'big5', label: 'Big5', hint: 'Traditional Chinese hosts' },
  { id: 'shift_jis', label: 'Shift_JIS', hint: 'Japanese hosts' },
];

const KNOWN = new Set<string>(TERMINAL_ENCODINGS.map((choice) => choice.id));

export function isTerminalEncoding(value: unknown): value is TerminalEncoding {
  return typeof value === 'string' && KNOWN.has(value);
}

/** What the terminal is handed: untouched bytes, or text once it was decoded. */
export type ScreenChunk = Uint8Array | string;

/**
 * Decodes remote output for the screen, keeping half a character between chunks.
 *
 * `decode` returns the same `Uint8Array` it was given while the encoding is UTF-8,
 * so the default path cannot drift from the byte-for-byte behaviour the rest of the
 * suite asserts.
 */
export class RemoteTextDecoder {
  private decoder: TextDecoder | null = null;
  private current: TerminalEncoding = 'utf-8';

  constructor(encoding: TerminalEncoding = 'utf-8') {
    this.setEncoding(encoding);
  }

  get encoding(): TerminalEncoding {
    return this.current;
  }

  setEncoding(encoding: TerminalEncoding): void {
    const next = isTerminalEncoding(encoding) ? encoding : 'utf-8';
    if (next === this.current) return;
    this.current = next;
    // A fresh decoder, deliberately: any half-character the old one was holding
    // belonged to a stream that has ended (or was never in that encoding), and
    // finishing it would print a character that was never there.
    this.decoder = next === 'utf-8' ? null : new TextDecoder(next, { fatal: false });
  }

  decode(bytes: Uint8Array): ScreenChunk {
    if (!this.decoder) return bytes;
    // `stream: true` keeps a trailing lead byte for the next call.
    return this.decoder.decode(bytes, { stream: true });
  }
}
