/*
 * Application state: tabs, saved hosts, notices, settings — plus the session
 * plumbing that ties a tab to a Go-side SSH session and an xterm instance.
 *
 * xterm objects are deliberately kept OUT of the reactive graph (a Terminal is
 * enormous and Vue would walk it), so bridges live in a plain Map and only
 * their status is reactive.
 */
import { computed, nextTick, reactive, watch } from 'vue';

import { backend, errorMessage, fileBridgeApi } from './backend';
import { bytesToBase64, formatBytes } from './bytes';
import { readClipboard, writeClipboard } from './clipboard';
import { bridgeAvailable, bridgeError, decodeData, initBridge, onClosedEvent, onDataEvent, onNoticeEvent } from './events';
import type { AppInfo, Host, HostExport, HostImport, HostKeyRecord, NoticeAction, NoticeEvent, NoticeLevel, ProxyConfig } from './protocol';
import { COLOUR_SGR, DEFAULT_HIGHLIGHT_SETS } from './highlight';
import type { CustomRule } from './highlight';
import type { IdleNudge, TerminalBridge, ThemeName } from './terminal';
import { installFilePickers, pickerMode, type PickerMode } from './fsaccess';
import { isMultilinePaste, lineCount, trimLeadingNewlines, trimTrailingBlankLines } from './paste';
import { DEFAULT_WORD_SEPARATOR, createTerminalBridge, panePalette } from './terminal';
import { isTerminalEncoding } from './encoding';
import type { TerminalEncoding } from './encoding';

export type TabStatus = 'connecting' | 'open' | 'closed' | 'error';

export interface Tab {
  id: string;
  title: string;
  target: string;
  hostId?: string;
  hostName?: string;
  sessionId?: string;
  status: TabStatus;
  detail?: string;
  serverVersion?: string;
  fingerprint?: string;
  /** Jump host this session tunnels through, when it is not a direct one. */
  via?: string;
  bytesIn: number;
  bytesOut: number;
  startedAt: number;
}

export interface Notice {
  id: number;
  level: NoticeLevel;
  message: string;
  at: number;
  sticky: boolean;
  /** Something the user can do about it, when the backend names one. */
  action?: NoticeAction;
}

interface Settings {
  /** Layout version, so a new built-in set can reach users who saved settings. */
  settingsVersion: number;
  theme: ThemeName;
  fontSize: number;
  /** Font family for the terminal; empty means the built-in monospace stack. */
  fontFamily: string;
  /**
   * What the remote writes in. UTF-8 unless the host is older than that: a
   * `zh_CN.GBK` box sends GBK, and xterm decodes UTF-8 only, so those bytes have to
   * be translated on the way in or every Chinese character is mojibake.
   */
  encoding: TerminalEncoding;
  /** Characters that end a word for double-click selection. */
  wordSeparator: string;
  /** Ids of the enabled keyword-highlight sets; empty means off. */
  highlight: string[];
  /** Terminal background (`#rrggbb`); empty means the theme's own colour. */
  background: string;
  /** The user's own keywords and patterns, on top of the built-in sets. */
  customRules: CustomRule[];
  /**
   * Minutes of inactivity after which the anti-idle nudges the session so a
   * shell with `TMOUT` set keeps it. 0 turns it off.
   */
  idleKeepAliveMinutes: number;
  /**
   * What that nudge is: a space and a backspace (invisible, cannot run
   * anything — the default) or a completed empty line.
   */
  idleNudge: IdleNudge;
  /**
   * Hard cap on one connection, in hours. Go enforces it from the moment the
   * connection is made and clamps it to 1–24, so it is a limit rather than a
   * suggestion: the keepalive cannot hold a login open past it, and a duplicated
   * tab shares the remaining time instead of buying a new window.
   */
  sessionHours: number;
  /**
   * Put a finished mouse selection straight on the clipboard. Off means only
   * Ctrl+Shift+C copies.
   */
  copyOnSelect: boolean;
  /**
   * Fill the whole cell with block and box-drawing characters.
   *
   * On: a TUI's frame lines meet exactly, but stacked bar charts (`dust`, a
   * progress meter) fuse into one solid shape because nothing is left between
   * the rows. Off (the default): every row of blocks keeps the line-height
   * padding, so bar charts read as bars and frame lines get a hairline gap.
   */
  continuousGlyphs: boolean;
}

const SETTINGS_KEY = 'goterm.settings';
/** Bumped when the meaning of the stored settings changes. */
const SETTINGS_VERSION = 2;

function defaultSettings(): Settings {
  return {
    settingsVersion: SETTINGS_VERSION,
    theme: 'dark',
    fontSize: 14,
    fontFamily: '',
    encoding: 'utf-8',
    wordSeparator: DEFAULT_WORD_SEPARATOR,
    highlight: [...DEFAULT_HIGHLIGHT_SETS],
    background: '',
    customRules: [],
    idleKeepAliveMinutes: 0,
    idleNudge: 'space',
    sessionHours: 12,
    copyOnSelect: true,
    continuousGlyphs: false,
  };
}

const COLOUR_NAMES = new Set(Object.keys(COLOUR_SGR));

/** Minutes, clamped to something a human might mean (0 = off). */
function clampMinutes(value: unknown): number {
  const minutes = typeof value === 'number' && Number.isFinite(value) ? Math.round(value) : 0;
  return Math.min(120, Math.max(0, minutes));
}

/**
 * Hours a connection may stay open, clamped to Go's bounds.
 *
 * There is no "off": the cap exists so a keepalive cannot turn into an endless
 * login, and a missing value means the 12-hour default rather than unlimited. Go
 * clamps independently, so this is only about showing the user a true number.
 */
const SESSION_HOURS_MIN = 1;
const SESSION_HOURS_MAX = 24;
const SESSION_HOURS_DEFAULT = 12;

function clampSessionHours(value: unknown): number {
  // Zero and negatives mean "unset", not "an hour": the same reading Go gives
  // them, so a hand-edited file cannot end up with a tighter cap than the person
  // who wrote it intended.
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) return SESSION_HOURS_DEFAULT;
  return Math.min(SESSION_HOURS_MAX, Math.max(SESSION_HOURS_MIN, Math.round(value)));
}

/**
 * The three nudges, and nothing else. A stored value nobody recognises falls
 * back to the safest one rather than to whichever one happens to be checked last.
 * Declared up here because `loadSettings()` runs while `state` is being built.
 */
const IDLE_NUDGES = new Set<string>(['space', 'line', 'space-only']);

function toIdleNudge(value: unknown): IdleNudge {
  return typeof value === 'string' && IDLE_NUDGES.has(value) ? (value as IdleNudge) : 'space';
}

/** Accept only rules that make sense, so a hand-edited localStorage cannot hurt. */
function sanitiseRules(value: unknown): CustomRule[] {
  if (!Array.isArray(value)) return [];
  const out: CustomRule[] = [];
  for (const raw of value) {
    if (!raw || typeof raw !== 'object') continue;
    const rule = raw as Partial<CustomRule>;
    if (typeof rule.pattern !== 'string' || !rule.pattern.trim()) continue;
    out.push({
      id: typeof rule.id === 'string' ? rule.id : `custom-${out.length}`,
      pattern: rule.pattern,
      colour: (typeof rule.colour === 'string' && COLOUR_NAMES.has(rule.colour) ? rule.colour : 'yellow') as CustomRule['colour'],
      regex: Boolean(rule.regex),
      ignoreCase: rule.ignoreCase !== false,
      enabled: rule.enabled !== false,
    });
  }
  return out;
}
const bridges = new Map<string, TerminalBridge>();
/** Output that arrived before its tab had a terminal to write into. */
const pendingOutput = new Map<string, Uint8Array[]>();
const PENDING_LIMIT = 4 * 1024 * 1024;

