/*
 * lrzsz (`rz` / `sz`) support, for servers that have no trzsz.
 *
 * trzsz is the better protocol and this app is built around it, but it has to be
 * installed on the far side. Plenty of hosts only have lrzsz, and there `sz
 * backup.tar` used to do nothing but print `**\x18B00…` on the screen and wait
 * for a ZMODEM client that never came. This is that client.
 *
 * The protocol itself is zmodem.js (Apache-2.0, FGasper/zmodemjs, whose own tests
 * run against real lrzsz binaries); what lives here is the part specific to this
 * app:
 *
 *   - **Routing.** Every byte from the server passes through `consume()` first.
 *     Once a session is confirmed, the protocol owns the stream and nothing else
 *     sees it; the rest of the time every byte goes straight to the screen (with
 *     a fast path that skips the scanner entirely when a chunk cannot contain a
 *     ZMODEM start).
 *   - **Local files.** Downloads ask where to put the file with the *same*
 *     native Explorer dialog the rest of the app uses (the File System Access
 *     API is shimmed onto Go in lib/fsaccess.ts), then stream the payload into
 *     it. Uploads pick files the same way and stream them back out.
 *   - **Lifecycle.** `active()` is true exactly while a transfer runs: the
 *     terminal uses it to stand the anti-idle down, to skip keyword colouring,
 *     and to swallow keystrokes — typing into a ZMODEM stream would corrupt it.
 *
 * Direction is not guessed. The first header decides it: ZRQINIT means the far
 * side wants to send (`sz`), so we receive; ZRINIT means it wants to receive
 * (`rz`), so we send.
 */
import Zmodem from 'zmodem.js';

import { textToUtf8 } from './bytes';
// trzsz's own progress bar (vendored verbatim) — see src/vendor/trzsz/comm.ts.
import { TextProgressBar } from '../vendor/trzsz/progress';

import type {
  ZmodemDetection,
  ZmodemOffer,
  ZmodemSentry,
  ZmodemSentryConstructor,
  ZmodemSession,
} from 'zmodem.js';

const { Sentry } = Zmodem as unknown as { Sentry: ZmodemSentryConstructor };

/** Where a download can be written. */
export interface ZmodemWritable {
  write(bytes: Uint8Array): Promise<void>;
  close(): Promise<void>;
  abort(): Promise<void>;
}

/** One file the user chose to send. */
export interface ZmodemUpload {
  name: string;
  size: number;
  read(offset: number, length: number): Promise<Uint8Array>;
}

// Progress is deliberately not an event on this interface: the pipe draws trzsz's
// own bar into the terminal (vendor/trzsz/progress.ts), which is where `trz`/`tsz`
// draw theirs — the two look like the same program because they are the same code.

export interface ZmodemIo {
  /** Bytes that are not ZMODEM traffic: they belong on the screen. */
  toTerminal(bytes: Uint8Array): void;
  /**
   * Our own text for the screen (the progress line), already decoded.
   *
   * Kept apart from `toTerminal` because those bytes are the *remote's* and may be
   * decoded from a legacy code page, while this is ours and always UTF-8.
   */
  toTerminalText?(text: string): void;
  /** Protocol bytes for the remote. */
  toServer(bytes: Uint8Array): void;
  /** Ask where a download should go; `null` when the user cancels. */
  chooseDownloadTarget(name: string): Promise<ZmodemWritable | null>;
  /** Ask which files to send; `null`/empty when the user cancels. */
  chooseUploads(): Promise<ZmodemUpload[] | null>;
  onNotice(level: 'info' | 'warn' | 'error', message: string): void;
  /** Terminal width in cells, so the progress line can be fitted to it. */
  columns?(): number;
}

/**
 * How much outgoing file data is read per step. zmodem.js frames this into its
 * own 1024-byte subpackets, so this only bounds one read of the local file.
 */
