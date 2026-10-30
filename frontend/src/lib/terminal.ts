/*
 * One xterm.js terminal plus its trzsz filter, per tab.
 *
 * This file owns the byte pipe:
 *
 *   remote ──b64──▶ base64ToBytes ─▶ trzsz.processServerOutput ─▶ term.write
 *   keyboard ─────▶ trzsz.processTerminalInput ─▶ toSendBytes ──▶ Go (b64)
 *
 * trzsz sits in the middle in *both* directions, so it can sniff the
 * ::TRZSZ:TRANSFER: banner out of the output stream and take over input while a
 * transfer runs. That is also why nothing here may lossily convert bytes — the
 * whole point of this project is that the pipe stays binary-exact.
 */
import { Terminal } from '@xterm/xterm';
import type { ITheme } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { SearchAddon } from '@xterm/addon-search';
import type { ISearchDecorationOptions, ISearchOptions } from '@xterm/addon-search';
import { UnicodeGraphemesAddon } from '@xterm/addon-unicode-graphemes';
import { WebLinksAddon } from '@xterm/addon-web-links';
import { WebglAddon } from '@xterm/addon-webgl';
import { TrzszFilter } from 'trzsz';

import { ZmodemPipe, chooseDownloadTarget, chooseUploads } from './zmodem';

import { base64ToBytes, bytesToLatin1, latin1ToBytes, toSendBytes } from './bytes';
import { RemoteTextDecoder } from './encoding';
import type { TerminalEncoding } from './encoding';
import { writeClipboard } from './clipboard';
import { OutputHighlighter } from './highlight';
import type { CustomRule } from './highlight';
import { filterStdoutWrite, isLibraryNoise, isRemoteStackDump, isTransferCancel } from './transfer';
import { appOwnsKey } from './shortcuts';

export type ThemeName = 'dark' | 'light';

/**
 * What the anti-idle puts into the session when it fires.
 *
 * - `space` — a space and a backspace. Two real keystrokes that cancel out: the
 *   line is untouched, nothing can execute, and it works even when the user has
 *   text typed but not submitted (a `>` continuation prompt, say). This is the
 *   default because it is the safe one.
 * - `line` — a completed line. A new prompt each time, and only safe on an empty
 *   line, but it is what a shell whose `TMOUT` insists on a finished line needs.
 *
 * A NUL byte was tried too and is not offered: it did not reset either a direct
 * login's `TMOUT` or a JumpServer session (see README → Keepalives).
 */
/**
 * What the anti-idle sends when a session has been quiet for a while.
 *
 * - `space` (default): a space and a backspace. Two keystrokes that cancel out,
 *   so the line is exactly as the user left it — but it is still *input*, which
 *   is what `TMOUT` and a bastion's activity detector count.
 * - `line`: an Enter. Resets a shell that only counts completed lines, at the
 *   cost of a new prompt each time (and it is never sent with text pending).
 * - `space-only`: a single space, no backspace. This is the only mode that may
 *   fire inside a full-screen program, because it is also the only one that
 *   leaves something behind: in a TUI it types into whatever has focus. That is
 *   what makes it work for an app whose input box has its own idle timer — and
 *   what makes it wrong for `less` (space pages down), `fzf` (space selects an
 *   item) or an editor. Opt-in, never the default.
 */
export type IdleNudge = 'space' | 'line' | 'space-only';

const SPACE_BACKSPACE = new Uint8Array([0x20, 0x7f]);
/** One space and nothing else: survives, and in a TUI that is the point. */
const BARE_SPACE = new Uint8Array([0x20]);

/**
 * The bytes one nudge sends. A pure function so the three modes are pinned by
 * name rather than by reading the timer.
 */
export function nudgeBytes(mode: IdleNudge): Uint8Array {
  if (mode === 'line') return ENTER_KEY;
  if (mode === 'space-only') return BARE_SPACE;
  return SPACE_BACKSPACE;
}
const ENTER_KEY = new Uint8Array([0x0d]);

export interface TerminalTheme {
  background: string;
  foreground: string;
}

const PALETTES: Record<ThemeName, ITheme> = {
  dark: {
    background: '#0d1117',
    foreground: '#d7e0ea',
    cursor: '#4aa8ff',
    cursorAccent: '#0d1117',
    selectionBackground: '#214373',
    black: '#484f58',
    red: '#ff7b72',
    green: '#3fb950',
    yellow: '#d29922',
    blue: '#58a6ff',
    magenta: '#bc8cff',
    cyan: '#39c5cf',
    white: '#b1bac4',
    brightBlack: '#6e7681',
    brightRed: '#ffa198',
    brightGreen: '#56d364',
    brightYellow: '#e3b341',
    brightBlue: '#79c0ff',
    brightMagenta: '#d2a8ff',
    brightCyan: '#56d4dd',
    brightWhite: '#f0f6fc',
  },
  light: {
    background: '#ffffff',
    foreground: '#1f2933',
    cursor: '#0b62c4',
    cursorAccent: '#ffffff',
    selectionBackground: '#bfe0ff',
    black: '#24292e',
    red: '#d1242f',
    green: '#116329',
    yellow: '#4d2d00',
    blue: '#0969da',
    magenta: '#8250df',
    cyan: '#1b7c83',
    white: '#6e7781',
    brightBlack: '#57606a',
    brightRed: '#a40e26',
    brightGreen: '#2da44e',
    brightYellow: '#3b2300',
    brightBlue: '#218bff',
    brightMagenta: '#a475f9',
    brightCyan: '#3192aa',
    brightWhite: '#8c959f',
  },
};

/** The banner trzsz prints just before it takes a transfer over. */
const TRANSFER_MAGIC = new TextEncoder().encode('::TRZSZ:TRANSFER:');

/**
 * Relative luminance of a `#rrggbb` colour (the WCAG formula), 0 black … 1 white.
 * Returns 0 for anything that is not a colour, which is the safe answer: a pane
 * whose colour cannot be read keeps the theme's own ink.
 */