function loadSettings(): Settings {
  const fallback = defaultSettings();
  try {
    const raw = localStorage.getItem(SETTINGS_KEY);
    if (!raw) return fallback;
    const parsed = JSON.parse(raw) as Partial<Settings>;
    const highlight = Array.isArray(parsed.highlight)
      ? parsed.highlight.filter((id): id is string => typeof id === 'string')
      : [...fallback.highlight];
    // v2 added the data-shape set. Someone who had already chosen their sets
    // never saw it, and it colours things the other sets do not claim — so it is
    // switched on once, for them, instead of waiting to be discovered.
    if ((parsed.settingsVersion ?? 1) < 2 && !highlight.includes('data')) highlight.push('data');
    return {
      settingsVersion: SETTINGS_VERSION,
      theme: parsed.theme === 'light' ? 'light' : 'dark',
      fontSize:
        typeof parsed.fontSize === 'number' ? Math.min(28, Math.max(9, parsed.fontSize)) : fallback.fontSize,
      fontFamily: typeof parsed.fontFamily === 'string' ? parsed.fontFamily : fallback.fontFamily,
    encoding: isTerminalEncoding(parsed.encoding) ? parsed.encoding : fallback.encoding,
      // An empty separator is a legitimate choice? No: xterm would then treat the
      // whole line as one word, and no user asks for that on purpose.
      wordSeparator:
        typeof parsed.wordSeparator === 'string' && parsed.wordSeparator.length > 0
          ? parsed.wordSeparator
          : fallback.wordSeparator,
      highlight,
      background: typeof parsed.background === 'string' && /^#[0-9a-f]{6}$/i.test(parsed.background) ? parsed.background : '',
      customRules: sanitiseRules(parsed.customRules),
      idleKeepAliveMinutes: clampMinutes(parsed.idleKeepAliveMinutes),
      idleNudge: toIdleNudge(parsed.idleNudge),
      sessionHours: clampSessionHours(parsed.sessionHours),
      copyOnSelect: typeof parsed.copyOnSelect === 'boolean' ? parsed.copyOnSelect : fallback.copyOnSelect,
    continuousGlyphs:
      typeof parsed.continuousGlyphs === 'boolean' ? parsed.continuousGlyphs : fallback.continuousGlyphs,
    };
  } catch {
    return fallback;
  }
}

const saved = loadSettings();

export const state = reactive({
  tabs: [] as Tab[],
  activeTabId: null as string | null,
  hosts: [] as Host[],
  knownHosts: [] as HostKeyRecord[],
  info: null as AppInfo | null,
  notices: [] as Notice[],
  sidebarOpen: true,
  busy: false,
  bridgeReady: false,
  filePickerMode: 'none' as PickerMode,
  settings: saved,
});

let noticeSeq = 0;
let tabSeq = 0;

export const activeTab = computed<Tab | undefined>(() =>
  state.tabs.find((tab) => tab.id === state.activeTabId),
);

export const groupedHosts = computed(() => {
  const groups = new Map<string, Host[]>();
  for (const host of state.hosts) {
    const key = host.group?.trim() || '';
    const list = groups.get(key);
    if (list) list.push(host);
    else groups.set(key, [host]);
  }
  return [...groups.entries()]
    .sort((a, b) => a[0].localeCompare(b[0]))
    .map(([name, hosts]) => ({ name, hosts }));
});

export function setTheme(theme: ThemeName): void {
  state.settings.theme = theme;
  document.documentElement.dataset.theme = theme;
  saveSettings();
  for (const bridge of bridges.values()) bridge.setTheme(theme);
  // A custom pane colour keeps its own ink, and that ink can depend on the theme
  // (see paneTheme), so the variables have to be re-derived on a theme switch.
  applyBackground(state.settings.background);
  applyWindowTheme(theme);
}

/**
 * Colours the native title bar and window border to match the app theme.
 *
 * The frame is drawn by the OS, which only knows Windows' own light/dark
 * setting: without this a dark GoTerm on a light-mode Windows sits under a
 * white caption with a white hairline around it. Fire and forget — a frame that
 * stays system-coloured is a cosmetic miss, not a reason to disturb the user,
 * and there is no browser to call from when the UI runs alone (`npm run dev`).
 */
export function applyWindowTheme(theme: ThemeName = state.settings.theme): void {
  void backend.setWindowTheme(theme === 'dark').catch(() => {});
}

/** Persist whatever the settings screen just changed. */
export function saveSettings(): void {
  try {
    localStorage.setItem(SETTINGS_KEY, JSON.stringify(state.settings));
  } catch {
    /* private mode: settings simply do not survive the session */
  }
}

export function setFontSize(size: number): void {
  state.settings.fontSize = size;
  saveSettings();
  for (const bridge of bridges.values()) bridge.setFontSize(size);
}

export function setFontFamily(family: string): void {
  state.settings.fontFamily = family;
  saveSettings();
  for (const bridge of bridges.values()) bridge.setFontFamily(family);
}

/**
 * What the remote writes in, applied to the open tabs at once: a user who has just
 * found out that their host is GBK should not have to reconnect to see it.
 */
export function setTerminalEncoding(encoding: TerminalEncoding): void {
  if (!isTerminalEncoding(encoding)) return;
  state.settings.encoding = encoding;
  saveSettings();
  for (const bridge of bridges.values()) bridge.setEncoding(encoding);
}

export function setWordSeparator(separator: string): void {
  // An empty separator would make every line a single word, so keep the last
  // usable value instead of accepting it.
  if (!separator.length) return;
  state.settings.wordSeparator = separator;
  saveSettings();
  for (const bridge of bridges.values()) bridge.setWordSeparator(separator);
}

export function setHighlightSets(ids: string[]): void {
  state.settings.highlight = [...ids];
  saveSettings();
  for (const bridge of bridges.values()) bridge.setHighlight(ids);
}

/**
 * Set the terminal background. Empty means "the theme's own colour".
 *
 * The pane behind the canvas is painted with the same value through a CSS
 * variable, so the terminal's edge and the space around it cannot disagree.
 */
export function setBackgroundColor(colour: string): void {
  const value = /^#[0-9a-f]{6}$/i.test(colour) ? colour : '';
  state.settings.background = value;
  saveSettings();
  applyBackground(value);
  for (const bridge of bridges.values()) bridge.setBackground(value);
}

/**
 * Mirrors the terminal's own colours onto the app's terminal surfaces.
 *
 * The pane is the one thing the user can colour by hand, and the text colour has
 * to follow it: a #000000 pane under the light theme used to keep the light
 * theme's dark ink, which is black on black. `panePalette` picks the palette the
 * pane can be read on — decided by the pane alone, which is why a theme switch
 * needs nothing re-derived here — and the same choice is mirrored into
 * `--term-fg` so the settings preview (CSS, not canvas) cannot disagree with the
 * terminal next to it.
 */
export function applyBackground(colour: string): void {
  const root = document.documentElement;
  if (!colour) {
    root.style.removeProperty('--term-bg');
    root.style.removeProperty('--term-fg');
    return;
  }
  root.style.setProperty('--term-bg', colour);
  const ink = panePalette(state.settings.theme, colour).foreground;
  if (ink) root.style.setProperty('--term-fg', ink);
  else root.style.removeProperty('--term-fg');
}

/**
 * Install the user's own rules everywhere. Returns the patterns that would not
 * compile, so the settings screen can say which one is wrong.
 */
export function setCustomRules(rules: CustomRule[]): string[] {
  state.settings.customRules = rules.map((rule) => ({ ...rule }));
  saveSettings();
  const errors: string[] = [];
  for (const bridge of bridges.values()) errors.push(...bridge.setCustomRules(state.settings.customRules));
  return [...new Set(errors)];
}

/**
 * Anti-idle: nudge the session after `minutes` of inactivity. 0 turns it off.
 *
 * This is the setting that keeps a `TMOUT` shell alive; the SSH keepalive in Go
 * cannot, because it never reaches the shell. It is off by default: it writes
 * into the session, and that should be a choice.
 */
export function setIdleKeepAlive(minutes: number): void {
  state.settings.idleKeepAliveMinutes = clampMinutes(minutes);
  saveSettings();
  const seconds = state.settings.idleKeepAliveMinutes * 60;
  for (const bridge of bridges.values()) bridge.setIdleKeepAlive(seconds);
}

/**
 * Hand the current cap to Go, which is where it is actually enforced.
 *
 * Skipped when there is no bridge: a plain browser (or a headless test) has no
 * GoTerm behind it to enforce anything, and the value is still in settings.json
 * for the next real start. A failed call is equally quiet — Go's own 12-hour
 * default is already a cap, so the worst case is "the default applies until the
 * next successful push" rather than "no limit at all".
 */
function pushSessionHours(): void {
  if (!state.bridgeReady) return;
  void backend.setSessionLifetime(state.settings.sessionHours).catch(() => undefined);
}