const UPLOAD_CHUNK = 32 * 1024;

/**
 * One ZMODEM hex header: `**`, ZDLE, then hex — 21 bytes, which is also what the
 * scanner's own detection window (MAX_ZM_HEX_START_LENGTH) is sized to.
 */
/** One ZMODEM hex header, padding included. */
const HEADER_LENGTH = 21;
const ASTERISK = 0x2a;
const ZDLE = 0x18;

/** How long a session may go silent before this pipe lets the terminal go. */
const STALL_MS = 45_000;

/**
 * How long a tail may wait for the rest of a header before it is shown anyway.
 */
const HEADER_HOLD_MS = 120;

/**
 * Headers that only ever mean "I am still here".
 *
 * `ZACK` answers a ZSINIT, `ZRINIT` and `ZRQINIT` are hellos, and `ZRPOS` is the
 * offset a receiver is waiting at — which lrzsz also uses to poke a sender that
 * has gone quiet. None of them moves a transfer forward on their own, so while a
 * file dialog has us blocked there is nothing to do with one but ignore it.
 */
const POLL_HEADERS = ['ZACK', 'ZRINIT', 'ZRQINIT', 'ZRPOS', 'ZSINIT'];

const noop = (): void => undefined;

function isHexByte(byte: number): boolean {
  return (byte >= 0x30 && byte <= 0x39) || (byte >= 0x41 && byte <= 0x46) || (byte >= 0x61 && byte <= 0x66);
}

/**
 * Whether the bytes from `start` to the end of the chunk are still consistent
 * with the beginning of a header, and too short to be one.
 *
 * That last part matters. A complete header in hand is the scanner's to judge
 * right now; holding it back would delay detection by a chunk that may never
 * come, which is how a prompt ends up printed as `**` and nothing else.
 */
function unfinishedHeader(data: Uint8Array, start: number): boolean {
  const length = data.length - start;
  if (length <= 0 || length >= HEADER_LENGTH) return false;
  if (data[start] !== ASTERISK) return false;
  if (length === 1) return true;
  if (data[start + 1] !== ASTERISK) return false;
  if (length === 2) return true;
  if (data[start + 2] !== ZDLE) return false;
  for (let i = start + 3; i < data.length; i += 1) {
    if (!isHexByte(data[i] as number)) return false;
  }
  return true;
}

/** Where an unfinished header begins in this chunk's tail, else its length. */
function tailStart(data: Uint8Array): number {
  const from = Math.max(0, data.length - (HEADER_LENGTH - 1));
  for (let i = from; i < data.length; i += 1) {
    if (data[i] === ASTERISK && unfinishedHeader(data, i)) return i;
  }
  return data.length;
}

function concatBytes(first: Uint8Array, second: Uint8Array): Uint8Array {
  const out = new Uint8Array(first.length + second.length);
  out.set(first);
  out.set(second, first.length);
  return out;
}

const ABORT = { name: 'AbortError' };

const EMPTY = new Uint8Array(0);

function isAbort(error: unknown): boolean {
  if (error instanceof Error) return error.name === ABORT.name;
  return typeof error === 'string' && /abort/i.test(error);
}

function message(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}

/**
 * A file name that is safe to create: ZMODEM carries whatever the far side put in
 * the header, and `../../etc/passwd` is a legal thing for it to say.
 */