export function luminance(hex: string): number {
  const value = hex.trim().replace('#', '');
  if (!/^[0-9a-f]{6}$/i.test(value)) return 0;
  const channels = [0, 2, 4].map((at) => Number.parseInt(value.slice(at, at + 2), 16) / 255);
  const linear = channels.map((c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4));
  return 0.2126 * (linear[0] as number) + 0.7152 * (linear[1] as number) + 0.0722 * (linear[2] as number);
}

/** WCAG contrast ratio between two luminance values, 1 … 21. */
function contrast(a: number, b: number): number {
  const [high, low] = a > b ? [a, b] : [b, a];
  return (high + 0.05) / (low + 0.05);
}

/**
 * Which palette a pane should be painted with.
 *
 * Normally the theme decides. Once the user has chosen a pane colour of their
 * own, *that* decides, because the pane is what the text sits on: a black pane
 * under the light theme was black text on a black background — invisible, and no
 * warning in a settings dialog makes that acceptable. Whichever palette's
 * foreground contrasts better with the pane wins, so a mid-grey lands on the
 * side that actually reads.
 */
export function paneTheme(theme: ThemeName, background: string): ThemeName {
  if (!background) return theme;
  const pane = luminance(background);
  const onDark = contrast(pane, luminance(PALETTES.dark.foreground ?? '#ffffff'));
  const onLight = contrast(pane, luminance(PALETTES.light.foreground ?? '#000000'));
  return onDark >= onLight ? 'dark' : 'light';
}

/** The palette a terminal paints with, custom background and all. */
export function panePalette(theme: ThemeName, background: string): ITheme {
  return background ? { ...PALETTES[paneTheme(theme, background)], background } : PALETTES[theme];
}

/**
 * trzsz-js assumes it runs somewhere with Node's `process`: when a transfer
 * ends it writes the closing message — and the escape sequences that tidy up the
 * progress bar — to `process.stdout`. A webview has no `process`, so cancelling
 * a transfer threw "process is not defined" out of the library even though the
 * cancel itself had worked. Giving it a stdout fixes that; `filterStdoutWrite`
 * decides what the user actually sees, which is the cleanup and any real
 * failure, but not the narration of a transfer they just stopped.
 */
let stdoutSink: { id: string; write: (text: string) => void } | null = null;

export function bindTrzszStdout(id: string, sink: (text: string) => void): void {
  // The write lands in whichever pane's server bytes are moving; rebinding on
  // every write keeps that honest without inventing a session id the library
  // could not pass back anyway.
  stdoutSink = { id, write: sink };
  const host = globalThis as unknown as { process?: Record<string, unknown> };
  const stdout = host.process?.stdout as { write?: (chunk: unknown) => boolean } | undefined;
  if (stdout && typeof stdout.write === 'function') return;
  host.process = {
    ...(host.process ?? {}),
    stdout: {
      isTTY: false,
      write: (chunk: unknown): boolean => {
        const text = typeof chunk === 'string' ? chunk : chunk == null ? '' : String(chunk);
        const keep = filterStdoutWrite(text);
        if (keep !== null) stdoutSink?.write(keep);
        return true;
      },
    },
  };
}

/** Shortest chunk trzsz will sniff a banner in: banner (25 bytes) plus its newline. */
const TRANSFER_MAGIC_MIN_CHUNK = 26;
/** How long a held-back banner fragment may wait for the rest of itself. */
const MAGIC_HOLD_MS = 120;

function concatBytes(first: Uint8Array, second: Uint8Array): Uint8Array {
  if (!first.length) return second;
  if (!second.length) return first;
  const out = new Uint8Array(first.length + second.length);
  out.set(first, 0);
  out.set(second, first.length);
  return out;
}

function endsWithPrefix(data: Uint8Array, prefix: Uint8Array, length: number): boolean {
  const start = data.length - length;
  for (let i = 0; i < length; i++) {
    if (data[start + i] !== prefix[i]) return false;
  }
  return true;
}

function lastIndexOfBytes(data: Uint8Array, needle: Uint8Array): number {
  for (let at = data.length - needle.length; at >= 0; at--) {
    let found = true;
    for (let i = 0; i < needle.length; i++) {
      if (data[at + i] !== needle[i]) {
        found = false;
        break;
      }
    }
    if (found) return at;
  }
  return -1;
}

/**
 * How many trailing bytes must wait for the next chunk so trzsz sees the whole
 * banner at once.
 *
 * The output pump batches by time and size, so `::TRZSZ:TRANSFER:R:1.2.0:` can
 * land split across two events — and trzsz only looks for the banner inside a
 * single chunk, and only in at least 26 bytes. So a complete banner with less
 * than that behind it waits for its newline, and a trailing fragment that is
 * still a prefix of the banner waits for the rest of it.
 */
export function bannerHoldBack(data: Uint8Array, magic: Uint8Array = TRANSFER_MAGIC): number {
  const at = lastIndexOfBytes(data, magic);
  if (at >= 0) {
    const available = data.length - at;
    return available < TRANSFER_MAGIC_MIN_CHUNK ? available : 0;
  }
  const maxPrefix = Math.min(magic.length - 1, data.length);
  for (let length = maxPrefix; length > 0; length--) {
    if (endsWithPrefix(data, magic, length)) return length;
  }
  return 0;
}

/**
 * Behind a `#FAIL:` / `#fail:` frame, trzsz sends zlib-compressed, base64 text.
 * Decoding it turns "nothing happened" into an actual message in the UI.
 */
async function decodeFailFrame(frame: string): Promise<string | null> {
  const match = /^#(FAIL|fail):([^\r\n]*)/.exec(frame);
  if (!match) return null;
  const payload = (match[2] ?? '').trim();
  if (!payload) return 'transfer failed (empty #FAIL frame)';
  try {
    const bytes = base64ToBytes(payload);
    if (typeof DecompressionStream === 'function') {
      const raw = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer;
      const stream = new Blob([raw]).stream().pipeThrough(new DecompressionStream('deflate'));
      const text = new TextDecoder().decode(await new Response(stream).arrayBuffer());
      if (text.trim()) return text.trim();
    }
  } catch {
    /* Not deflate, or no DecompressionStream: fall through to the raw body. */
  }
  return payload;
}