/**
 * How long one connection may stay open, in hours (1–24).
 *
 * Deliberately has no "off": the anti-idle and the SSH keepalive exist so a
 * session does not die of someone else's idle timer, and the same machinery must
 * not become a way to keep a login alive indefinitely. Go holds the cap and
 * applies it to connections that are already up, so this call is the whole
 * setting — there is no per-session state to keep in step.
 */
export function setSessionHours(hours: number): void {
  state.settings.sessionHours = clampSessionHours(hours);
  saveSettings();
  pushSessionHours();
}

/**
 * Choose what the anti-idle sends: space+backspace (default) or an empty line.
 *
 * The space pair is the safe one — it cannot submit or run anything, and it is
 * allowed while the user has half a command typed. An empty line is the fallback
 * for a shell that only resets its timer on a finished line.
 */
export function setIdleNudge(mode: IdleNudge): void {
  state.settings.idleNudge = toIdleNudge(mode);
  saveSettings();
  for (const bridge of bridges.values()) bridge.setIdleNudge(state.settings.idleNudge);
}

/**
 * Copy a finished mouse selection to the clipboard automatically.
 *
 * On by default because that is the behaviour the request was about, and it is
 * what terminals trained people to expect. It fires on the release of a press
 * that started inside the pane — never on a selection a repaint produced.
 */
export function setCopyOnSelect(on: boolean): void {
  state.settings.copyOnSelect = on;
  saveSettings();
  for (const bridge of bridges.values()) bridge.setCopyOnSelect(on);
}

/**
 * Choose between continuous TUI frame lines and separated stacked bar charts.
 *
 * Off by default: a fused `dust` chart is unreadable, while the gap this gives
 * box-drawing lines is a hairline only some fonts and line spacings show. Applied
 * to open tabs as well as to the ones made later.
 */
export function setContinuousGlyphs(on: boolean): void {
  state.settings.continuousGlyphs = on;
  saveSettings();
  for (const bridge of bridges.values()) bridge.setContinuousGlyphs(on);
}

export function resetSettings(): void {
  const defaults = defaultSettings();
  setFontSize(defaults.fontSize);
  setFontFamily(defaults.fontFamily);
  setTerminalEncoding(defaults.encoding);
  setWordSeparator(defaults.wordSeparator);
  setHighlightSets(defaults.highlight);
  setBackgroundColor(defaults.background);
  setCustomRules(defaults.customRules);
  setIdleKeepAlive(defaults.idleKeepAliveMinutes);
  setSessionHours(defaults.sessionHours);
  setIdleNudge(defaults.idleNudge);
  setCopyOnSelect(defaults.copyOnSelect);
  setContinuousGlyphs(defaults.continuousGlyphs);
}

// --- notices ---------------------------------------------------------------

export function pushNotice(level: NoticeLevel, message: string, sticky = false, action?: string): void {
  // The same sentence again within a few seconds is an echo, not news: a failing
  // loop (a keystroke on a dead session, a nudge, a retrying probe) would
  // otherwise fill the corner with copies faster than they time out.
  const previous = state.notices[state.notices.length - 1];
  if (previous && previous.level === level && previous.message === message && Date.now() - previous.at < 4000) {
    return;
  }
  const notice: Notice = {
    id: ++noticeSeq,
    level,
    message,
    at: Date.now(),
    // An offer the user might not read within nine seconds is not an offer.
    sticky: sticky || level === 'error' || Boolean(action),
    action: action === 'enable-anti-idle' ? 'enable-anti-idle' : undefined,
  };
  state.notices.push(notice);
  while (state.notices.length > 6) state.notices.shift();
  if (!notice.sticky) {
    window.setTimeout(() => dismissNotice(notice.id), level === 'info' ? 5000 : 9000);
  }
}

let explainedProgramMouse = false;

/**
 * Once per run: why the right-click did not open GoTerm's menu, and how to get
 * either menu.
 *
 * A full-screen program that asked for the mouse owns that click — it has
 * already been sent to the app and the app draws its own menu. Saying so once
 * beats a menu that silently fails to appear, and it is where the Shift rules
 * (which come from xterm, not from us) are worth spelling out.
 */
export function explainProgramMouse(): void {
  if (explainedProgramMouse) return;
  explainedProgramMouse = true;
  pushNotice(
    'info',
    'Right-click belongs to the running program. Shift+right-click opens GoTerm’s menu; '
      + 'Shift+drag selects text (and still copies it).',
  );
}

/**
 * Turns anti-idle on now — the answer to "this shell timed itself out again".
 *
 * It uses whichever nudge the user has chosen, and that is a space and a
 * backspace unless they changed it: it cannot run anything, so it is safe to
 * switch on without reading a warning first. The notice says how to get the
 * louder empty-line nudge if the shell wants one.
 */
export function enableAntiIdle(minutes = 3): void {
  setIdleKeepAlive(minutes);
  const mode = state.settings.idleNudge;
  const what =
    mode === 'line'
      ? 'an empty line'
      : mode === 'space-only'
        ? 'a space (it stays in the input box, which is what reaches a TUI)'
        : 'a space and a backspace';
  const cost =
    mode === 'line'
      ? 'Nothing is sent while you have unsubmitted text. '
      : mode === 'space-only'
        ? 'This one types into full-screen programs too, so turn it off before using less, fzf or an editor. '
        : 'It cannot submit or run anything, and your half-typed line is left alone. ';
  pushNotice(
    'info',
    `Anti-idle on: ${what} every ${state.settings.idleKeepAliveMinutes} min while a session is idle. `
      + cost
      + 'Settings → Idle sessions changes this.',
    true,
  );
}

export function dismissNotice(id: number): void {
  const index = state.notices.findIndex((notice) => notice.id === id);
  if (index >= 0) state.notices.splice(index, 1);
}

/**
 * One notice event, from Go to the toast stack. Its own function so the wiring
 * (including `action`) can be tested without standing up the event runtime.
 */
export function applyNoticeEvent(event: NoticeEvent): void {
  pushNotice(event.level, event.message, false, event.action);
}

// --- data loading ---------------------------------------------------------

export async function loadHosts(): Promise<void> {
  try {
    state.hosts = await backend.listHosts();
  } catch (error) {
    pushNotice('error', errorMessage(error));
  }
}

export async function loadKnownHosts(): Promise<void> {
  try {
    state.knownHosts = await backend.knownHosts();
  } catch (error) {
    pushNotice('error', errorMessage(error));
  }
}

export async function loadInfo(): Promise<void> {
  try {
    state.info = await backend.info();
  } catch (error) {
    pushNotice('error', errorMessage(error));
  }
}

// --- host list portability -------------------------------------------------

/**
 * Export every host, passwords included, into one encrypted file.
 *
 * The passphrase is asked for before the save dialog opens, because the file is
 * written already sealed — there is never an instant where the plaintext
 * password sits in a file waiting to be encrypted.
 */
export async function exportHosts(): Promise<void> {
  if (!state.hosts.length) {
    pushNotice('info', 'There are no saved hosts to export yet.');
    return;
  }
  const passphrase = await askPrompt({
    title: 'Export hosts',
    hint: `${state.hosts.length} saved host(s) will be written to one file, with their passwords re-encrypted under this passphrase. Only GoTerm can read it back — and without this passphrase, neither can GoTerm.`,
    label: 'Export passphrase',
    placeholder: 'a few words you will remember',
    confirm: 'Choose file…',
    secret: true,
  });
  if (passphrase === null) return;
  if (!passphrase.trim()) {
    pushNotice('warn', 'An export passphrase is required — it is the only key to the file.');
    return;
  }

  state.busy = true;
  try {
    const report: HostExport = await backend.exportHosts(passphrase);
    if (!report.path) {
      pushNotice('info', 'Export cancelled.');
      return;
    }
    const partial = report.missing
      ? ` — ${report.missing} secret(s) could not be read and are not in the file`
      : '';
    pushNotice(
      report.missing ? 'warn' : 'info',
      `Exported ${report.hosts} host(s) and ${report.secrets} password(s) to ${report.path}${partial}`,
      true,
    );
  } catch (error) {
    pushNotice('error', `Export failed: ${errorMessage(error)}`, true);
  } finally {
    state.busy = false;
  }
}