export function safeFileName(offered: string): string {
  const base = offered.split(/[\\/]/).filter(Boolean).pop() ?? '';
  // eslint-disable-next-line no-control-regex
  const cleaned = base.replace(/[\x00-\x1f<>:"|?*]/g, '_').trim();
  return cleaned && cleaned !== '.' && cleaned !== '..' ? cleaned : 'download.bin';
}

type FilePickerWindow = {
  showDirectoryPicker?: (options?: { id?: string; mode?: string }) => Promise<unknown>;
  showOpenFilePicker?: (options?: { multiple?: boolean }) => Promise<unknown[]>;
};

type DirectoryLike = {
  getFileHandle(name: string, options?: { create?: boolean }): Promise<{ createWritable(): Promise<ZmodemWritable> }>;
};

type FileHandleLike = {
  name?: string;
  getFile(): Promise<{ name: string; size: number; lastModified?: number; slice(a?: number, b?: number): { arrayBuffer(): Promise<ArrayBuffer> } }>;
};

/**
 * The transfer engine for one terminal.
 *
 * One instance per tab: a ZMODEM session belongs to the byte stream it was
 * detected in, and two tabs must never see each other's handshake.
 */
export class ZmodemPipe {
  private readonly sentry: ZmodemSentry;
  private session: ZmodemSession | null = null;
  private disposed = false;
  /**
   * The tail of the previous chunk that could still grow into a ZMODEM header.
   * It has not been shown to the user yet, so it cannot be shown twice.
   *
   * This has to live here rather than in the Sentry: the Sentry's detection cache
   * keeps the *first* 21 bytes it retained (`cache.splice(MAX_ZM_HEX_START_LENGTH)`
   * in zsentry.js truncates to the head), so a `**` that lands at the end of a
   * 64 KiB chunk is discarded before the rest of the header arrives. Go batches
   * output by time and size at 64 KiB, which is exactly how that happens.
   */
  private held = EMPTY;
  private timer: ReturnType<typeof setTimeout> | null = null;
  /** Fires when a session has gone silent for so long it is certainly over. */
  private stall: ReturnType<typeof setTimeout> | null = null;
  /** Set once the scanner throws: from then on its output is the screen only. */
  private broken = false;
  /**
   * How many file dialogs are open right now.
   *
   * While one is open the far side is waiting on a human, and the header it sends
   * in the meantime is a poll rather than progress — see `toleratePollHeaders`.
   */
  private dialogs = 0;
  /** trzsz's progress bar while a transfer runs; null when nothing is moving. */
  private bar: TextProgressBar | null = null;
  /** Bytes moved and the announced size, for deciding how the line ends. */
  private progressDone = 0;
  private progressTotal = 0;

  constructor(private readonly io: ZmodemIo) {
    this.sentry = new Sentry({
      to_terminal: (octets) => this.io.toTerminal(Uint8Array.from(octets)),
      sender: (octets) => {
        this.io.toServer(Uint8Array.from(octets));
        this.touched();
      },
      on_detect: (detection) => this.onDetect(detection),
      on_retract: () => {
        // The bytes looked like ZMODEM and then did not; the Sentry hands the
        // stream back on its own. Nothing to keep: this pipe holds no state about
        // a header beyond `held`, which is released by the next chunk or its timer.
      },
    });
  }

  /** True while a transfer owns the stream. */
  active(): boolean {
    return this.session !== null;
  }

  /**
   * Feed every byte that arrives from the server.
   *
   * Returns true when the protocol consumed the bytes, which callers use to skip
   * anything that would otherwise be done with terminal output.
   */
  consume(bytes: Uint8Array): boolean {
    if (this.disposed || !bytes.length) return false;
    this.touched();

    if (this.session) {
      this.feed(bytes);
      return true;
    }

    const data = this.held.length ? concatBytes(this.held, bytes) : bytes;
    this.releaseHeld(false);

    // The fast path the whole design is there to protect: a ZMODEM start is `**`,
    // ZDLE, then hex, so a chunk with no asterisk in it — and no held tail
    // carrying one — cannot hide the start of one, and copying 64 KiB into the
    // scanner's byte array would be pure waste. Not sticky, deliberately: `echo *`
    // must not switch this path off for the rest of the tab's life.
    if (data.indexOf(0x2a) < 0) {
      this.io.toTerminal(data);
      return false;
    }

    // Hand the scanner everything up to a tail that might still grow into a
    // header, and keep that tail for the next chunk.
    const holdFrom = tailStart(data);
    // No view when nothing is held: the caller's own buffer goes through, which is
    // both one object fewer on the hot path and the identity the tests rely on.
    const feed = holdFrom === data.length ? data : data.subarray(0, holdFrom);
    const tail = holdFrom === data.length ? EMPTY : data.subarray(holdFrom);
    if (feed.length) this.feed(feed);

    if (this.session) {
      // A session just started, so whatever trailed is its bytes, not a guess
      // about one.
      if (tail.length) this.feed(tail);
      return true;
    }
    if (tail.length) this.holdForNow(tail);
    return this.session !== null;
  }

  /** Drop the pipe: a closed tab must not leave a session half open. */
  dispose(): void {
    this.disposed = true;
    this.clearStall();
    this.releaseHeld(false);
    this.abortSession();
  }

  /**
   * Traffic in either direction restarts the stall clock, so a working transfer is
   * never interrupted — and a sender that is patiently waiting retransmits its
   * header every few seconds, which counts as traffic too.
   */
  private touched(): void {
    if (!this.session) return;
    this.clearStall();
    this.stall = setTimeout(() => this.giveUp(), STALL_MS);
  }

  /**
   * Give up on a session that has stopped answering.
   *
   * This is the only way out of a stuck transfer: while a session owns the stream
   * the terminal swallows keystrokes, because typing into a packet corrupts the
   * file. Without this, a far side that dies mid-handshake leaves a tab that
   * cannot be typed in and cannot be rescued except by closing it.
   */
  private giveUp(): void {
    this.stall = null;
    if (!this.session || this.disposed) return;
    this.abortSession();
    this.io.onNotice(
      'warn',
      `ZMODEM ended: nothing came from the server for ${Math.round(STALL_MS / 1000)}s. ` +
        'This terminal takes typing again; run rz / sz once more when you want the transfer.',
    );
  }

  private clearStall(): void {
    if (this.stall === null) return;
    clearTimeout(this.stall);
    this.stall = null;
  }

  /** One step into the scanner, which must never be able to eat the user's bytes. */
  private feed(bytes: Uint8Array): void {
    if (this.broken) {
      this.io.toTerminal(bytes);
      return;
    }
    const hadSession = this.session !== null;
    try {
      this.sentry.consume(bytes);
    } catch (error) {
      // Whatever threw, these bytes belong to the terminal: losing text is worse
      // than losing the transfer.
      this.io.toTerminal(bytes);
      const reason = message(error);
      if (hadSession) {
        // A session that threw can never finish, and while it is the current one
        // this pipe holds keystrokes (typing into a packet corrupts a file), so
        // the tab looks hung with no way back. End the session, tell the far side,
        // and leave the scanner ready for the next `rz` / `sz`.
        this.abortSession();
        this.io.onNotice(
          'warn',
          `ZMODEM ended: ${reason}. The terminal takes typing again; run rz / sz once more.`,
        );
        return;
      }
      // No session yet, so it was the scanner itself that gave up — zmodem.js
      // spreads its input through `Function.prototype.apply`, which throws past
      // roughly 131 072 bytes. A scanner that threw once is not trusted with
      // output again; Go caps a chunk at 64 KiB, so this is a safety net.
      this.broken = true;
      this.io.onNotice('warn', `ZMODEM scan disabled: ${reason}`);
    }
  }

  /**
   * Answer a poll instead of throwing over it.
   *
   * zmodem.js is unmaintained and expects exactly one header at each step. It also
   * runs a five-second "keepalive" that sends a ZSINIT and then leaves `{ZACK}` (or
   * nothing at all) as the only header it will accept. Meanwhile the far side is
   * waiting for a human to pick a file, and lrzsz's `rz` pokes back with a `ZRPOS`
   * at the offset it is waiting at; `sz` re-announces itself with `ZRQINIT`. Either
   * one makes the library throw `Unhandled header: ZRPOS` from inside `consume()`.
   *
   * `_next_header_handler` is a plain property that the library assigns at every
   * step and reads once per header, so a pair of accessors can sit in front of it:
   * whatever the library assigned wins, the polls above get a no-op — but *only*
   * while a dialog is open — and everything else (`ZFILE`, `ZDATA`, `ZEOF`,
   * `ZSKIP`, `ZABORT`, …) still throws, because swallowing those would hang a
   * transfer instead of ending it.
   */
  private toleratePollHeaders(session: ZmodemSession): void {
    const state: { handlers: Record<string, unknown> | null } = { handlers: null };
    Object.defineProperty(session, '_next_header_handler', {
      configurable: true,
      get: () => {
        const handlers: Record<string, unknown> = { ...state.handlers };
        if (this.dialogs > 0) {
          for (const name of POLL_HEADERS) {
            if (!(name in handlers)) handlers[name] = noop;
          }
        }
        return handlers;
      },
      set: (handlers: Record<string, unknown> | null) => {
        state.handlers = handlers;
      },
    });
  }

  /**
   * Run a promise whose wait is a human staring at a file dialog.
   *
   * The count, not a flag: a download dialog can open over the tail of an upload.
   */
  private async whileDialog<T>(run: () => Promise<T>): Promise<T> {
    this.dialogs += 1;
    try {
      return await run();
    } finally {
      this.dialogs -= 1;
    }
  }

  /**
   * Start trzsz's progress bar for a transfer of `count` files.
   *
   * This is the class `trz`/`tsz` use, driven in the order their transfer code
   * drives it (`onNum`, then per file `onName`/`onSize`/`onStep`/`onDone`), so both
   * kinds of transfer draw the same line. It writes into the terminal, which is
   * where trzsz draws its own — that is the whole point — so this pipe needs no
   * UI state, no markup and no styling of its own.
   */
  private startBar(count: number): void {
    const bar = new TextProgressBar(
      (text) => this.writeText(text),
      this.io.columns?.() ?? 80,
    );
    bar.hideCursor();
    bar.onNum(count);
    this.bar = bar;
  }

  /** One file's line. trzsz paints on the first step, so 0 % starts it off. */
  private barFile(name: string, size: number): void {
    this.progressTotal = size;
    this.progressDone = 0;
    if (!this.bar) return;
    this.bar.onName(name);
    this.bar.onSize(size);
    this.bar.onStep(0);
  }

  private barStep(done: number): void {
    this.progressDone = done;
    if (!this.bar) return;
    // A resize mid-transfer: the bar fits itself to the width it is told.
    this.bar.setTerminalColumns(this.io.columns?.() ?? 80);
    this.bar.onStep(done);
  }

  /**
   * End the line: 100 % when every byte arrived, wiped when the transfer died.
   *
   * `completed` cannot be read off the session — ZFIN (or our own close) can arrive
   * while the last bytes are still being written to disk — so the caller passes what
   * the byte count says. The newline is ours: trzsz's bar repaints in place and
   * leaves the line to whatever prints next, which here would be the shell prompt.
   */
  private endBar(completed: boolean): void {
    const bar = this.bar;
    if (!bar) return;
    this.bar = null;
    if (completed) {
      bar.onDone();
      this.writeText('\r\n');
    } else {
      this.writeText('\r\x1b[K');
    }
    bar.showCursor();
  }

  /** Our own text to the screen; UTF-8 bytes when the host has no text channel. */
  private writeText(text: string): void {
    if (this.io.toTerminalText) this.io.toTerminalText(text);
    else this.io.toTerminal(textToUtf8(text));
  }

  /** Park a tail until the next chunk, or until it is clearly not a header. */
  private holdForNow(tail: Uint8Array): void {
    this.held = tail.slice();
    this.timer = setTimeout(() => this.releaseHeld(true), HEADER_HOLD_MS);
  }

  private releaseHeld(toTerminal: boolean): void {
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    if (!this.held.length) return;
    const tail = this.held;
    this.held = EMPTY;
    if (toTerminal) this.io.toTerminal(tail);
  }

  private onDetect(detection: ZmodemDetection): void {
    let session: ZmodemSession;
    try {
      session = detection.confirm();
    } catch {
      // A stale detection: more bytes arrived and it was retracted first.
      return;
    }
    this.session = session;
    // Before the first `start()` / `offer`: from here on the library decides what
    // it expects next, and a poll in the wrong moment must not end the tab.
    this.toleratePollHeaders(session);
    this.io.onNotice('info', `ZMODEM: ${session.type === 'receive' ? 'download' : 'upload'} started`);

    session.on('session_end', () => {
      // Whatever ended it — ZFIN, a peer abort, our own abort — the line belongs to
      // this session. Whether that was a finished transfer or an abandoned one is
      // decided by the bytes that actually moved.
      this.endBar(this.progressTotal > 0 && this.progressDone >= this.progressTotal);
      if (this.session === session) {
        this.session = null;
        this.clearStall();
      }
    });

    if (session.type === 'receive') this.receive(session);
    else void this.send(session);
  }

  private abortSession(): void {
    this.clearStall();
    const session = this.session;
    this.session = null;
    if (!session) return;
    try {
      session.abort();
    } catch {
      /* aborting a session that already ended is not a problem */
    }
  }

  // --- receiving (`sz` on the server) --------------------------------------

  private receive(session: ZmodemSession): void {
    session.on('offer', (offer) => {
      void this.acceptOffer(offer);
    });
    session.start();
  }

  private async acceptOffer(offer: ZmodemOffer): Promise<void> {
    const details = offer.get_details();
    const name = safeFileName(details.name);
    let target: ZmodemWritable | null = null;
    try {
      target = await this.whileDialog(() => this.io.chooseDownloadTarget(name));
    } catch (error) {
      if (!isAbort(error)) this.io.onNotice('error', `ZMODEM: ${message(error)}`);
    }
    if (!target) {
      // The user said no: tell the sender to move on to the next file instead of
      // making it wait for a receiver that is never going to answer.
      offer.skip();
      return;
    }

    const total = details.size ?? 0;
    let moved = 0;
    this.startBar(1);
    this.barFile(name, total);

    let chain: Promise<void> = Promise.resolve();
    let failed: unknown = null;
    offer.on('input', (octets) => {
      const chunk = Uint8Array.from(octets);
      moved += chunk.length;
      this.barStep(moved);
      // Writes must land in order; one chain keeps them in the order the packets
      // arrived without buffering the whole file in memory.
      chain = chain.then(() => {
        if (failed) return undefined;
        return target.write(chunk).catch((error: unknown) => {
          failed = error;
        });
      });
    });

    try {
      await offer.accept();
      await chain;
      if (failed) throw failed;
      await target.close();
      this.endBar(true);
      this.io.onNotice('info', `Downloaded ${name}`);
    } catch (error) {
      this.endBar(false);
      await target.abort().catch(() => undefined);
      this.io.onNotice('error', `ZMODEM download of ${name} failed: ${message(error)}`);
      // Say so on the wire: an lrzsz sender sitting on a half-written file would
      // otherwise keep resending packets to a receiver that has already given up.
      this.abortSession();
    }
  }

  // --- sending (`rz` on the server) ----------------------------------------

  private async send(session: ZmodemSession): Promise<void> {
    let files: ZmodemUpload[] | null = null;
    try {
      files = await this.whileDialog(() => this.io.chooseUploads());
    } catch (error) {
      if (!isAbort(error)) this.io.onNotice('error', `ZMODEM: ${message(error)}`);
    }
    if (!files || !files.length) {
      // `rz` is waiting for a file that is not coming. ZFIN is the polite way to
      // say so; it exits with its own message on the far side.
      await session.close().catch(() => undefined);
      this.io.onNotice('info', 'ZMODEM upload cancelled');
      return;
    }

    let remaining = files.reduce((sum, file) => sum + file.size, 0);
    // One bar for the whole upload: `(2/3)` in the name is how trzsz shows which
    // file it is on, rather than a second bar underneath.
    this.startBar(files.length);
    try {
      for (let index = 0; index < files.length; index += 1) {
        const file = files[index]!;
        const transfer = await session.send_offer({
          name: file.name,
          size: file.size,
          mtime: new Date(),
          // ZMODEM counts these *including* the file being offered, and rejects a
          // zero — the last file is `1`, not `0`.
          files_remaining: files.length - index,
          bytes_remaining: remaining,
        });
        remaining -= file.size;
        if (!transfer) continue; // skipped by the receiver
        // One line for the whole upload, restarted per file.
        this.barFile(file.name, file.size);
        await this.writeFile(session, transfer, file);
      }
      this.endBar(true);
      await session.close();
      this.io.onNotice('info', `Sent ${files.length} file${files.length === 1 ? '' : 's'}`);
    } catch (error) {
      this.endBar(false);
      this.io.onNotice('error', `ZMODEM upload failed: ${message(error)}`);
      this.abortSession();
    }
  }

  private async writeFile(
    session: ZmodemSession,
    transfer: { send(bytes: Uint8Array): void; end(bytes?: Uint8Array): Promise<void> },
    file: ZmodemUpload,
  ): Promise<void> {
    let offset = 0;
    // Everything but the tail goes through send(); the last packet goes through
    // end(), which is what tells the receiver the file is complete.
    while (offset < file.size) {
      if (session.aborted()) throw new Error('the far side aborted the transfer');
      const length = Math.min(UPLOAD_CHUNK, file.size - offset);
      const chunk = await file.read(offset, length);
      offset += length;
      if (offset >= file.size) {
        // The last packet is what tells the receiver the file is complete.
        this.barStep(offset);
        await transfer.end(chunk);
      } else {
        transfer.send(chunk);
        this.barStep(offset);
      }
    }
    if (file.size === 0) await transfer.end(new Uint8Array(0));
  }
}

/**
 * The pickers this app installs over the File System Access API (see
 * lib/fsaccess.ts): a desktop user gets Explorer, and the same call works in the
 * webview as it does in a browser with the native pickers.
 */
export async function chooseDownloadTarget(name: string): Promise<ZmodemWritable | null> {
  const picker = (window as unknown as FilePickerWindow).showDirectoryPicker;
  if (typeof picker !== 'function') throw new Error('this system has no file picker');
  let directory: unknown;
  try {
    directory = await picker.call(window, { id: 'goterm-zmodem', mode: 'readwrite' });
  } catch (error) {
    if (isAbort(error)) return null;
    throw error;
  }
  const dir = directory as DirectoryLike;
  const handle = await dir.getFileHandle(name, { create: true });
  return handle.createWritable();
}

export async function chooseUploads(): Promise<ZmodemUpload[] | null> {
  const picker = (window as unknown as FilePickerWindow).showOpenFilePicker;
  if (typeof picker !== 'function') throw new Error('this system has no file picker');
  let handles: unknown[];
  try {
    handles = await picker.call(window, { multiple: true });
  } catch (error) {
    if (isAbort(error)) return null;
    throw error;
  }
  const uploads: ZmodemUpload[] = [];
  for (const entry of handles) {
    const handle = entry as FileHandleLike;
    const file = await handle.getFile();
    uploads.push({
      name: safeFileName(handle.name ?? file.name),
      size: file.size,
      read: async (offset, length) => new Uint8Array(await file.slice(offset, offset + length).arrayBuffer()),
    });
  }
  return uploads;
}