/**
 * trzsz-js 1.1.6 reads a unique id out of the banner and then does
 * `id.length < 8` on it — with no id present that throws inside a detached
 * promise, so the transfer dies without a word and the banner is all the user
 * ever sees. trzsz 1.2.0 on the remote sends exactly that: a bare
 * `::TRZSZ:TRANSFER:R:1.2.0:`. No newer trzsz-js exists to upgrade to, so the
 * smallest id that satisfies the check is inserted on the way in and taken
 * off again on the way to the screen.
 */
const COMPAT_BANNER_ID = '0';
const BANNER_MISSING_ID = /(::TRZSZ:TRANSFER:[SRD]:\d+\.\d+\.\d+:)(?!\d)/g;
const BANNER_WITH_COMPAT_ID = /(::TRZSZ:TRANSFER:[SRD]:\d+\.\d+\.\d+:)0(?!\d)/g;

function rewriteBannerText(text: string): string {
  return text.replace(BANNER_MISSING_ID, `$1${COMPAT_BANNER_ID}`);
}

function unrewriteBannerText(text: string): string {
  return text.replace(BANNER_WITH_COMPAT_ID, '$1');
}

/**
 * Rewrites the banner inside a byte stream, leaving every other byte alone.
 *
 * The banner is pure ASCII and latin1 round-trips bytes one-to-one, so the
 * surrounding binary output cannot be disturbed by the substitution.
 */
function rewriteBannerBytes(data: Uint8Array, rewrite: (text: string) => string): Uint8Array {
  const at = lastIndexOfBytes(data, TRANSFER_MAGIC);
  if (at < 0) return data;
  const end = Math.min(data.length, at + TRANSFER_MAGIC.length + 32);
  const slice = bytesToLatin1(data.subarray(at, end));
  const rewritten = rewrite(slice);
  if (rewritten === slice) return data;
  const out = new Uint8Array(data.length + rewritten.length - slice.length);
  out.set(data.subarray(0, at), 0);
  out.set(latin1ToBytes(rewritten), at);
  out.set(data.subarray(end), at + rewritten.length);
  return out;
}

/** Server banner → the shape trzsz-js can parse. */
export function compatBannerBytes(data: Uint8Array): Uint8Array {
  return rewriteBannerBytes(data, rewriteBannerText);
}

/** Banner heading for the screen → exactly what the server sent. */
export function displayBannerBytes(data: Uint8Array | string): Uint8Array | string {
  return typeof data === 'string' ? unrewriteBannerText(data) : rewriteBannerBytes(data, unrewriteBannerText);
}

/**
 * The part of trzsz that server bytes must pass through, narrowed down to what
 * this module actually calls (and to what a test can stand in for).
 */
export interface ServerOutputSink {
  processServerOutput: (bytes: Uint8Array) => void;
  isTransferringFiles: () => boolean;
}

/**
 * Feeds server output to the trzsz filter without ever losing bytes.
 *
 * trzsz sniffs the `::TRZSZ:TRANSFER:` banner out of a *single* chunk, while
 * the Go pump batches by time and size and may split that banner in two. A
 * trailing fragment that could still grow into a banner is therefore held back
 * briefly and re-joined with the next chunk; if nothing follows (a shell
 * prompt that merely ends in `:`) a timer releases it so the terminal is never
 * left short of text. While a transfer runs every byte is file data, so
 * nothing is ever held back there — holding transfer bytes can stall the
 * request/response protocol.
 */
export class ServerOutputStream {
  private pending = new Uint8Array(0);
  private timer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private readonly sink: ServerOutputSink,
    private readonly onError: (bytes: Uint8Array, error: unknown) => void = () => undefined,
    private readonly holdMs: number = MAGIC_HOLD_MS,
    private readonly rewriteBanners: boolean = true,
  ) {}

  write(bytes: Uint8Array): void {
    if (!bytes.length) return;
    if (this.timer !== null) {
      clearTimeout(this.timer);
      this.timer = null;
    }
    const transferring = this.sink.isTransferringFiles();
    const data = this.pending.length ? concatBytes(this.pending, bytes) : bytes;
    this.pending = new Uint8Array(0);
    const held = transferring ? 0 : bannerHoldBack(data);
    if (held) {
      this.pending = data.slice(data.length - held);
      this.timer = setTimeout(() => {
        this.timer = null;
        this.flush();
      }, this.holdMs);
    }
    const ready = held ? data.subarray(0, data.length - held) : data;
    if (ready.length) this.deliver(ready, !transferring);
  }

  /** Stop waiting for the rest of a banner: hand over what is buffered. */
  flush(): void {
    if (!this.pending.length) return;
    const bytes = this.pending;
    this.pending = new Uint8Array(0);
    this.deliver(bytes, true);
  }

  dispose(): void {
    if (this.timer !== null) clearTimeout(this.timer);
    this.timer = null;
    this.pending = new Uint8Array(0);
  }

  private deliver(bytes: Uint8Array, rewrite: boolean): void {
    try {
      // Scanning multi-megabyte file chunks for a banner would cost more than
      // it can ever return: a banner only appears between transfers.
      this.sink.processServerOutput(rewrite && this.rewriteBanners ? compatBannerBytes(bytes) : bytes);
    } catch (error) {
      // A filter that throws must never take the terminal down with it.
      this.onError(bytes, error);
    }
  }
}

export const DEFAULT_FONTS =
  "'Fira Code', 'Cascadia Mono', 'JetBrains Mono', 'Sarasa Mono SC', Consolas, 'Noto Sans Mono CJK SC', 'Microsoft YaHei Mono', monospace";

/**
 * What double-click and word-wise selection treat as a word boundary — xterm's
 * own default, spelled out so the settings screen can show and restore it.
 */
export const DEFAULT_WORD_SEPARATOR = ' ()[]{}\'"';

/** CSS stack tail: a custom family never loses its monospace fallback. */
export function fontStack(family: string): string {
  const trimmed = family.trim();
  if (!trimmed) return DEFAULT_FONTS;
  return /monospace/i.test(trimmed) ? trimmed : `${trimmed}, monospace`;
}

/** What a search bar can offer, kept to what this app actually exposes. */
export interface FindOptions {
  caseSensitive?: boolean;
  wholeWord?: boolean;
  incremental?: boolean;
}