/** Import a file written by {@link exportHosts}: same host updates, new host adds. */
export async function importHosts(): Promise<void> {
  const passphrase = await askPrompt({
    title: 'Import hosts',
    hint: 'Pick a GoTerm host export next, then enter the passphrase that was set when it was written.',
    label: 'Export passphrase',
    placeholder: 'the passphrase used at export time',
    confirm: 'Choose file…',
    secret: true,
  });
  if (passphrase === null) return;
  if (!passphrase.trim()) {
    pushNotice('warn', 'Enter the export passphrase first — the file cannot be opened without it.');
    return;
  }

  state.busy = true;
  try {
    const report: HostImport = await backend.importHosts(passphrase);
    if (!report.path) {
      pushNotice('info', 'Import cancelled.');
      return;
    }
    await loadHosts();
    const parts = [`${report.added} added`, `${report.updated} updated`];
    if (report.skipped) parts.push(`${report.skipped} skipped`);
    if (report.secrets) parts.push(`${report.secrets} password(s) restored`);
    pushNotice(report.skipped ? 'warn' : 'info', `Imported ${parts.join(', ')} from ${report.path}`, true);
  } catch (error) {
    pushNotice('error', `Import failed: ${errorMessage(error)}`, true);
  } finally {
    state.busy = false;
  }
}

// --- bootstrap ------------------------------------------------------------

export async function bootstrap(): Promise<boolean> {
  document.documentElement.dataset.theme = state.settings.theme;
  applyBackground(state.settings.background);
  // One real round trip decides whether Go is there. Everything that needs a
  // backend — the event subscriptions' usefulness, the Go-backed pickers —
  // follows from that answer instead of from sniffing injected globals.
  state.bridgeReady = await initBridge();
  // The frame waits for the answer above: with no Go there is nothing to paint
  // it, and without this the title bar would keep whatever the OS gave it.
  if (state.bridgeReady) {
    applyWindowTheme();
    // Settings Go enforces rather than renders are pushed once the bridge is known
    // to exist: the session lifetime has to be in force before the first
    // connection, or the cap would only start applying after a restart.
    pushSessionHours();
  }
  installFilePickers(state.bridgeReady ? fileBridgeApi() : null);
  state.filePickerMode = pickerMode();

  onDataEvent((event) => {
    const bytes = decodeData(event);
    if (!bytes) return;
    const tab = state.tabs.find((candidate) => candidate.sessionId === event.sessionId);
    if (!bytes.length) return;
    if (!tab) {
      stageOutput(event.sessionId, bytes);
      return;
    }
    const bridge = bridges.get(tab.id);
    if (!bridge) {
      stageOutput(event.sessionId, bytes);
      return;
    }
    tab.bytesIn += bytes.length;
    bridge.write(bytes);
  });

  onClosedEvent((event) => {
    const tab = state.tabs.find((candidate) => candidate.sessionId === event.sessionId);
    pendingOutput.delete(event.sessionId);
    if (!tab) return;
    // No toast. The tab goes grey and the reason is written into the pane right
    // where the session was — a popup for something the user can already see (and
    // that appears every time they close a tab themselves) is noise, not news.
    // markSessionGone also stops the anti-idle: a tab stays mounted after it
    // closes, so nothing else would ever stop that timer.
    markSessionGone(tab, event.reason || 'connection closed');
  });

  onNoticeEvent(applyNoticeEvent);

  if (state.bridgeReady) {
    void loadHosts();
    void loadInfo();
    void loadKnownHosts();
  } else {
    // Naming the reason turns "it does not work" into something reportable.
    const reason = bridgeError();
    pushNotice(
      'warn',
      `No Go backend${reason ? ` (${reason})` : ''}: this page is open in a plain browser, so connect will fail.`,
      true,
    );
  }
  return state.bridgeReady;
}

function stageOutput(sessionId: string, bytes: Uint8Array): void {
  const existing = pendingOutput.get(sessionId);
  const total = (existing?.reduce((sum, chunk) => sum + chunk.length, 0) ?? 0) + bytes.length;
  if (total > PENDING_LIMIT) {
    pendingOutput.set(sessionId, []);
    return;
  }
  if (existing) existing.push(bytes);
  else pendingOutput.set(sessionId, [bytes]);
}

function drainOutput(tab: Tab, bridge: TerminalBridge): void {
  if (!tab.sessionId) return;
  const queued = pendingOutput.get(tab.sessionId);
  if (!queued?.length) return;
  pendingOutput.delete(tab.sessionId);
  for (const chunk of queued) bridge.write(chunk);
}

// --- tabs -----------------------------------------------------------------

/**
 * Adds a tab and returns the *reactive proxy* for it.
 *
 * Mutating the raw object that was pushed does not notify Vue, which is how a
 * connected session once sat there labelled "connecting": the session worked,
 * but the tab's status field was written through the raw object.
 */
function pushTab(raw: Tab): Tab {
  state.tabs.push(raw);
  const live = state.tabs[state.tabs.length - 1];
  if (!live) throw new Error('could not add the tab');
  return live;
}

function newTab(title: string, target: string, hostId?: string, hostName?: string): Tab {
  return {
    id: `tab-${++tabSeq}-${Date.now().toString(36)}`,
    title,
    target,
    hostId,
    hostName,
    status: 'connecting',
    bytesIn: 0,
    bytesOut: 0,
    startedAt: Date.now(),
  };
}

export function getBridge(tabId: string): TerminalBridge | undefined {
  return bridges.get(tabId);
}

/**
 * The geometry the last pane that got laid out ended up with.
 *
 * A dial needs a PTY size before any pane exists (the tab is created and the
 * connection starts in the same tick, and the pane is only measured a frame
 * later), so this carries the previous tab's answer across. 100x30 is the
 * fallback for the very first tab.
 */
let lastGeometry = { cols: 100, rows: 30 };

/**
 * Build the terminal for a tab. Called when its pane mounts, which is also the
 * first moment a real pixel size exists to measure.
 */
export function ensureBridge(tab: Tab): TerminalBridge {
  const existing = bridges.get(tab.id);
  if (existing) return existing;

  const bridge = createTerminalBridge({
    id: tab.id,
    cols: 100,
    rows: 30,
    fontSize: state.settings.fontSize,
    fontFamily: state.settings.fontFamily,
    encoding: state.settings.encoding,
    onSearchResults: (info) => {
      // The bar belongs to the pane in front; a background tab's matches are none
      // of its business.
      if (tab.id !== state.activeTabId) return;
      find.index = info.index;
      find.count = info.count;
    },
    wordSeparator: state.settings.wordSeparator,
    highlight: state.settings.highlight,
    background: state.settings.background,
    customRules: state.settings.customRules,
    idleKeepAliveSeconds: state.settings.idleKeepAliveMinutes * 60,
    idleNudge: state.settings.idleNudge,
    copyOnSelect: state.settings.copyOnSelect,
    continuousGlyphs: state.settings.continuousGlyphs,
    theme: state.settings.theme,
    onSend: (bytes) => {
      // Nothing can be sent to a session that has ended, and saying so on every
      // keystroke is what turned switching back to a dead tab into a wall of
      // toasts — see acceptsInput.
      if (!acceptsInput(tab)) return;
      const sessionId = tab.sessionId as string;
      const b64 = bytesToBase64(bytes);
      tab.bytesOut += bytes.length;
      backend.sendInputB64(sessionId, b64).catch((error: unknown) => {
        const message = errorMessage(error);
        if (/closed/i.test(message)) {
          // The first refusal is the news; everything after it is an echo, and
          // acceptsInput drops those once the tab is marked.
          markSessionGone(tab, message);
          return;
        }
        pushNotice('warn', `${tab.title}: ${message}`);
      });
    },
    onResize: (cols, rows) => {
      // Remembered even when this pane has no session yet — which is most of the
      // time, because the pane is laid out a frame after the tab appears and a
      // dial usually finishes after that. It is the best guess at the geometry
      // the next PTY should be born with.
      lastGeometry = { cols, rows };
      const sessionId = tab.sessionId;
      if (!sessionId) return;
      backend.resizePTY(sessionId, cols, rows).catch(() => {
        /* a resize racing a disconnect is not worth a toast */
      });
    },
    onTitle: (title) => {
      if (title.trim() && tab.hostId) tab.title = title.trim().slice(0, 48);
    },
    onLink: (url) => {
      backend.openURL(url).catch(() => window.open(url, '_blank', 'noopener'));
    },
    onNotice: (level, message, sticky) => pushNotice(level, message, sticky),
    onPaste: (text) => {
      void pasteIntoTerminal(tab, text);
    },
  });

  bridges.set(tab.id, bridge);
  drainOutput(tab, bridge);
  return bridge;
}