/** Blend two `#RRGGBB` colours; `weight` is how much of `b` is taken. */
function mixHex(a: string, b: string, weight: number): string {
  const parse = (hex: string): [number, number, number] => {
    const digits = hex.replace('#', '').trim();
    const full = digits.length === 3 ? `${digits[0]}${digits[0]}${digits[1]}${digits[1]}${digits[2]}${digits[2]}` : digits;
    return [0, 2, 4].map((at) => Number.parseInt(full.slice(at, at + 2), 16) || 0) as [number, number, number];
  };
  const one = parse(a);
  const two = parse(b);
  const channel = (index: number): string =>
    Math.round((one[index] as number) * (1 - weight) + (two[index] as number) * weight)
      .toString(16)
      .padStart(2, '0');
  return `#${channel(0)}${channel(1)}${channel(2)}`;
}

/**
 * Highlight colours for matches, taken from the theme instead of fixed.
 *
 * `--selection` is already this app's "this text is selected" colour in both
 * palettes, which is exactly what a match looks like; the active match is that
 * colour mixed towards the accent so it stands out from its neighbours.
 */
function searchDecorations(): ISearchDecorationOptions {
  const styles = getComputedStyle(document.documentElement);
  const selection = styles.getPropertyValue('--selection').trim() || '#214373';
  const accent = styles.getPropertyValue('--accent').trim() || '#4aa8ff';
  return {
    matchBackground: selection,
    matchBorder: accent,
    matchOverviewRuler: accent,
    activeMatchBackground: mixHex(selection, accent, 0.45),
    activeMatchBorder: accent,
    activeMatchColorOverviewRuler: accent,
  };
}

export interface CreateTerminalOptions {
  id: string;
  cols?: number;
  rows?: number;
  fontSize?: number;
  fontFamily?: string;
  /** What the remote writes in; UTF-8 unless the host is older than that. */
  encoding?: TerminalEncoding;
  /** Matches found, and which of them is active, as a search walks them. */
  onSearchResults?: (info: { index: number; count: number }) => void;
  /** Characters that end a word for double-click selection. */
  wordSeparator?: string;
  /** Ids of the enabled highlight sets; empty turns keyword colouring off. */
  highlight?: string[];
  /** Terminal background; empty uses the theme's own colour. */
  background?: string;
  /** The user's own highlight rules. */
  customRules?: CustomRule[];
  /**
   * Anti-idle: after this many seconds without input, send a nudge so a shell
   * with `TMOUT` set does not log the session out. 0 disables it.
   */
  idleKeepAliveSeconds?: number;
  /** The keystroke the anti-idle sends: space+backspace (default) or a line. */
  idleNudge?: IdleNudge;
  /**
   * Put the selection on the clipboard when the mouse gesture ends — the
   * MobaXterm/Xshell habit. Off means only Ctrl+Shift+C copies.
   */
  copyOnSelect?: boolean;
  /**
   * Draw block and box-drawing characters edge to edge across the whole cell
   * (xterm's `customGlyphs`). Off unless asked — see the note where it is set.
   */
  continuousGlyphs?: boolean;
  /**
   * Where clipboard pastes go. Without it xterm writes them straight to the
   * PTY; with it the app can review a multi-line block before it runs.
   */
  onPaste?: (text: string) => void;
  scrollback?: number;
  theme?: ThemeName;
  /** Remote is a Windows shell (cmd/PowerShell) — changes trzsz framing. */
  isWindowsShell?: boolean;
  onSend: (bytes: Uint8Array) => void;
  onResize: (cols: number, rows: number) => void;
  onTitle?: (title: string) => void;
  onLink?: (url: string) => void;
  onNotice?: (level: 'info' | 'warn' | 'error', message: string, sticky?: boolean) => void;
}

export interface TerminalBridge {
  readonly id: string;
  readonly el: HTMLDivElement;
  readonly term: Terminal;
  readonly trzsz: TrzszFilter;
  /** Attach to a container and, on first attach, open xterm. */
  attach: (container: HTMLElement) => void;
  write: (bytes: Uint8Array) => void;
  /** Re-measure the container; returns the new size when it changed. */
  fitNow: () => { cols: number; rows: number } | null;
  focus: () => void;
  setTheme: (theme: ThemeName) => void;
  setFontSize: (size: number) => void;
  setFontFamily: (family: string) => void;
  /** Re-decode what the remote sends: UTF-8, or a legacy CJK code page. */
  setEncoding: (encoding: TerminalEncoding) => void;
  /** Find in the scrollback; true when something matched. */
  findNext: (query: string, options?: FindOptions) => boolean;
  findPrevious: (query: string, options?: FindOptions) => boolean;
  /** Drop the highlights a search left behind. */
  clearSearch: () => void;
  setWordSeparator: (separator: string) => void;
  setHighlight: (setIds: string[]) => void;
  setBackground: (colour: string) => void;
  /** Returns the patterns that would not compile. */
  setCustomRules: (rules: CustomRule[]) => string[];
  /** Anti-idle interval in seconds; 0 turns it off. */
  setIdleKeepAlive: (seconds: number) => void;
  /** What the anti-idle sends: a space+backspace pair, or a completed line. */
  setIdleNudge: (mode: IdleNudge) => void;
  /** Copy a finished mouse selection to the clipboard, or stop doing that. */
  setCopyOnSelect: (on: boolean) => void;
  /**
   * Switch between continuous TUI frames (blocks fill the cell) and separated
   * stacked bars (the font keeps the line-height padding). Takes effect at once.
   */
  setContinuousGlyphs: (on: boolean) => void;
  copySelection: () => string;
  pasteText: (text: string) => void;
  clear: () => void;
  /** Files dropped on the terminal, straight to trzsz. */
  uploadDropped: (items: DataTransferItemList) => void;
  /**
   * Start an upload the way `trz` on the server does: ask for the files through
   * the native picker and stream them. Returns false when a transfer is running.
   */
  requestUpload: () => boolean;
  dispose: () => void;
}