export function releaseBridge(tabId: string): void {
  const bridge = bridges.get(tabId);
  if (!bridge) return;
  bridges.delete(tabId);
  bridge.dispose();
}

/**
 * Move a tab onto another tab's position — what dragging one along the strip
 * does.
 *
 * Only the order changes. A terminal is held by tab id, so the pane, its xterm
 * instance, the running transfer or SSH session behind it are untouched by where
 * the tab sits, and Vue moves the keyed DOM nodes rather than remounting them.
 */
export function moveTab(tabId: string, targetId: string): void {
  if (tabId === targetId) return;
  const from = state.tabs.findIndex((tab) => tab.id === tabId);
  const to = state.tabs.findIndex((tab) => tab.id === targetId);
  if (from < 0 || to < 0) return;
  const [moved] = state.tabs.splice(from, 1);
  if (!moved) return;
  // Inserting at the target's index puts the tab after it when it came from the
  // left and before it when it came from the right — which is the gap the cursor
  // is pointing at in either direction.
  state.tabs.splice(to, 0, moved);
}

// --- find in the terminal --------------------------------------------------

/**
 * The find bar for the active pane.
 *
 * `openFind` is what `Ctrl+Shift+F` and the context menu call. The counts come from
 * the terminal's own search addon (`onSearchResults`), so the bar reports what the
 * buffer really contains rather than what was typed into it.
 */
export const find = reactive({ open: false, index: 0, count: 0 });

export function openFind(): void {
  if (!state.activeTabId) return;
  find.open = true;
}

export function closeFind(tabId: string | null | undefined = state.activeTabId): void {
  find.open = false;
  find.index = 0;
  find.count = 0;
  const bridge = tabId ? bridges.get(tabId) : undefined;
  // Leaving the highlights behind would make the terminal look permanently
  // searched; the addon keeps them until it is told otherwise — and worse, it keeps
  // re-applying the last search term to every later write through its cached state.
  bridge?.clearSearch();
}

// Every path that moves the active tab has to put the bar away, and there are more
// of them than `activateTab`: connecting a host, duplicating a tab, reconnecting a
// dropped one all assign `state.activeTabId` themselves, and closing the last tab
// sets it to null. Doing the cleanup in one watcher means a future path cannot
// forget it — the bug that proved this was a bar carrying tab A's `3 / 12` onto a
// brand-new tab B, with A's highlights now unclearable because `closeFind()` had
// only ever cleared whatever tab was active *after* the switch.
//
// Sync flush: the id has already moved by the time a normal watcher runs, so it is
// handed the tab being left rather than reading the one that replaced it.
watch(
  () => state.activeTabId,
  (next, previous) => {
    if (!previous || next === previous || !find.open) return;
    closeFind(previous);
  },
  { flush: 'sync' },
);

export function activateTab(tabId: string): void {
  const switched = state.activeTabId !== tabId;
  // The bar belongs to the pane it was opened on; another tab's counts would be a
  // lie, so switching puts it away. `closeFind()` rather than `find.open = false`
  // because it also tells the *leaving* tab's terminal to drop the highlights it
  // was drawing — and it has to run before the id moves, or it would clear the tab
  // being switched to instead.
  if (switched) closeFind();
  state.activeTabId = tabId;
  const tab = state.tabs.find((candidate) => candidate.id === tabId);
  if (!tab) return;
  const bridge = bridges.get(tab.id);
  if (bridge) {
    bridge.fitNow();
    // Only a *switch* tells the PTY its size again. This function is also what a
    // pointerdown inside the terminal calls, and an unconditional resize meant
    // every click in a full-screen program sent `WindowChange` — SIGWINCH — which
    // makes a TUI repaint the entire screen, and made clicking feel like it did
    // something to the remote side.
    if (switched) pushPaneSize(tab);
    // Focus after the pane is on screen. `v-show` hides the old pane and shows this
    // one on the *next* Vue flush, and focusing a textarea inside a `display: none`
    // subtree does nothing in Chromium: the keyboard stays where it was, inside the
    // pane that is about to disappear. That is the state Windows reports as
    // "input method disabled" — the focused node is not being rendered, so an IME has
    // nothing to attach to. The other caller (a click inside the terminal) already
    // has its pane on screen, so it focuses immediately.
    if (switched) void nextTick(() => focusIfShown(tab.id));
    else bridge.focus();
  }
}

/**
 * Is this element actually being rendered?
 *
 * Walks the ancestors because `v-show` puts `display: none` on the pane wrapper,
 * not on the terminal's own element.
 */
function isShown(element: Element | null): boolean {
  let node: Element | null = element;
  while (node) {
    const style = getComputedStyle(node);
    if (style.display === 'none' || style.visibility === 'hidden') return false;
    node = node.parentElement;
  }
  return true;
}

/** Give a tab's terminal the keyboard — but only if it is on screen. */
function focusIfShown(tabId: string): void {
  const bridge = bridges.get(tabId);
  if (bridge && isShown(bridge.el)) bridge.focus();
}

/**
 * Put the keyboard back where the user thinks it is.
 *
 * Windows draws the IME as disabled (a cross, “已禁用输入法”) when the focused node
 * is not being rendered — which is exactly what a pane hidden by `v-show` is.
 * Nothing in the page can focus a textarea in that state, so unless something picks
 * the keyboard up again the only way out is to click back into the pane. Called when
 * the window, or the webview, comes back to the user.
 *
 * It acts only when focus is nowhere or somewhere dead. A dialog's field, a button,
 * or the terminal itself is left alone: taking the keyboard from what the user was
 * using would be the app deciding it knows better.
 */
export function reclaimFocus(): void {
  const tab = state.tabs.find((candidate) => candidate.id === state.activeTabId);
  const bridge = tab ? bridges.get(tab.id) : undefined;
  if (!bridge || !isShown(bridge.el)) return;

  const focused = document.activeElement;
  if (focused && focused !== document.body && isShown(focused)) return;
  bridge.focus();
}

/**
 * Tell a tab's PTY the size its pane actually has.
 *
 * Sent unconditionally on purpose. The pane is fitted the moment it mounts, and
 * that is usually *before* the session exists, so by adoption time the fit
 * changes nothing and asking "did the fit change?" answers "no" — which is how a
 * session dialled at 100x30 stayed 100x30 inside a 170x45 window, and why `htop`
 * painted a small screen in a big one until the window was resized and something
 * finally reported a difference.
 */
function pushPaneSize(tab: Tab): void {
  const bridge = bridges.get(tab.id);
  if (!bridge || !tab.sessionId) return;
  lastGeometry = { cols: bridge.term.cols, rows: bridge.term.rows };
  void backend.resizePTY(tab.sessionId, bridge.term.cols, bridge.term.rows).catch(() => undefined);
}

/**
 * Whether this tab can send input to a remote shell right now.
 *
 * A closed tab stays mounted — it shows the "connection closed" strip over the
 * terminal — so it still receives everything a live one does: the focus-in
 * sequence xterm emits when you switch back to it, mouse reports if the program
 * had asked for the mouse, and the anti-idle's nudge. Each of those used to go
 * through `onSend`, fail with "session is closed", and raise its own toast, so
 * returning to a dead tab painted the corner with copies of the same sentence.
 *
 * Exported because this is the rule, and the only other way to reach `onSend` is
 * to open a real terminal and type into it, which no headless test can do.
 */
export function acceptsInput(tab: Pick<Tab, 'sessionId' | 'status'>): boolean {
  return Boolean(tab.sessionId) && tab.status === 'open';
}

/**
 * A refused send means the session is gone. Say it once, where the session was,
 * and stop the timer that would otherwise keep nudging a dead session.
 *
 * No toast: the pane now carries the reason and the tab is marked closed, so a
 * popup would be a third copy of the same fact.
 */
export function markSessionGone(tab: Tab, message: string): void {
  tab.status = 'closed';
  tab.detail = message;
  const bridge = bridges.get(tab.id);
  bridge?.setIdleKeepAlive(0);
  bridge?.term.write(`\r\n\x1b[2m[goterm] ${message}\x1b[0m\r\n`);
}

/**
 * True when the keyboard belongs to a remote shell: the focused element lives
 * inside the active tab's terminal.
 *
 * This is what keeps Ctrl+W (readline's word-rubout) away from "close tab" —
 * three typos in a row should not cost a session.
 */
export function terminalHasFocus(tabId: string | null | undefined = state.activeTabId): boolean {
  const bridge = tabId ? bridges.get(tabId) : undefined;
  if (!bridge) return false;
  const active = document.activeElement;
  return Boolean(active && bridge.el.contains(active));
}

export function closeTab(tabId: string): void {
  const index = state.tabs.findIndex((tab) => tab.id === tabId);
  if (index < 0) return;
  const [tab] = state.tabs.splice(index, 1);
  if (tab?.sessionId) {
    backend.closeSession(tab.sessionId).catch(() => undefined);
    pendingOutput.delete(tab.sessionId);
  }
  releaseBridge(tabId);
  if (state.activeTabId === tabId) {
    const next = state.tabs[Math.min(index, state.tabs.length - 1)];
    if (next) activateTab(next.id);
    else state.activeTabId = null;
  }
}

// --- one-field prompts ----------------------------------------------------

/**
 * A single-field modal: a two-factor code, an export passphrase, the answer to
 * "unlock this file".
 *
 * It lives in the store for the same reason the key-passphrase prompt does: the
 * caller has to continue after the user types, and the component that draws it
 * is mounted once at the app root, far from the code that asked.
 */
export interface PromptState {
  open: boolean;
  title: string;
  hint: string;
  label: string;
  placeholder: string;
  confirm: string;
  /** Mask the input (passphrase-style) instead of showing what is typed. */
  secret: boolean;
  mono: boolean;
  maxLength: number;
  error: string;
  /** Multi-line answers get a textarea; Enter then inserts a newline. */
  multiline: boolean;
  /** Text the dialog opens with, for questions about text that already exists. */
  initial: string;
}

export const prompt = reactive<PromptState>({
  open: false,
  title: '',
  hint: '',
  label: '',
  placeholder: '',
  confirm: 'OK',
  secret: false,
  mono: false,
  maxLength: 0,
  error: '',
  multiline: false,
  initial: '',
});

let pendingPrompt: ((value: string | null) => void) | null = null;

/** Called by the dialog; null means Cancel or Escape. */
export function answerPrompt(value: string | null): void {
  const resolve = pendingPrompt;
  pendingPrompt = null;
  prompt.open = false;
  prompt.error = '';
  resolve?.(value);
}

export interface PromptOptions {
  title: string;
  hint?: string;
  label?: string;
  placeholder?: string;
  confirm?: string;
  secret?: boolean;
  mono?: boolean;
  maxLength?: number;
  multiline?: boolean;
  initial?: string;
}

function askPrompt(options: PromptOptions, error = ''): Promise<string | null> {
  // One modal at a time: a second ask replaces the first instead of stacking.
  if (pendingPrompt) answerPrompt(null);
  return new Promise<string | null>((resolve) => {
    pendingPrompt = resolve;
    Object.assign(prompt, {
      title: options.title,
      hint: options.hint ?? '',
      label: options.label ?? '',
      placeholder: options.placeholder ?? '',
      confirm: options.confirm ?? 'OK',
      secret: options.secret ?? false,
      mono: options.mono ?? false,
      maxLength: options.maxLength ?? 0,
      multiline: options.multiline ?? false,
      initial: options.initial ?? '',
      open: true,
      error,
    });
  });
}

/**
 * The two-factor prompt. It comes before the first attempt, not after a
 * rejection: a bastion that wants a code will refuse a code-less password too,
 * and making the user wait out a failed handshake to type six digits is rude.
 */
function askTwoFactor(label: string, error = ''): Promise<string | null> {
  return askPrompt(
    {
      title: 'Two-factor code',
      hint: `Enter the one-time code for ${label}. GoTerm appends it to the saved password — it is never stored.`,
      label: 'One-time code',
      placeholder: '123456',
      confirm: 'Connect',
      mono: true,
      maxLength: 16,
    },
    error,
  );
}

// --- passphrase prompt ----------------------------------------------------
/**
 * The modal that asks for a private key's passphrase.
 *
 * It lives in the store rather than in a component because the retry has to
 * happen after the user types, and two flows reach it: opening a saved host and
 * Quick connect.
 */
export interface SecretPromptState {
  open: boolean;
  title: string;
  hint: string;
  error: string;
  /** Only a saved host can store the answer; an ad-hoc connect cannot. */
  rememberable: boolean;
}

export const secretPrompt = reactive<SecretPromptState>({
  open: false,
  title: '',
  hint: '',
  error: '',
  rememberable: false,
});

type SecretAnswer = { secret: string; remember: boolean } | null;

let pendingSecret: ((answer: SecretAnswer) => void) | null = null;

/** Called by the dialog; null means the user pressed Cancel or Escape. */
export function answerSecretPrompt(answer: SecretAnswer): void {
  const resolve = pendingSecret;
  pendingSecret = null;
  secretPrompt.open = false;
  secretPrompt.error = '';
  resolve?.(answer);
}

function askSecret(prompt: Omit<SecretPromptState, 'open' | 'error'>, error = ''): Promise<SecretAnswer> {
  // A second prompt replaces the first instead of stacking modals.
  if (pendingSecret) answerSecretPrompt(null);
  return new Promise<SecretAnswer>((resolve) => {
    pendingSecret = resolve;
    Object.assign(secretPrompt, prompt, { open: true, error });
  });
}

/**
 * True when Go reports a private key that needs its passphrase.
 *
 * Wails delivers errors as text, so this matches the words Go puts on the
 * error. `sshx.ErrPassphraseRequired` is the Go half of the contract and
 * `internal/sshx/passphrase_test.go` pins the wording, so the two cannot drift
 * apart without a test failing.
 */
export function needsPassphrase(error: unknown): boolean {
  return /passphrase required/i.test(errorMessage(error));
}

/**
 * True when Go refuses to dial because it has no password for the host.
 *
 * `hoststore.ErrPasswordRequired` is the Go half of the contract and
 * `internal/hoststore/password_prompt_test.go` pins both sentences, so neither
 * side can reword it without a test failing. A host saved without a password used
 * to dead-end here with "edit the host and enter it again", which turned a
 * double-click into an auth error with no way forward.
 */
export function needsPassword(error: unknown): boolean {
  return /password required/i.test(errorMessage(error));
}

const CANCELLED = 'connection cancelled';

/** True when the user dismissed the passphrase prompt. */
export function wasCancelled(error: unknown): boolean {
  return errorMessage(error) === CANCELLED;
}

interface PassphraseOptions {
  title: string;
  hint: string;
  /** Saved hosts may remember the passphrase; ad-hoc connections cannot. */
  hostId?: string;
  /**
   * Set for a two-factor host: the label is user@host, shown in the code prompt
   * so it is obvious which login is asking.
   */
  twoFactor?: { label: string };
  /**
   * Called when Go refuses to dial because a password is not stored. The caller
   * refreshes whatever it will send next and the loop retries: a saved host asks
   * the user (and remembers on request), while an ad-hoc connect already has a
   * typed password and passes no hook, so the refusal stands as an error.
   *
   * The answers deliberately do not travel through this function — where a
   * password comes from is the caller's business, and passing five same-typed
   * strings between prompt and attempt is how a target's answer once ended up in
   * the jump host's slot.
   */
  refreshPassword?: (kind: 'target' | 'proxy', error: string) => Promise<void>;
}

/** True when the server turned the credentials down, which is what an expired
 *  or wrong one-time code looks like from here. */
export function looksLikeAuthFailure(error: unknown): boolean {
  return /authentication failed|unable to authenticate/i.test(errorMessage(error));
}

/**
 * One password prompt, shared by the up-front ask (a saved host with no stored
 * password) and the reactive one (Go said so, e.g. the key-ring entry went
 * missing). Throws CANCELLED when the user dismisses it, and stores the answer
 * only when they tick "remember" — the same rule every other secret follows.
 */
async function askLogin(kind: 'target' | 'proxy', label: string, hostId: string | undefined, error = ''): Promise<string> {
  const answer = await askSecret(
    {
      title: kind === 'proxy' ? 'Jump-host password' : 'Password',
      hint:
        kind === 'proxy'
          ? `GoTerm has no saved jump-host password for ${label}. It is used for this connection only.`
          : `GoTerm has no saved password for ${label}. It is used for this connection only.`,
      rememberable: Boolean(hostId),
    },
    error,
  );
  if (!answer) throw new Error(CANCELLED);
  if (answer.remember && hostId) {
    await (kind === 'proxy'
      ? backend.setHostProxySecret(hostId, answer.secret)
      : backend.setHostSecret(hostId, answer.secret));
  }
  return answer.secret;
}