export function createTerminalBridge(options: CreateTerminalOptions): TerminalBridge {
  let currentTheme: ThemeName = options.theme ?? 'dark';
  let customBackground = options.background ?? '';
  // Copy-on-select is per terminal, so the setting is applied to every open tab
  // as well as to the ones created later.
  let copyOnSelect = options.copyOnSelect ?? false;
  // Whether xterm paints block/box glyphs itself. Off by default: see the note
  // on `customGlyphs` below.
  const continuousGlyphs = options.continuousGlyphs ?? false;
  // Set by a press inside the pane: dragging past its edge is how long lines get
  // selected, and the release then happens over some other element.
  let pressedInPane = false;
  let stopMouseHooks: (() => void) | null = null;
  // A custom background is a *pane* colour: the palette follows the pane's own
  // brightness (see panePalette), so the text is never painted onto a pane it
  // cannot be read on.
  const themeFor = (name: ThemeName, background: string): ITheme => panePalette(name, background);
  const term = new Terminal({
    cols: options.cols ?? 100,
    rows: options.rows ?? 30,
    cursorBlink: true,
    cursorStyle: 'bar',
    fontFamily: fontStack(options.fontFamily ?? ''),
    fontSize: options.fontSize ?? 14,
    wordSeparator: options.wordSeparator ?? DEFAULT_WORD_SEPARATOR,
    lineHeight: 1.15,
    /*
     * xterm can draw block (U+2580-U+259F) and box-drawing characters itself
     * instead of using the font, and it draws them across the *whole* cell -
     * including the 15% line-height padding above. That is what turns a stacked
     * bar chart into one solid mass: `dust` prints one row of blocks per
     * directory, and with no padding left between rows the bars fuse into a
     * single staircase. Measured against xterm 6 with the WebGL renderer (the
     * one loaded below): with xterm's default `true` eight dust rows rendered as
     * one shape; with `false` the font keeps the padding and every row reads as
     * its own bar, which is what every other terminal does. The trade-off points
     * the other way for box drawing - a `|` from the font does not span the
     * padding either, so a TUI's vertical frame lines pick up a hairline gap.
     * That is a taste call, so it is a setting rather than a fixed choice.
     */
    customGlyphs: continuousGlyphs,
    scrollback: options.scrollback ?? 8000,
    allowProposedApi: true,
    logLevel: 'warn',
    theme: themeFor(currentTheme, customBackground),
  });

  // What the remote writes in. UTF-8 hands xterm the bytes untouched; anything else
  // is decoded here, streaming, so a character split across two chunks survives.
  const remote = new RemoteTextDecoder(options.encoding ?? 'utf-8');

  const el = document.createElement('div');
  el.className = 'terminal-host';
  el.dataset.terminal = options.id;

  const fit = new FitAddon();
  term.loadAddon(fit);
  term.loadAddon(new UnicodeGraphemesAddon());
  term.loadAddon(
    new WebLinksAddon((_event, uri) => {
      if (options.onLink) options.onLink(uri);
      else window.open(uri, '_blank', 'noopener');
    }),
  );

  /**
   * Search over the buffer — the scrollback too, which is the difference between
   * "find" and "look up". Every match is decorated so the count in the bar means
   * something, and the decorations are re-read per search so a theme switch while
   * the bar is open does not paint light-theme colours on a dark terminal.
   */
  const findOptions = (find: FindOptions = {}): ISearchOptions => ({
    caseSensitive: find.caseSensitive ?? false,
    wholeWord: find.wholeWord ?? false,
    incremental: find.incremental ?? true,
    decorations: searchDecorations(),
  });

  const search = new SearchAddon();
  term.loadAddon(search);
  search.onDidChangeResults((event) => {
    // `resultIndex` is -1 when the *selected* match is not in the list the addon
    // reports: it stops collecting at `highlightLimit` (1000 by default), so a
    // search with more matches than that would otherwise print `0 / 1000` for a
    // match that plainly exists. The bar tells the two cases apart.
    options.onSearchResults?.({ index: Math.max(event.resultIndex + 1, 0), count: event.resultCount });
  });

  // A keystroke the app acts on must not also reach the remote shell as a
  // control character: without this, Ctrl+Shift+W would rub out a word *and*
  // close the tab, and Ctrl+Shift+B would send a stray \x02 to tmux. Returning
  // false tells xterm to leave the event alone (keydown and keyup alike).
  term.attachCustomKeyEventHandler((event) => !appOwnsKey(event));

  // Keyword colouring. One instance per terminal: it tracks this stream's SGR
  // state and whether an alternate-screen app is in charge.
  const highlighter = new OutputHighlighter(options.highlight ?? []);
  highlighter.setCustomRules(options.customRules ?? []);

  // trzsz-js was written for a host with Node's process object (see
  // bindTrzszStdout); the terminal is where its closing words belong.
  bindTrzszStdout(options.id, (text) => term.write(text));

  const trzsz = new TrzszFilter({
    terminalColumns: term.cols,
    isWindowsShell: options.isWindowsShell ?? false,
    // Uploads read through the Go bridge; 1 MiB keeps one IPC message (and its
    // base64 expansion) modest without adding many round trips.
    maxDataChunkSize: 1 << 20,
    writeToTerminal: (output) => {
      // Strings arrive from the progress bar and the transfer protocol; file
      // payloads arrive as bytes. Neither is rewritten — the compatibility
      // banner id is stripped on the way to the screen, and keyword colouring
      // runs only when no transfer is in flight (file traffic has no words).
      const paint = (bytes: Uint8Array) => {
        const chunk = remote.decode(trzsz.isTransferringFiles() ? bytes : highlighter.process(bytes));
        if (chunk.length) term.write(chunk);
      };
      if (typeof output === 'string') {
        // Our own words (the transfer's clatter), not the remote's: already text, so
        // the remote's encoding must not be applied to it.
        term.write(displayBannerBytes(output) as string);
        return;
      }
      if (output instanceof Uint8Array) {
        paint(displayBannerBytes(output) as Uint8Array);
        return;
      }
      if (output instanceof ArrayBuffer) {
        paint(displayBannerBytes(new Uint8Array(output)) as Uint8Array);
        return;
      }
      output
        .arrayBuffer()
        .then((buffer) => paint(displayBannerBytes(new Uint8Array(buffer)) as Uint8Array))
        .catch(() => undefined);
    },
    sendToServer: (input) => {
      if (typeof input === 'string' && (input.startsWith('#FAIL:') || input.startsWith('#fail:'))) {
        void decodeFailFrame(input).then((message) => {
          if (!message) return;
          // Cancelling a transfer is not a failure, and a crash inside the
          // remote trzsz binary is already printed on the same PTY. Neither
          // belongs in a toast; a real failure still gets said out loud.
          if (isTransferCancel(message) || isRemoteStackDump(message) || isLibraryNoise(message)) return;
          options.onNotice?.('error', `Transfer failed: ${message}`);
        });
      }
      options.onSend(toSendBytes(input));
    },
  });

  // Everything from the server goes through this stream: bypassing it is what
  // made `trz` / `tsz` print a banner and do nothing.
  const output = new ServerOutputStream(trzsz, (bytes, error) => {
    // Bytes the filter would not take, still the remote's: same decoding as above.
    const chunk = remote.decode(bytes);
    if (chunk.length) term.write(chunk);
    const message = error instanceof Error ? error.message : String(error);
    // The bytes still went through — the stream never eats output — but a
    // stopped transfer's complaint is not worth interrupting the user for.
    if (!isTransferCancel(message)) options.onNotice?.('warn', `trzsz filter: ${message}`);
  });

  // lrzsz (`rz` / `sz`) for hosts without trzsz. It sits *in front* of the
  // terminal: every server byte is offered to it first, and the bytes that are
  // not ZMODEM traffic come back through to_terminal into the stream above —
  // which means trzsz, the highlighter and xterm all still see them.
  const zmodem = new ZmodemPipe({
    toTerminal: (bytes) => output.write(bytes),
    // The progress bar is our text, in UTF-8, and must not go through the remote's
    // decoder: a GBK host would turn its `█` into mojibake.
    toTerminalText: (text) => term.write(text),
    toServer: (bytes) => options.onSend(bytes),
    chooseDownloadTarget,
    chooseUploads,
    onNotice: (level, text) => options.onNotice?.(level, text),
    // The ZMODEM progress line is drawn into the terminal, so it needs the pane's width.
    columns: () => term.cols,
  });

  const disposables: { dispose: () => void }[] = [];
  disposables.push({
    dispose: () => {
      stopObserver?.();
      stopObserver = null;
    },
  });

  // The GPU renderer is worth it for a busy terminal, and worthless if the
  // driver drops the context — so fall back to the DOM renderer instead.
  let renderer: WebglAddon | null = null;
  try {
    renderer = new WebglAddon(true);
    renderer.onContextLoss(() => {
      renderer?.dispose();
      renderer = null;
      options.onNotice?.('warn', 'GPU terminal renderer lost; using the software renderer');
    });
    term.loadAddon(renderer);
  } catch {
    renderer = null;
  }

  // A ZMODEM session that owns the stream swallows keystrokes — typing would land
  // inside a packet and corrupt the file. That is invisible unless it is said once,
  // and it has to stop being said when the session ends, or every later Ctrl-C
  // would echo the same advice.
  let zmodemInputNoted = false;
  const droppedForZmodem = (): void => {
    if (zmodemInputNoted) return;
    zmodemInputNoted = true;
    options.onNotice?.(
      'info',
      'A ZMODEM transfer owns this session, so typing is set aside until it ends.',
    );
  };

  term.onData((data) => {
    if (zmodem.active()) {
      droppedForZmodem();
      return;
    }
    zmodemInputNoted = false;
    noteInput(data);
    trzsz.processTerminalInput(data);
  });
  term.onBinary((data) => {
    if (zmodem.active()) {
      droppedForZmodem();
      return;
    }
    zmodemInputNoted = false;
    trzsz.processBinaryInput(data);
  });
  term.onResize(({ cols, rows }) => {
    trzsz.setTerminalColumns(cols);
    options.onResize(cols, rows);
  });
  term.onTitleChange((title) => options.onTitle?.(title));

  let opened = false;

  // --- anti-idle ----------------------------------------------------------
  //
  // SSH keepalives keep the *transport* alive and never reach the shell, so a
  // login shell with `TMOUT` set still logs itself out — the "timed out waiting
  // for input: auto logout" that reads like "the keepalive is broken".
  //
  // The nudge is a space and a backspace by default: two keystrokes that cancel
  // each other out. Nothing is submitted, nothing can run, and the user's line
  // is exactly as they left it — which also means it is safe to send while they
  // have half a command typed (a `>` continuation prompt is exactly that case,
  // and it is where an Enter-only nudge used to give up and let the session die).
  // A completed line stays available for shells whose timer only resets on one.
  //
  // What it will not do:
  //
  //   * never in a full-screen program, where a space is a keypress that means
  //     something (a page in less, an indent in vim);
  //   * never during a transfer;
  //   * not in the middle of an output burst — five seconds of quiet first, so a
  //     keystroke never lands while a program is halfway through printing;
  //   * in `line` mode, never while there is unsubmitted input, because an Enter
  //     would run it;
  //   * and it stops for the session if one nudge makes the far side reprint a
  //     screenful — a bastion's asset menu does that, and screen spam is worse
  //     than the timeout it was meant to prevent.
  let idleSeconds = options.idleKeepAliveSeconds ?? 0;
  let nudge: IdleNudge = options.idleNudge ?? 'space';
  let pendingInput = false;
  let lastInputAt = Date.now();
  let lastOutputAt = Date.now();
  let idleTimer: ReturnType<typeof setInterval> | null = null;
  let idlePingNoted = false;
  let idleGaveUp = false;

  /** Output has to have stopped this long before a nudge is allowed to land. */
  const QUIET_MS = 5_000;

  // What the last nudge printed: one line is a prompt, a screenful is a menu.
  let measuringNudge = false;
  let nudgeNewlines = 0;
  let nudgeDeadline = 0;
  const NUDGE_WINDOW_MS = 2_000;
  const NUDGE_TOO_MUCH = 5;

  const noteInput = (data: string): void => {
    lastInputAt = Date.now();
    for (const ch of data) {
      // Enter, Ctrl-C and Ctrl-D leave the line empty; anything else means there
      // is text an Enter would submit.
      pendingInput = !'\r\n\x03\x04'.includes(ch);
      // A completed line means a person is here, at a real prompt — wherever the
      // nudge that made a mess landed, that is over now.
      if (idleGaveUp && (ch === '\r' || ch === '\n')) {
        idleGaveUp = false;
        startIdleTimer();
      }
    }
  };

  /** Every remote byte goes through here: it proves the session is alive. */
  const noteOutput = (bytes: Uint8Array): void => {
    lastOutputAt = Date.now();
    if (!measuringNudge) return;
    if (Date.now() > nudgeDeadline) {
      measuringNudge = false;
      return;
    }
    for (const byte of bytes) {
      if (byte === 0x0a) nudgeNewlines += 1;
    }
    if (nudgeNewlines < NUDGE_TOO_MUCH) return;
    measuringNudge = false;
    idleGaveUp = true;
    stopIdleTimer();
    options.onNotice?.(
      'warn',
      'Anti-idle stopped for this session: one nudge made the far side reprint a screenful '
        + '(a menu or a pager). That is worse than the timeout it was meant to prevent.',
      true,
    );
  };

  const stopIdleTimer = (): void => {
    if (idleTimer !== null) clearInterval(idleTimer);
    idleTimer = null;
  };

  const startIdleTimer = (): void => {
    stopIdleTimer();
    if (idleSeconds <= 0 || idleGaveUp) return;
    // Check often enough to be punctual, rarely enough to be free.
    const tick = Math.max(5000, Math.min(idleSeconds * 1000, 60_000));
    idleTimer = setInterval(() => {
      if (idleSeconds <= 0 || idleGaveUp) return;
      if (trzsz.isTransferringFiles() || zmodem.active()) return;
      // A full-screen program owns the keyboard, and typing into it is how people
      // lose work (a space pages `less`, selects in `fzf`, inserts in vim). The one
      // exception is the mode that exists to type into such a program on purpose:
      // `space-only` is opt-in and says so in Settings.
      if (highlighter.altScreenActive && nudge !== 'space-only') return;
      // A space that is immediately rubbed out cannot run anything, so the
      // half-typed line is no obstacle to it; an Enter is the opposite.
      if (nudge === 'line' && pendingInput) return;
      const now = Date.now();
      if (now - lastInputAt < idleSeconds * 1000) return;
      if (now - lastOutputAt < QUIET_MS) return;
      lastInputAt = now;
      options.onSend(nudgeBytes(nudge));
      measuringNudge = true;
      nudgeNewlines = 0;
      nudgeDeadline = now + NUDGE_WINDOW_MS;
      if (!idlePingNoted) {
        idlePingNoted = true;
        options.onNotice?.(
          'info',
          nudge === 'space'
            ? `Anti-idle: sending a space and a backspace after ${Math.round(idleSeconds / 60)} min of silence — `
              + 'the line stays exactly as you left it, so nothing can run. '
              + 'Settings → Idle sessions can send an empty line instead, for a shell that insists on one.'
            : nudge === 'space-only'
              ? `Anti-idle: sending a space after ${Math.round(idleSeconds / 60)} min of silence. `
                + 'It is a real keystroke, so it works inside a TUI — and it stays in whatever input box '
                + 'has focus. Settings → Idle sessions changes this.'
              : `Anti-idle: submitting an empty line after ${Math.round(idleSeconds / 60)} min of silence, so the `
                + "shell's own timeout does not end the session.",
        );
      }
    }, tick);
  };

  startIdleTimer();
  let stopObserver: (() => void) | null = null;
  let frame = 0;

  const fitSoon = () => {
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      bridge.fitNow();
    });
  };

  const bridge: TerminalBridge = {
    id: options.id,
    el,
    term,
    trzsz,

    attach(container: HTMLElement) {
      if (el.parentElement !== container) container.appendChild(el);
      if (!opened) {
        opened = true;
        term.open(el);
        if (window.ResizeObserver) {
          const observer = new ResizeObserver(() => fitSoon());
          observer.observe(el);
          stopObserver = () => observer.disconnect();
        }
      }
      fitSoon();
      requestAnimationFrame(() => term.focus());
    },

    write(bytes: Uint8Array) {
      // Server bytes are arriving in this pane, so it is the one trzsz's
      // stdout belongs to.
      bindTrzszStdout(options.id, (text) => term.write(text));
      noteOutput(bytes);
      zmodem.consume(bytes);
    },

    fitNow() {
      if (!opened) return null;
      const rect = el.getBoundingClientRect();
      // Hidden tabs have no size; proposing 0x0 here would wipe the remote
      // window size, so leave it alone until the tab becomes visible again.
      if (rect.width < 20 || rect.height < 20) return null;
      const before = { cols: term.cols, rows: term.rows };
      try {
        fit.fit();
      } catch {
        return null;
      }
      if (term.cols === before.cols && term.rows === before.rows) return null;
      return { cols: term.cols, rows: term.rows };
    },

    focus() {
      term.focus();
    },

    setTheme(next: ThemeName) {
      currentTheme = next;
      // The custom background survives a theme switch: it was chosen on purpose.
      term.options.theme = themeFor(next, customBackground);
    },

    setBackground(colour: string) {
      customBackground = colour;
      term.options.theme = themeFor(currentTheme, customBackground);
    },

    setFontSize(size: number) {
      const clamped = Math.min(28, Math.max(9, Math.round(size)));
      term.options.fontSize = clamped;
      fitSoon();
    },

    setFontFamily(family: string) {
      // A family that is not installed falls back silently in CSS, which is why
      // the stack always ends in `monospace`.
      term.options.fontFamily = fontStack(family);
      fitSoon();
    },

    setEncoding(encoding: TerminalEncoding) {
      remote.setEncoding(encoding);
    },

    findNext(query: string, find: FindOptions = {}) {
      if (!query) {
        search.clearDecorations();
        return false;
      }
      return search.findNext(query, findOptions(find));
    },

    findPrevious(query: string, find: FindOptions = {}) {
      if (!query) {
        search.clearDecorations();
        return false;
      }
      // `incremental` only means anything to findNext; walking backwards from a
      // half-typed word would keep stretching the selection instead of stepping.
      return search.findPrevious(query, { ...findOptions(find), incremental: false });
    },

    clearSearch() {
      search.clearDecorations();
    },

    setWordSeparator(separator: string) {
      term.options.wordSeparator = separator;
    },

    setHighlight(setIds: string[]) {
      highlighter.setSets(setIds);
    },

    setCustomRules(rules: CustomRule[]) {
      return highlighter.setCustomRules(rules);
    },

    setIdleKeepAlive(seconds: number) {
      idleSeconds = Number.isFinite(seconds) && seconds > 0 ? seconds : 0;
      lastInputAt = Date.now();
      lastOutputAt = Date.now();
      startIdleTimer();
    },

    setIdleNudge(mode: IdleNudge) {
      nudge = mode;
      // Say what the next nudge will be, the first time it happens.
      idlePingNoted = false;
    },

    setCopyOnSelect(on: boolean) {
      copyOnSelect = on;
    },

    setContinuousGlyphs(on: boolean) {
      term.options.customGlyphs = on;
      // Glyph atlases are cached per option state; redraw so the change shows on
      // the screen the user is looking at, not at the next resize.
      term.refresh(0, term.rows - 1);
    },

    copySelection() {
      const text = term.hasSelection() ? term.getSelection() : '';
      if (text) void navigator.clipboard?.writeText(text).catch(() => undefined);
      return text;
    },

    pasteText(text: string) {
      // paste() honours bracketed paste mode, so multi-line pastes stay safe.
      term.paste(text);
    },

    clear() {
      term.write('\x1b[3J\x1b[H\x1b[2J');
      term.clear();
      highlighter.reset();
    },

    requestUpload() {
      if (zmodem.active()) {
        options.onNotice?.('info', 'A ZMODEM transfer owns this session; wait for it to finish.');
        return false;
      }
      if (trzsz.isTransferringFiles()) {
        options.onNotice?.('info', 'A transfer is already running.');
        return false;
      }
      // Type `trz` on the *server's* behalf and let trzsz's own banner handler
      // take over: it opens the native picker (the fsaccess shim) and streams
      // the files. Synthesising a DataTransferItemList from a file input looked
      // tidier and never worked — items added by script carry no filesystem
      // entry, and `webkitGetAsEntry()` is exactly what trzsz reads, so it
      // reported "No files to upload" after the user had picked real files.
      trzsz.processTerminalInput('trz\r');
      return true;
    },

    uploadDropped(items: DataTransferItemList) {
      if (zmodem.active()) {
        // Dropping files starts a trzsz upload, and `trz` typed into the middle
        // of a ZMODEM packet is a corrupted file, not a second transfer.
        options.onNotice?.('info', 'A ZMODEM transfer owns this session; wait for it to finish.');
        return;
      }
      trzsz
        .uploadFiles(items)
        .then(() => options.onNotice?.('info', 'Upload finished'))
        .catch((error: unknown) => {
          const message = error instanceof Error ? error.message : String(error);
          // trzsz rejects when the user cancels or the server never answers.
          // A cancel means the user already knows: say nothing.
          if (isTransferCancel(message)) return;
          options.onNotice?.('warn', message);
        });
    },

    dispose() {
      stopIdleTimer();
      stopMouseHooks?.();
      stopMouseHooks = null;
      // Only let go of stdout if this pane still owns it.
      if (stdoutSink?.id === options.id) stdoutSink = null;
      if (frame) cancelAnimationFrame(frame);
      frame = 0;
      // A closed tab must not leave `sz` waiting on a client that has gone away.
      zmodem.dispose();
      output.dispose();
      for (const item of disposables) {
        try {
          item.dispose();
        } catch {
          /* a half-built terminal is already gone */
        }
      }
      renderer?.dispose();
      fit.dispose();
      term.dispose();
      el.remove();
    },
  };

  // Drag a file onto the terminal and it uploads — no "trz" to remember.
  el.addEventListener('dragover', (event) => {
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy';
    el.classList.add('is-drop-target');
  });
  el.addEventListener('dragleave', () => el.classList.remove('is-drop-target'));
  el.addEventListener('drop', (event) => {
    event.preventDefault();
    el.classList.remove('is-drop-target');
    const items = event.dataTransfer?.items;
    if (items && items.length > 0) bridge.uploadDropped(items);
  });
  el.addEventListener('focusin', () => el.classList.add('is-focused'));
  el.addEventListener('focusout', () => el.classList.remove('is-focused'));

  // --- copy on select --------------------------------------------------------
  //
  // The gesture decides, not the clipboard: a selection that appears because a
  // program repainted is not an invitation to overwrite what was copied
  // elsewhere. So this fires on mouse release, and only for a press that began
  // in this pane.
  const copySelectionNow = (): void => {
    if (!copyOnSelect) return;
    const text = term.hasSelection() ? term.getSelection() : '';
    if (!text) return;
    void writeClipboard(text).then((ok) => {
      if (!ok) options.onNotice?.('warn', 'Select-to-copy could not reach the clipboard.');
    });
  };

  const onMouseDown = (): void => {
    pressedInPane = true;
  };
  const onMouseUp = (): void => {
    const started = pressedInPane;
    pressedInPane = false;
    if (started) copySelectionNow();
  };

  el.addEventListener('mousedown', onMouseDown);
  window.addEventListener('mouseup', onMouseUp);
  stopMouseHooks = () => {
    el.removeEventListener('mousedown', onMouseDown);
    window.removeEventListener('mouseup', onMouseUp);
  };

  // --- paste -----------------------------------------------------------------
  //
  // Taken over completely: xterm would otherwise hand a pasted block straight to
  // the PTY, and a multi-line block has to be shown to the user first. Capture
  // phase plus stopPropagation means xterm's own handler never sees the event.
  const onNativePaste = (event: Event): void => {
    const paste = event as ClipboardEvent;
    paste.preventDefault();
    paste.stopPropagation();
    const text = paste.clipboardData?.getData('text/plain') ?? '';
    if (!text) return;
    if (options.onPaste) options.onPaste(text);
    else term.paste(text);
  };
  el.addEventListener('paste', onNativePaste, true);

  return bridge;
}