/**
 * Runs one connect attempt and, when a key turns out to be encrypted, asks for
 * the passphrase and tries again — which is what an encrypted key needs and a
 * form field never delivered.
 *
 * The answer is used for this attempt only; it reaches the key ring solely when
 * the user ticks "remember", and the jump host's passphrase is stored as the
 * jump host's own secret. A two-factor host is asked for its code before the
 * first attempt, and again if the server turns the login down. A host whose
 * password was never stored hands the ask to `refreshPassword` (see there) and
 * retries, so a missing secret is a prompt rather than a failed login.
 */
export async function connectWithPassphrasePrompt(
  attempt: (passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>,
  options: PassphraseOptions,
): Promise<string> {
  let passphrase = '';
  let proxyPassphrase = '';
  let twoFactorCode = '';

  if (options.twoFactor) {
    const answer = await askTwoFactor(options.twoFactor.label);
    if (!answer || !answer.trim()) throw new Error(CANCELLED);
    twoFactorCode = answer.trim();
  }

  for (let round = 0; round < 4; round += 1) {
    try {
      return await attempt(passphrase, proxyPassphrase, twoFactorCode);
    } catch (error) {
      const message = errorMessage(error);
      if (needsPassphrase(error)) {
        // Go marks the jump host's request — "passphrase required (proxy): …" — so
        // the prompt can say which key it is asking for. Match the whole marker and
        // nothing looser: a key's *path* is no such hint, and a target key filed
        // under "proxy.pem" or "(proxy)" used to have its passphrase sent to the
        // jump host's field — both logins then failed with a prompt that looked fine.
        const forProxy = /passphrase required \(proxy\)/i.test(message);
        const answer = await askSecret(
          {
            title: forProxy ? 'Jump-host key passphrase' : options.title,
            hint: forProxy ? 'The jump host private key is encrypted.' : options.hint,
            rememberable: Boolean(options.hostId),
          },
          // The first ask is just the question; after that the prompt carries the
          // failure that brought the user back, so a typo is diagnosable.
          round === 0 ? '' : message,
        );
        if (!answer) throw new Error(CANCELLED);
        if (forProxy) proxyPassphrase = answer.secret;
        else passphrase = answer.secret;
        if (answer.remember && options.hostId) {
          await (forProxy
            ? backend.setHostProxySecret(options.hostId, answer.secret)
            : backend.setHostSecret(options.hostId, answer.secret));
        }
        continue;
      }
      // A saved host with no stored password (or one whose key-ring entry went
      // missing): ask the caller to refresh it, then try again. The "(proxy)"
      // marker is matched whole so a jump host's missing password never opens
      // the target's prompt — the same trap the passphrase branch above guards.
      if (needsPassword(error) && options.refreshPassword) {
        const forProxy = /password required \(proxy\)/i.test(message);
        await options.refreshPassword(forProxy ? 'proxy' : 'target', message);
        continue;
      }
      // A refused login on a two-factor host is most often an expired code, so
      // it earns one more ask; every other refusal goes straight back up.
      if (options.twoFactor && looksLikeAuthFailure(error)) {
        const answer = await askTwoFactor(options.twoFactor.label, message);
        if (!answer || !answer.trim()) throw new Error(CANCELLED);
        twoFactorCode = answer.trim();
        continue;
      }
      throw error;
    }
  }
  throw new Error(
    options.twoFactor
      ? 'that code was not accepted either'
      : 'that passphrase still did not decrypt the key',
  );
}

/** Shared tail of both connect paths: hand the live session to its tab. */
async function adoptSession(tab: Tab, sessionId: string): Promise<void> {
  tab.sessionId = sessionId;
  tab.status = 'open';
  tab.detail = '';
  const bridge = bridges.get(tab.id);
  if (bridge) {
    bridge.fitNow();
    // Before anything is replayed: staged bytes are already wrapped for the old
    // size and no resize can fix them, but everything after this is not.
    pushPaneSize(tab);
  }
  await describeSession(tab, sessionId);
  if (bridge) {
    drainOutput(tab, bridge);
    bridge.focus();
  }
  void loadKnownHosts();
}

/** Shared failure path: the tab explains itself, and so does a notice. */
function failTab(tab: Tab, error: unknown): boolean {
  if (wasCancelled(error)) {
    // Cancelling a prompt is not an error; the tab just never opened.
    tab.status = 'closed';
    tab.detail = CANCELLED;
    pushNotice('info', `${tab.title}: ${CANCELLED}`);
    return false;
  }
  tab.status = 'error';
  tab.detail = errorMessage(error);
  pushNotice('error', tab.detail, true);
  bridges.get(tab.id)?.term.write(`\r\n\x1b[31m[goterm] ${tab.detail}\x1b[0m\r\n`);
  return false;
}

/** Opens a session for a saved host; the password never reaches this process. */
export async function connectHost(hostId: string, options: { reuseExisting?: boolean } = {}): Promise<boolean> {
  const host = state.hosts.find((candidate) => candidate.id === hostId);
  if (!host) {
    pushNotice('error', 'That host is no longer in the list — refresh and try again.');
    return false;
  }
  // Clicking a host twice should focus the session it already has. Reconnect is
  // the opposite: it was asked for *because* the tab is dead, and with duplicate
  // sessions open there may well be another live tab for the same host.
  const existing = options.reuseExisting === false
    ? undefined
    : state.tabs.find((tab) => tab.hostId === hostId && (tab.status === 'open' || tab.status === 'connecting'));
  if (existing) {
    activateTab(existing.id);
    return true;
  }

  const tab = pushTab(newTab(host.name || host.host, `${host.user}@${host.host}:${host.port}`, host.id, host.name));
  state.activeTabId = tab.id;
  state.busy = true;
  // Passwords live here rather than inside the retry machine: a saved host asks
  // the user for one, an ad-hoc connect already had it typed into its form. The
  // attempt closure reads the current value every round, so "refresh" is just an
  // assignment — and no answer can be handed to the wrong machine by position.
  let password = '';
  let proxyPassword = '';
  const askLoginFor = async (kind: 'target' | 'proxy', error = ''): Promise<void> => {
    const secret = await askLogin(kind, `${host.user}@${host.host}:${host.port}`, hostId, error);
    if (kind === 'proxy') proxyPassword = secret;
    else password = secret;
  };
  try {
    // Ask before dialing when we can already see there is nothing to send. An
    // empty password would only come back as a refusal, and that refusal is
    // charged to the server's failed-login counter — possibly to an account
    // lockout — for a mistake GoTerm can see before it connects.
    if (host.authType === 'password' && !host.hasSecret) await askLoginFor('target');
    if (host.proxyHost && host.proxyAuthType === 'password' && !host.hasProxySecret) await askLoginFor('proxy');

    const sessionId = await connectWithPassphrasePrompt(
      (passphrase, proxyPassphrase, twoFactorCode) =>
        backend.connectHost(hostId, lastGeometry.cols, lastGeometry.rows, {
          passphrase,
          proxyPassphrase,
          twoFactorCode,
          password,
          proxyPassword,
        }),
      {
        title: 'Key passphrase',
        hint: `The private key for ${host.user}@${host.host} is encrypted.`,
        hostId,
        refreshPassword: (kind, error) => askLoginFor(kind, error),
        ...(host.twoFactor ? { twoFactor: { label: `${host.user}@${host.host}` } } : {}),
      },
    );
    await adoptSession(tab, sessionId);
    return true;
  } catch (error) {
    return failTab(tab, error);
  } finally {
    state.busy = false;
  }
}

export async function connectAdhoc(config: {
  host: string;
  port: number;
  user: string;
  authType: 'password' | 'key' | 'agent';
  password?: string;
  keyPath?: string;
  passphrase?: string;
  proxy?: ProxyConfig;
  /** Append a one-time code to the typed password, as a 2FA bastion expects. */
  twoFactor?: boolean;
}): Promise<boolean> {
  const tab = pushTab(newTab(`${config.user}@${config.host}:${config.port}`, `${config.user}@${config.host}:${config.port}`));
  state.activeTabId = tab.id;
  state.busy = true;
  try {
    const sessionId = await connectWithPassphrasePrompt(
      (passphrase, proxyPassphrase, twoFactorCode) =>
        backend.connect({
          host: config.host,
          port: config.port,
          user: config.user,
          authType: config.authType,
          // Nothing is stored here, so the code is simply part of the password
          // that leaves for Go with this one attempt.
          password: config.twoFactor ? `${config.password ?? ''}${twoFactorCode}` : config.password ?? '',
          keyPath: config.keyPath ?? '',
          passphrase: passphrase || config.passphrase || '',
          cols: lastGeometry.cols,
          rows: lastGeometry.rows,
          term: 'xterm-256color',
          hostKeyPolicy: 'tofu',
          ...(config.proxy
            ? { proxy: { ...config.proxy, ...(proxyPassphrase ? { passphrase: proxyPassphrase } : {}) } }
            : {}),
        }),
      {
        title: 'Key passphrase',
        hint: `The private key for ${config.user}@${config.host} is encrypted.`,
        ...(config.twoFactor ? { twoFactor: { label: `${config.user}@${config.host}` } } : {}),
      },
    );
    await adoptSession(tab, sessionId);
    return true;
  } catch (error) {
    return failTab(tab, error);
  } finally {
    state.busy = false;
  }
}

/** Pulls the server version and pinned fingerprint onto the tab for the status bar. */
async function describeSession(tab: Tab, sessionId: string): Promise<void> {
  try {
    const sessions = await backend.listSessions();
    const info = sessions.find((candidate) => candidate.id === sessionId);
    if (info) {
      tab.serverVersion = info.serverVersion;
      tab.fingerprint = info.fingerprint;
      tab.via = info.via;
    }
  } catch {
    /* status-bar garnish only; never fail a session over it */
  }
}

export async function reconnect(tabId: string): Promise<void> {
  const index = state.tabs.findIndex((tab) => tab.id === tabId);
  if (index < 0) return;
  const tab = state.tabs[index] as Tab;
  if (!tab.hostId) {
    pushNotice('info', 'Only saved hosts can reconnect — the ad-hoc password was never kept.');
    return;
  }
  closeTab(tabId);
  await connectHost(tab.hostId, { reuseExisting: false });
}

/** A tab title that will not collide with a duplicate already on screen. */
function duplicateTitle(source: Tab): string {
  const taken = new Set(state.tabs.filter((tab) => tab.target === source.target).map((tab) => tab.title));
  for (let n = 2; n < 100; n += 1) {
    const candidate = `${source.title} (${n})`;
    if (!taken.has(candidate)) return candidate;
  }
  return `${source.title} (${Date.now().toString(36)})`;
}

/**
 * Opens a second terminal on a connection that is already up — Xshell's
 * "Duplicate session".
 *
 * Go opens another channel on the same SSH client, so nothing is authenticated
 * again: no password, no key passphrase, no one-time code (which is the whole
 * point in front of a JumpServer). Closing either tab leaves the other one
 * working; the connection goes when the last of them does.
 */
export async function duplicateTab(tabId: string): Promise<boolean> {
  const source = state.tabs.find((candidate) => candidate.id === tabId);
  if (!source?.sessionId || source.status !== 'open') {
    pushNotice('info', 'Only a connected session can be duplicated — connect first, then duplicate.');
    return false;
  }
  state.busy = true;
  try {
    const sessionId = await backend.duplicateSession(source.sessionId);
    const tab = pushTab(newTab(duplicateTitle(source), source.target, source.hostId, source.hostName));
    state.activeTabId = tab.id;
    await adoptSession(tab, sessionId);
    // No toast: the new tab appearing, connected and focused, is the whole message.
    return true;
  } catch (error) {
    // No tab was created yet, so there is nothing to mark as failed.
    pushNotice('error', `Duplicate failed: ${errorMessage(error)}`, true);
    return false;
  } finally {
    state.busy = false;
  }
}

// --- misc -----------------------------------------------------------------

// --- clipboard and editing on a terminal ----------------------------------

function bridgeFor(tabId?: string): TerminalBridge | undefined {
  const id = tabId ?? state.activeTabId;
  return id ? bridges.get(id) : undefined;
}

export async function copyActiveSelection(tabId?: string): Promise<void> {
  const bridge = bridgeFor(tabId);
  if (!bridge) return;
  const text = bridge.term.hasSelection() ? bridge.term.getSelection() : '';
  if (!text) {
    pushNotice('info', 'Nothing selected.');
    return;
  }
  if (!(await writeClipboard(text))) pushNotice('warn', 'Could not reach the clipboard.');
}

export async function pasteIntoActive(tabId?: string): Promise<void> {
  const bridge = bridgeFor(tabId);
  if (!bridge) return;
  const tab = state.tabs.find((candidate) => candidate.id === bridge.id);
  if (!tab) return;
  const text = await readClipboard();
  if (!text) return;
  await pasteIntoTerminal(tab, text);
}

/**
 * One paste, whichever way it arrived: Ctrl+Shift+V, the context menu, or the
 * browser's own paste event (which the terminal takes over so this path cannot
 * be bypassed).
 *
 * A single line — with or without its trailing newline — goes straight to the
 * PTY, because that is what "copy a command and paste it" means. A block of
 * lines is a script, and a script nobody has read is exactly how the wrong host
 * gets wiped: it is shown in an editable dialog first, with the blank lines a
 * web page likes to prepend stripped off.
 */
export async function pasteIntoTerminal(tab: Tab, raw: string): Promise<void> {
  const text = trimLeadingNewlines(raw);
  if (!text) return;
  if (!isMultilinePaste(text)) {
    bridges.get(tab.id)?.pasteText(text);
    return;
  }

  const review = trimTrailingBlankLines(text);
  const lines = lineCount(review);
  const answer = await askPrompt({
    title: 'Paste several lines?',
    hint:
      `${lines} lines are about to be sent to ${tab.title}. Read them first — ` +
      'Enter pastes them, Shift+Enter adds a line break, Escape cancels.',
    label: `${lines} lines`,
    confirm: 'Paste',
    mono: true,
    multiline: true,
    initial: review,
  });
  if (answer === null) {
    pushNotice('info', 'Paste cancelled.');
    bridges.get(tab.id)?.focus();
    return;
  }
  if (!answer.trim()) {
    bridges.get(tab.id)?.focus();
    return;
  }
  // The tab may have been closed while the dialog was open.
  const target = bridges.get(tab.id);
  if (!target) return;
  // The dialog took the focus; typing should land in the session again.
  target.focus();
  target.pasteText(answer);
}

export function clearActiveBuffer(tabId?: string): void {
  bridgeFor(tabId)?.clear();
}

export function zoomActive(step: number): void {
  setFontSize(Math.min(28, Math.max(9, state.settings.fontSize + step)));
}

export function cycleTab(offset: number): void {
  if (state.tabs.length < 2) return;
  const index = state.tabs.findIndex((tab) => tab.id === state.activeTabId);
  const next = state.tabs[(index + offset + state.tabs.length) % state.tabs.length];
  if (next) activateTab(next.id);
}

export function runtimeSummary(): string {
  const info = state.info;
  if (!info) return state.bridgeReady ? 'connecting to backend…' : (bridgeError() ? `no backend: ${bridgeError()}` : 'no backend');
  const parts = [`v${info.version}`, info.platform, info.secretBackend];
  if (!info.secretSecure) parts.push('secrets in a sealed file, not the OS key ring');
  return parts.join(' · ');
}

export function formatThroughput(tab: Tab): string {
  return `↓ ${formatBytes(tab.bytesIn)}  ↑ ${formatBytes(tab.bytesOut)}`;
}

export function hasBackend(): boolean {
  return bridgeAvailable();
}

export async function runSelfTest(): Promise<void> {
  pushNotice('info', 'Self-test: starting a local SSH server and driving the real SSH stack…', true);
  try {
    const report = await backend.selfTest();
    pushNotice(report.startsWith('PASS') ? 'info' : 'error', report, true);
  } catch (error) {
    pushNotice('error', `Self-test failed: ${errorMessage(error)}`, true);
  }
}

export async function pingBackend(): Promise<void> {
  try {
    pushNotice('info', `backend says: ${await backend.ping()}`);
  } catch (error) {
    pushNotice('error', errorMessage(error));
  }
}
