<script setup lang="ts">
/*
 * Settings: what the terminal looks like and how it behaves.
 *
 * The font list is the interesting part. A terminal cell is a fixed rectangle, so
 * a proportional face is not a matter of taste — it breaks the grid. The list
 * therefore comes from Go, which asks GDI which installed faces are fixed-pitch,
 * instead of a text box that would happily accept "Arial".
 */
import { RotateCcw, Trash2, X } from 'lucide-vue-next';
import { computed, onMounted, reactive, ref } from 'vue';

import { backend, errorMessage, fileBridgeApi } from '../lib/backend';
import { base64ToBytes } from '../lib/bytes';
import {
  HIGHLIGHT_SETS,
  compileCustomRules,
  type CustomRule,
  type HighlightColour,
} from '../lib/highlight';
import { decodeMsyn, looksLikeMsyn, parseMsyn } from '../lib/msyn';
import { TERMINAL_ENCODINGS, isTerminalEncoding } from '../lib/encoding';
import {
  pushNotice,
  resetSettings,
  setBackgroundColor,
  setCustomRules,
  setFontFamily,
  setFontSize,
  setHighlightSets,
  setIdleKeepAlive,
  setIdleNudge,
  setCopyOnSelect,
  setContinuousGlyphs,
  setSessionHours,
  setTerminalEncoding,
  setWordSeparator,
  state,
} from '../lib/store';
import { DEFAULT_WORD_SEPARATOR, fontStack, paneTheme, type IdleNudge } from '../lib/terminal';
import type { StorageInfo } from '../lib/protocol';

const emit = defineEmits<{ (event: 'close'): void }>();

// --- sections --------------------------------------------------------------
//
// One long scroll became three tabs. The sections stay exactly where they are
// in the DOM (each one is simply shown or hidden) because the component's state
// — the loaded font list, the pending separator, the custom rules — lives in
// refs that should not be torn down every time a tab is clicked.

const TABS = [
  { id: 'appearance', label: 'Appearance' },
  { id: 'behaviour', label: 'Behaviour' },
  { id: 'highlighting', label: 'Highlighting' },
] as const;

type SettingsTab = (typeof TABS)[number]['id'];

const TAB_KEY = 'goterm.settingsTab';

function storedTab(): SettingsTab {
  try {
    const saved = localStorage.getItem(TAB_KEY);
    return TABS.some((item) => item.id === saved) ? (saved as SettingsTab) : 'appearance';
  } catch {
    return 'appearance';
  }
}

const tab = ref<SettingsTab>(storedTab());

function pickTab(id: SettingsTab): void {
  tab.value = id;
  try {
    localStorage.setItem(TAB_KEY, id);
  } catch {
    /* a private window that refuses storage is not a reason to fail a click */
  }
}

const families = ref<string[]>([]);
const loadError = ref('');
const loading = ref(true);

onMounted(async () => {
  void loadStorage();
  try {
    const list = await backend.fonts();
    families.value = list.map((family) => family.name).filter(Boolean);
  } catch (error) {
    loadError.value = errorMessage(error);
  } finally {
    loading.value = false;
  }
});

// --- storage ---------------------------------------------------------------
//
// Go owns this one. The state directory is resolved before a webview exists, so
// the dialog reads it back from the backend instead of keeping it in
// localStorage like the other settings. A change is recorded for the *next*
// start and says so, because hosts and the secret ring were opened at startup
// and nothing is moved behind the user's back.
const storage = ref<StorageInfo | null>(null);
const storageError = ref('');

async function loadStorage(): Promise<void> {
  try {
    storage.value = await backend.storage();
    storageError.value = '';
  } catch (error) {
    storageError.value = errorMessage(error);
  }
}

async function adoptStorage(dir: string): Promise<void> {
  try {
    storage.value = await backend.setStorageDir(dir);
    storageError.value = '';
    const next = storage.value;
    pushNotice(
      'info',
      next.restartPending
        ? `Storage folder set to ${next.pendingDir}. Restart GoTerm to use it; nothing was moved for you.`
        : 'Storage folder is back to the default.',
      true,
    );
  } catch (error) {
    storageError.value = errorMessage(error);
    await loadStorage();
  }
}

async function chooseStorage(): Promise<void> {
  try {
    const dir = await backend.pickStorageDir();
    if (!dir) return;
    await adoptStorage(dir);
  } catch (error) {
    storageError.value = errorMessage(error);
  }
}

function useDefaultStorage(): void {
  void adoptStorage('');
}

const FONT_DEFAULT = '';const fontOptions = computed(() => {
  const current = state.settings.fontFamily;
  // A saved family that has since been uninstalled stays visible: the select
  // must never silently rewrite the user's choice on open.
  if (current && !families.value.includes(current)) return [current, ...families.value];
  return families.value;
});

const previewStyle = computed(() => ({
  fontFamily: fontStack(state.settings.fontFamily),
  fontSize: `${state.settings.fontSize}px`,
}));

function toggleSet(id: string, on: boolean): void {
  const current = new Set(state.settings.highlight);
  if (on) current.add(id);
  else current.delete(id);
  // Keep the definition order, so the settings list and the rule order agree.
  setHighlightSets(HIGHLIGHT_SETS.filter((set) => current.has(set.id)).map((set) => set.id));
}

function pickFont(event: Event): void {
  setFontFamily((event.target as HTMLSelectElement).value);
}

function pickSize(event: Event): void {
  const value = Number.parseInt((event.target as HTMLInputElement).value, 10);
  if (Number.isFinite(value)) setFontSize(value);
}

const separator = ref(state.settings.wordSeparator);
const separatorError = computed(() => (separator.value.length ? '' : 'A word separator of nothing would select whole lines.'));

function applySeparator(): void {
  if (!separator.value.length) return;
  setWordSeparator(separator.value);
}

function restoreSeparator(): void {
  separator.value = DEFAULT_WORD_SEPARATOR;
  setWordSeparator(DEFAULT_WORD_SEPARATOR);
}

const separatorPreview = computed(() => {
  // What a double-click on “log” in this sample selects, given the separator.
  const text = 'tail -f /var/log/nginx/access.log:404';
  if (!separator.value.length) return text;
  // Inside a character class only these characters need escaping; a user typing
  // `[` as a separator must not turn the preview into a syntax error.
  const escaped = Array.from(separator.value)
    .map((ch) => ch.replace(/[\\\]^\[-]/g, '\\$&'))
    .join('');
  const segments = text.split(new RegExp(`[${escaped}]`)).filter(Boolean);
  return segments.find((segment) => segment.includes('log')) ?? segments[0] ?? '';
});

// --- background ------------------------------------------------------------

const BACKGROUND_PRESETS = [
  { label: 'Black', value: '#000000' },
  { label: 'Solarized dark', value: '#002b36' },
  { label: 'Nord', value: '#2e3440' },
  { label: 'Gruvbox', value: '#282828' },
  { label: 'Solarized light', value: '#fdf6e3' },
  { label: 'Sepia', value: '#f4ecd8' },
];

const themeBackground = computed(() => (state.settings.theme === 'light' ? '#ffffff' : '#0d1117'));
const backgroundValue = computed(() => state.settings.background || themeBackground.value);

/* Relative luminance and the palette a pane can be read on live in terminal.ts,
 * where the palettes are, so the warning below cannot drift from the behaviour. */
const contrastWarning = computed(() => {
  if (!state.settings.background) return '';
  // The pane's own palette is chosen by contrast, so a mismatched pane is no
  // longer unreadable — it is just two different looks side by side, which is
  // worth saying once and leaving to the user.
  if (paneTheme(state.settings.theme, state.settings.background) === state.settings.theme) return '';
  return state.settings.theme === 'dark'
    ? 'A light background under the dark theme: the terminal uses its light palette so the text stays readable, while the tabs and sidebar stay dark. Switch the theme (moon icon in the status bar) if the mixture bothers you.'
    : 'A dark background under the light theme: the terminal uses its dark palette so the text stays readable, while the tabs and sidebar stay light. Switch the theme if the mixture bothers you.';
});

function pickBackground(event: Event): void {
  setBackgroundColor((event.target as HTMLInputElement).value);
}

function usePreset(value: string): void {
  setBackgroundColor(value);
}

function resetBackground(): void {
  setBackgroundColor('');
}

// --- anti-idle -------------------------------------------------------------

function toggleIdleKeepAlive(event: Event): void {
  const on = (event.target as HTMLInputElement).checked;
  setIdleKeepAlive(on ? state.settings.idleKeepAliveMinutes || 3 : 0);
}

function pickIdleMinutes(event: Event): void {
  const value = Number.parseInt((event.target as HTMLInputElement).value, 10);
  if (Number.isFinite(value)) setIdleKeepAlive(Math.min(120, Math.max(1, value)));
}

function pickIdleNudge(event: Event): void {
  // The store owns which values are nudges; the menu and the setting cannot drift
  // apart because this adds a third name by hand.
  setIdleNudge((event.target as HTMLSelectElement).value as IdleNudge);
}

// --- selection and paste ---------------------------------------------------

function toggleCopyOnSelect(event: Event): void {
  setCopyOnSelect((event.target as HTMLInputElement).checked);
}

// --- glyph rendering -------------------------------------------------------

function toggleContinuousGlyphs(event: Event): void {
  setContinuousGlyphs((event.target as HTMLInputElement).checked);
}

// --- session lifetime ------------------------------------------------------
//
// A hard cap on one connection, in whole hours. Go clamps to 1–24 and applies it
// to open connections as well, so the number here is what is enforced, not a
// hint kept in localStorage.

function pickSessionHours(event: Event): void {
  const hours = Number((event.target as HTMLInputElement).value);
  setSessionHours(Number.isFinite(hours) ? hours : state.settings.sessionHours);
}

// --- remote text encoding --------------------------------------------------
//
// Bytes reach xterm untouched, so a host that speaks GBK cannot be read by
// guessing at the font: the bytes themselves have to be decoded first. Picking a
// value here applies to every open tab immediately, which makes it a way to try
// an encoding rather than something that needs a restart.

const encodingChoices = TERMINAL_ENCODINGS;
const encodingHint = computed(() => TERMINAL_ENCODINGS.find((choice) => choice.id === state.settings.encoding)?.hint ?? '');

function applyEncoding(event: Event): void {
  const value = (event.target as HTMLSelectElement).value;
  if (isTerminalEncoding(value)) setTerminalEncoding(value);
}

// --- custom keywords -------------------------------------------------------

const COLOURS: HighlightColour[] = ['red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white', 'grey', 'bold', 'underline'];
/** Swatch colours for the rules list; they match the SGR the terminal gets. */
const SWATCH: Record<HighlightColour, string> = {
  red: '#ff7b72',
  green: '#3fb950',
  yellow: '#d29922',
  blue: '#79c0ff',
  magenta: '#bc8cff',
  cyan: '#39c5cf',
  white: '#f0f6fc',
  grey: '#8b949e',
  bold: '#d7e0ea',
  underline: '#d7e0ea',
};

function swatch(colour: HighlightColour): string {
  return SWATCH[colour] ?? SWATCH.yellow;
}
const draft = reactive({ pattern: '', colour: 'yellow' as HighlightColour, regex: false, ignoreCase: true });
const draftError = ref('');
const importSummary = ref('');
const importError = ref('');
const importSkipped = ref<{ source: string; reason: string }[]>([]);

const customRules = computed(() => state.settings.customRules);

/** Compiled here rather than per bridge, so a pattern error is visible at once. */
const ruleErrors = computed(() => compileCustomRules(customRules.value).errors);

function commit(rules: CustomRule[]): void {
  setCustomRules(rules);
}

function addRule(): void {
  draftError.value = '';
  const pattern = draft.pattern.trim();
  if (!pattern) {
    draftError.value = 'Type a keyword or a regular expression first.';
    return;
  }
  const candidate: CustomRule = {
    id: `custom-${Date.now().toString(36)}-${customRules.value.length}`,
    pattern,
    colour: draft.colour,
    regex: draft.regex,
    ignoreCase: draft.ignoreCase,
    enabled: true,
  };
  if (draft.regex) {
    const compiled = compileCustomRules([candidate]);
    if (compiled.errors.length) {
      draftError.value = `That expression does not compile: ${compiled.errors[0] ?? ''}`;
      return;
    }
  }
  commit([...customRules.value, candidate]);
  draft.pattern = '';
}

function removeRule(id: string): void {
  commit(customRules.value.filter((rule) => rule.id !== id));
}

function toggleRule(id: string, enabled: boolean): void {
  commit(customRules.value.map((rule) => (rule.id === id ? { ...rule, enabled } : rule)));
}

function setRuleColour(id: string, colour: HighlightColour): void {
  commit(customRules.value.map((rule) => (rule.id === id ? { ...rule, colour } : rule)));
}

async function importMsyn(): Promise<void> {
  importSummary.value = '';
  importError.value = '';
  importSkipped.value = [];
  try {
    // The same Go-backed picker trzsz uses; choosing a file also grants read
    // access to its folder for this session.
    const api = fileBridgeApi();
    const [path] = await api.chooseFiles('Choose a MobaXterm syntax file (.msyn)');
    if (!path) return;
    const entry = await api.stat(path);
    const size = Math.min(entry.size, 4 << 20);
    const text = decodeMsyn(base64ToBytes(await api.readRange(path, 0, size)));
    if (!looksLikeMsyn(text)) {
      importError.value = 'That file is not a MobaXterm .msyn syntax file.';
      return;
    }
    const result = parseMsyn(text);
    const seen = new Set(customRules.value.map((rule) => `${rule.colour}\u0000${rule.pattern}`));
    const stamp = Date.now().toString(36);
    const added = result.rules
      .filter((rule) => !seen.has(`${rule.colour}\u0000${rule.pattern}`))
      .map((rule, index) => ({ ...rule, id: `msyn-${stamp}-${index}` }));
    commit([...customRules.value, ...added]);
    importSkipped.value = result.skipped.slice(0, 5);
    importSummary.value =
      `Imported ${added.length} rule(s) from “${result.meta.name}”` +
      (result.skipped.length ? ` — ${result.skipped.length} branch(es) could not be converted to JavaScript.` : '.');
  } catch (error) {
    importError.value = errorMessage(error);
  }
}
</script>

<template>
  <div class="fixed inset-0 z-40 flex items-start justify-center bg-black/45 p-6 pt-12" @click.self="emit('close')">
    <div class="flex max-h-[86vh] w-[620px] flex-col overflow-hidden rounded-xl border border-[var(--border)] bg-[var(--panel)] shadow-2xl">
      <header class="flex shrink-0 items-center gap-2 border-b border-[var(--border)] px-4 py-3">
        <h2 class="text-[13px] font-semibold">Settings</h2>
        <span class="text-[11px] text-[var(--text-dim)]">applies to every tab, open and future</span>
        <button class="focus-ring ml-auto flex items-center gap-1 rounded px-1.5 py-1 text-[11px] text-[var(--text-dim)] hover:text-[var(--text)]" title="Restore the defaults" @click="resetSettings">
          <RotateCcw :size="12" /> Reset
        </button>
        <button class="focus-ring rounded p-1 text-[var(--text-dim)] hover:text-[var(--text)]" @click="emit('close')">
          <X :size="14" />
        </button>
      </header>

      <nav
        class="flex shrink-0 gap-1 overflow-x-auto border-b border-[var(--border)] px-3"
        role="tablist"
        aria-label="Settings sections"
      >
        <button
          v-for="item in TABS"
          :key="item.id"
          type="button"
          role="tab"
          :aria-selected="tab === item.id"
          :data-test="`settings-tab-${item.id}`"
          class="focus-ring -mb-px shrink-0 border-b-2 px-2.5 py-2 text-[12px] transition-colors"
          :class="
            tab === item.id
              ? 'border-[var(--accent)] text-[var(--text)]'
              : 'border-transparent text-[var(--text-dim)] hover:text-[var(--text)]'
          "
          @click="pickTab(item.id)"
        >
          {{ item.label }}
        </button>
      </nav>

      <div class="min-h-[360px] flex-1 overflow-y-auto px-4 py-4 text-[12px]">
        <!-- Font -->
        <section v-show="tab === 'appearance'" class="mb-6">
          <h3 class="settings-section">Terminal font</h3>
          <div class="grid grid-cols-2 gap-3">
            <label class="flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
              Family (monospace only)
              <select
                :value="state.settings.fontFamily || FONT_DEFAULT"
                data-test="font-family"
                class="focus-ring rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 text-[13px] text-[var(--text)]"
                :disabled="loading"
                @change="pickFont"
              >
                <option value="">Default stack</option>
                <option v-for="family in fontOptions" :key="family" :value="family">{{ family }}</option>
              </select>
            </label>
            <label class="flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
              Size — {{ state.settings.fontSize }}px
              <input
                type="range"
                min="9"
                max="28"
                step="1"
                :value="state.settings.fontSize"
                class="focus-ring accent-[var(--accent-ink)]"
                @input="pickSize"
              />
            </label>
          </div>

          <p v-if="loading" class="mt-2 text-[11px] text-[var(--text-dim)]">Reading installed fonts…</p>
          <p v-else-if="loadError" class="mt-2 text-[11px] text-[var(--warn)]">{{ loadError }}</p>
          <p v-else class="mt-2 text-[11px] text-[var(--text-dim)]">
            {{ fontOptions.length }} fixed-pitch families installed on this machine. Proportional faces are left out on
            purpose — a terminal cell is a fixed rectangle.
          </p>

          <div
            class="mt-2 overflow-hidden rounded border border-[var(--border-soft)] bg-[var(--term-bg)] px-3 py-2 leading-relaxed text-[var(--term-fg)]"
            :style="previewStyle"
          >
            <div>Il1|0oO — 0O il1| 混淆字符看得清</div>
            <div>git commit -m 'fix: 终端字体' && make test</div>
            <div>drwxr-xr-x 12 root root 4096 Oct 23 04:11 etc</div>
          </div>
        </section>

        <!-- Block and box characters -->
        <section v-show="tab === 'appearance'" class="mb-6">
          <h3 class="settings-section">Block and box characters</h3>
          <label class="flex items-center gap-2 text-[12px] text-[var(--text)]">
            <input
              type="checkbox"
              data-test="continuous-glyphs"
              class="focus-ring accent-[var(--accent-ink)]"
              :checked="state.settings.continuousGlyphs"
              @change="toggleContinuousGlyphs"
            />
            Let GoTerm draw block and box characters edge to edge
          </label>
          <p class="mt-1 text-[11px] leading-snug text-[var(--text-dim)]">
            On, a TUI's frame lines meet with no gap — and a stacked bar chart such as
            <code class="text-[var(--text)]">dust</code> fuses into one solid shape, because every row of blocks is
            painted across the full line height. Off (the default), the font's own glyphs are used: each bar keeps its
            row, and frame lines show a hairline gap at this line spacing.
          </p>
        </section>

        <!-- Remote text encoding -->
        <section v-show="tab === 'appearance'" class="mb-6">
          <h3 class="settings-section">Remote text encoding</h3>
          <p class="mb-2 text-[11px] leading-snug text-[var(--text-dim)]">
            What the host's bytes are decoded as. UTF-8 covers every modern Linux and macOS box; a legacy Chinese,
            Taiwanese or Japanese host sends GBK/Big5/Shift_JIS instead, which arrives as mojibake until it is decoded
            here. Typing is always sent as UTF-8.
          </p>
          <label class="flex items-center gap-2 text-[12px] text-[var(--text)]">
            <select
              data-test="encoding"
              class="focus-ring rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 text-[13px] text-[var(--text)]"
              :value="state.settings.encoding"
              @change="applyEncoding"
            >
              <option v-for="choice in encodingChoices" :key="choice.id" :value="choice.id">{{ choice.label }}</option>
            </select>
            <span class="text-[11px] text-[var(--text-dim)]">{{ encodingHint }}</span>
          </label>
          <p class="mt-1 text-[11px] leading-snug text-[var(--text-dim)]">
            Only what the terminal <em>shows</em> changes; commands are unaffected. If a host needs GBK input as well,
            set <code class="text-[var(--text)]">export LANG=zh_CN.UTF-8</code> on it and stay on UTF-8.
          </p>
        </section>

        <!-- Word separator -->
        <section v-show="tab === 'behaviour'" class="mb-6">
          <h3 class="settings-section">Word separator</h3>
          <p class="mb-2 text-[11px] leading-snug text-[var(--text-dim)]">
            Characters that end a word when you double-click or triple-click a selection. Put the characters in, nothing
            else: a space here means a space is a word boundary.
          </p>
          <div class="flex items-end gap-2">
            <label class="flex min-w-0 flex-1 flex-col gap-1 text-[11px] text-[var(--text-dim)]">
              Separator characters
              <input
                v-model="separator"
                data-test="word-separator"
                spellcheck="false"
                autocomplete="off"
                class="focus-ring min-w-0 rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 font-mono text-[13px] text-[var(--text)]"
                @input="applySeparator"
              />
            </label>
            <button
              type="button"
              class="focus-ring flex items-center gap-1 rounded border border-[var(--border)] px-2 py-1.5 text-[12px] text-[var(--text-dim)] hover:text-[var(--text)]"
              @click="restoreSeparator"
            >
              <RotateCcw :size="12" /> Default
            </button>
          </div>
          <p class="mt-2 text-[11px] text-[var(--text-dim)]">
            <span v-if="separatorError" class="text-[var(--warn)]">{{ separatorError }}</span>
            <span v-else>
              Double-clicking
              <code class="font-mono text-[var(--text)]">/var/log/nginx/access.log:404</code>
              selects <code class="font-mono text-[var(--text)]">{{ separatorPreview || '—' }}</code>
            </span>
          </p>
        </section>

        <!-- Selection and paste -->
        <section v-show="tab === 'behaviour'" class="mb-6">
          <h3 class="settings-section">Selection and paste</h3>
          <label class="flex items-center gap-2 text-[12px] text-[var(--text)]">
            <input
              type="checkbox"
              data-test="copy-on-select"
              class="focus-ring accent-[var(--accent-ink)]"
              :checked="state.settings.copyOnSelect"
              @change="toggleCopyOnSelect"
            />
            Copy the selection as soon as I release the mouse
          </label>
          <p class="mt-1 text-[11px] leading-snug text-[var(--text-dim)]">
            Only a selection you dragged ends up on the clipboard — a repaint that happens to leave a selection behind
            never overwrites what you copied elsewhere. <code class="text-[var(--text)]">Ctrl+Shift+C</code> copies on
            demand either way.
          </p>
          <p class="mt-2 text-[11px] leading-snug text-[var(--text-dim)]">
            A pasted block of several lines is shown in an editable dialog before anything is sent, with the blank
            lines it started with stripped; <code class="text-[var(--text)]">Enter</code> pastes it,
            <code class="text-[var(--text)]">Shift+Enter</code> adds a line break,
            <code class="text-[var(--text)]">Escape</code> drops it. One line, with or without its Enter, goes straight
            through.
          </p>
        </section>

        <!-- Anti-idle -->
        <section v-show="tab === 'behaviour'" class="mb-6">
          <h3 class="settings-section">Idle sessions</h3>
          <p class="mb-2 text-[11px] leading-snug text-[var(--text-dim)]">
            GoTerm already sends an SSH keepalive every 45 s; that keeps the <em>connection</em> alive and is invisible
            to the shell. A login shell with <code class="text-[var(--text)]">TMOUT</code> set logs itself out anyway
            (<code class="text-[var(--text)]">timed out waiting for input: auto logout</code>), because that timer
            counts <em>input</em>, not traffic. This option supplies the input.
          </p>
          <label class="flex items-center gap-2 text-[12px] text-[var(--text)]">
            <input
              type="checkbox"
              data-test="idle-keepalive"
              class="focus-ring accent-[var(--accent-ink)]"
              :checked="state.settings.idleKeepAliveMinutes > 0"
              @change="toggleIdleKeepAlive"
            />
            Nudge the session while I am idle
            <template v-if="state.settings.idleKeepAliveMinutes > 0">
              after
              <input
                type="number"
                min="1"
                max="120"
                data-test="idle-minutes"
                :value="state.settings.idleKeepAliveMinutes"
                class="focus-ring w-14 rounded border border-[var(--border)] bg-[var(--bg)] px-1.5 py-1 text-[12px] text-[var(--text)]"
                @change="pickIdleMinutes"
              />
              minutes
            </template>
          </label>
          <label
            v-if="state.settings.idleKeepAliveMinutes > 0"
            class="mt-2 flex flex-wrap items-center gap-2 text-[12px] text-[var(--text)]"
          >
            by sending
            <select
              data-test="idle-nudge"
              class="focus-ring rounded border border-[var(--border)] bg-[var(--bg)] px-1.5 py-1 text-[12px] text-[var(--text)]"
              :value="state.settings.idleNudge"
              @change="pickIdleNudge"
            >
              <option value="space">a space and a backspace (invisible)</option>
              <option value="line">an empty line (a new prompt each time)</option>
              <option value="space-only">a space, no backspace (types into TUIs)</option>
            </select>
          </label>
          <p class="mt-1 text-[11px] leading-snug text-[var(--text-dim)]">
            <strong class="text-[var(--text)]">A space and a backspace</strong> is the default: two keystrokes that
            cancel each other out. The line is left exactly as you typed it, nothing can be submitted or run, and it
            still counts as input — so it works even when the prompt is a <code class="text-[var(--text)]">&gt;</code>
            continuation and you have half a command on screen.
          </p>
          <p class="mt-1 text-[11px] leading-snug text-[var(--text-dim)]">
            <strong class="text-[var(--text)]">An empty line</strong> is the louder fallback for a shell whose timer
            only resets on a completed line. It costs a new prompt each time, and it is never sent while you have text
            pending — an Enter there would run it.
          </p>
          <p class="mt-1 text-[11px] leading-snug text-[var(--text-dim)]">
            Neither is ever sent inside a full-screen program (vim, less, htop), during a transfer, or in the middle of
            an output burst — five seconds of quiet first. If a nudge makes the far side reprint a screenful — a
            bastion's “choose an asset” menu does — it stops for that session and says so instead of spamming the
            screen.
          </p>
          <p
            v-if="state.settings.idleNudge === 'space-only'"
            class="mt-1 text-[11px] leading-snug text-[var(--warn)]"
            data-test="nudge-tui-warning"
          >
            <strong class="text-[var(--warn)]">A space on its own is a keystroke that stays.</strong> It is the only
            mode that fires inside a full-screen program, because that is exactly what it does: type into it. That
            makes it right for a TUI whose input box has its own idle timer, and wrong for <code>less</code> (a space
            pages down), <code>fzf</code> (it selects the current item) or an editor (it inserts). The box it lands in
            keeps the space until you delete it — most chat inputs trim it when the message is sent.
          </p>
        </section>

        <!-- Session lifetime -->
        <section v-show="tab === 'behaviour'" class="mb-6">
          <h3 class="settings-section">Session lifetime</h3>
          <p class="mb-2 text-[11px] leading-snug text-[var(--text-dim)]">
            A connection is closed once it has been open this long, whatever is happening in it. The keepalive above
            keeps a session from dying of someone else's idle timer; it is not a way to hold a login open
            indefinitely, so this is a hard stop rather than another idle timer. Reconnecting starts a new session.
          </p>
          <label class="flex flex-wrap items-center gap-2 text-[12px] text-[var(--text)]">
            Close a session after
            <input
              type="number"
              min="1"
              max="24"
              data-test="session-hours"
              :value="state.settings.sessionHours"
              class="focus-ring w-14 rounded border border-[var(--border)] bg-[var(--bg)] px-1.5 py-1 text-[12px] text-[var(--text)]"
              @change="pickSessionHours"
            />
            hours
            <span class="text-[11px] text-[var(--text-dim)]">(1–24; a duplicated tab shares the same clock)</span>
          </label>
          <p class="mt-1 text-[11px] leading-snug text-[var(--text-dim)]">
            Go enforces it from the moment the connection was made: lowering this ends a session that is already past
            the new limit, and raising it does not restart anyone's clock.
          </p>
        </section>

        <!-- Storage -->
        <section v-show="tab === 'behaviour'" class="mb-6">
          <h3 class="settings-section">Storage</h3>
          <p class="mb-2 text-[11px] leading-snug text-[var(--text-dim)]">
            Saved hosts, pinned host keys and any sealed secrets live in this folder. Move it to a synced drive or a
            portable stick and the same hosts travel with you.
          </p>
          <div class="flex flex-wrap items-center gap-2 text-[12px] text-[var(--text)]">
            <code
              data-test="storage-dir"
              class="max-w-full break-all rounded bg-[var(--panel-2)] px-1.5 py-0.5 font-mono text-[11px]"
            >{{ storage?.dir ?? '…' }}</code>
            <button
              data-test="storage-change"
              class="focus-ring rounded border border-[var(--border)] px-2 py-1 text-[11px] hover:border-[var(--accent)] disabled:opacity-40"
              :disabled="!storage || storage.envOverride"
              @click="chooseStorage"
            >Change…</button>
            <button
              data-test="storage-default"
              class="focus-ring rounded border border-[var(--border)] px-2 py-1 text-[11px] hover:border-[var(--accent)] disabled:opacity-40"
              :disabled="!storage || storage.envOverride || !storage.configuredDir"
              @click="useDefaultStorage"
            >Use default</button>
          </div>
          <p v-if="storage" class="mt-1 text-[11px] leading-snug text-[var(--text-dim)]">
            Default: <code class="font-mono">{{ storage.defaultDir }}</code>
          </p>
          <p v-if="storage?.envOverride" data-test="storage-env" class="mt-1 text-[11px] leading-snug text-[var(--warn)]">
            <code class="font-mono">GOTERM_DIR</code> is set to
            <code class="font-mono">{{ storage.envDir }}</code>, so the environment decides and this setting does not
            apply while it is set.
          </p>
          <p
            v-if="storage?.restartPending"
            data-test="storage-pending"
            class="mt-1 text-[11px] leading-snug text-[var(--warn)]"
          >
            Next start: <code class="font-mono">{{ storage.pendingDir }}</code> — restart GoTerm to use it. Nothing is
            copied: the files stay where they are until you move them (or a new folder simply starts empty).
          </p>
          <p v-if="storageError" data-test="storage-error" class="mt-1 text-[11px] leading-snug text-[var(--warn)]">
            {{ storageError }}
          </p>
        </section>

        <!-- Background -->
        <section v-show="tab === 'appearance'" class="mb-6">
          <h3 class="settings-section">Terminal background</h3>
          <div class="flex flex-wrap items-center gap-2">
            <input
              type="color"
              data-test="background"
              :value="backgroundValue"
              class="h-7 w-10 cursor-pointer rounded border border-[var(--border)] bg-transparent p-0"
              @input="pickBackground"
            />
            <span class="font-mono text-[11px] text-[var(--text-dim)]">{{ state.settings.background || 'theme default' }}</span>
            <button
              type="button"
              class="focus-ring rounded border border-[var(--border)] px-2 py-1 text-[11px] text-[var(--text-dim)] hover:text-[var(--text)]"
              @click="resetBackground"
            >
              Use theme colour
            </button>
          </div>
          <div class="mt-2 flex flex-wrap gap-1.5">
            <button
              v-for="preset in BACKGROUND_PRESETS"
              :key="preset.value"
              type="button"
              class="focus-ring h-6 w-6 rounded border border-[var(--border)]"
              :style="{ background: preset.value }"
              :title="preset.label"
              @click="usePreset(preset.value)"
            />
          </div>
          <p v-if="contrastWarning" class="mt-2 text-[11px] leading-snug text-[var(--warn)]">{{ contrastWarning }}</p>
          <p v-else class="mt-2 text-[11px] text-[var(--text-dim)]">
            Only the pane changes; text, cursor and selection keep the theme's colours.
          </p>
        </section>

        <!-- Highlighting -->
        <section v-show="tab === 'highlighting'">
          <h3 class="settings-section">Keyword highlighting</h3>
          <p class="mb-2 text-[11px] leading-snug text-[var(--text-dim)]">
            Colours words in the output the way MobaXterm's filters do. Each set is applied to the lines it recognises,
            so a syslog line is not read as a shell prompt. Full-screen programs (vim, less) are left alone.
          </p>
          <div class="flex flex-col gap-2">
            <label
              v-for="set in HIGHLIGHT_SETS"
              :key="set.id"
              class="flex cursor-pointer items-start gap-2 rounded border border-[var(--border-soft)] bg-[var(--bg)] px-2.5 py-2"
            >
              <input
                type="checkbox"
                :data-test="`set-${set.id}`"
                class="focus-ring mt-0.5 accent-[var(--accent-ink)]"
                :checked="state.settings.highlight.includes(set.id)"
                @change="toggleSet(set.id, ($event.target as HTMLInputElement).checked)"
              />
              <span class="min-w-0">
                <span class="block text-[12px] text-[var(--text)]">{{ set.label }}</span>
                <span class="block text-[11px] leading-snug text-[var(--text-dim)]">{{ set.description }}</span>
              </span>
            </label>
          </div>
        </section>

        <!-- Custom keywords -->
        <section v-show="tab === 'highlighting'" class="mt-6">
          <h3 class="settings-section">Custom keywords</h3>
          <p class="mb-2 text-[11px] leading-snug text-[var(--text-dim)]">
            Your own words and patterns. They are checked before the built-in sets, so a custom colour wins.
            A keyword is matched whole-word and case-insensitively by default; a regex is used as written.
          </p>

          <form class="flex flex-wrap items-end gap-2" @submit.prevent="addRule">
            <label class="flex min-w-0 flex-1 flex-col gap-1 text-[11px] text-[var(--text-dim)]">
              Keyword or pattern
              <input
                v-model="draft.pattern"
                data-test="custom-pattern"
                spellcheck="false"
                autocomplete="off"
                class="focus-ring min-w-0 rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 font-mono text-[12px] text-[var(--text)]"
                placeholder="panic  or  \\bjob\\s*#?\\d+\\b"
              />
            </label>
            <label class="flex flex-col gap-1 text-[11px] text-[var(--text-dim)]">
              Colour
              <select
                v-model="draft.colour"
                data-test="custom-colour"
                class="focus-ring rounded border border-[var(--border)] bg-[var(--bg)] px-2 py-1.5 text-[12px] text-[var(--text)]"
              >
                <option v-for="colour in COLOURS" :key="colour" :value="colour">{{ colour }}</option>
              </select>
            </label>
            <label class="flex items-center gap-1 pb-1.5 text-[11px] text-[var(--text-dim)]">
              <input v-model="draft.regex" type="checkbox" class="focus-ring accent-[var(--accent-ink)]" /> regex
            </label>
            <label class="flex items-center gap-1 pb-1.5 text-[11px] text-[var(--text-dim)]" title="Ignore case">
              <input v-model="draft.ignoreCase" type="checkbox" class="focus-ring accent-[var(--accent-ink)]" /> Aa
            </label>
            <button
              type="submit"
              class="focus-ring rounded bg-[var(--accent)] px-2.5 py-1.5 text-[12px] font-medium text-[var(--accent-ink)]"
            >
              Add
            </button>
          </form>
          <p v-if="draftError" class="mt-1 text-[11px] text-[var(--err)]">{{ draftError }}</p>

          <div class="mt-3 flex flex-wrap items-center gap-2">
            <button
              type="button"
              class="focus-ring flex items-center gap-1.5 rounded border border-[var(--border)] px-2 py-1.5 text-[11px] text-[var(--text-dim)] hover:text-[var(--text)]"
              title="Read a MobaXterm .msyn file and add every rule it can convert"
              @click="importMsyn"
            >
              Import MobaXterm .msyn…
            </button>
            <span class="text-[11px] text-[var(--text-dim)]">{{ customRules.length }} rule(s)</span>
          </div>
          <p v-if="importSummary" class="mt-1 text-[11px] text-[var(--text)]">{{ importSummary }}</p>
          <p v-if="importError" class="mt-1 text-[11px] text-[var(--err)]">{{ importError }}</p>
          <ul v-if="importSkipped.length" class="mt-1 space-y-0.5 text-[10px] text-[var(--text-dim)]">
            <li v-for="skip in importSkipped" :key="skip.source" class="truncate-1 font-mono" :title="skip.reason">
              skipped: {{ skip.source }}
            </li>
          </ul>
          <p v-if="ruleErrors.length" class="mt-1 text-[11px] text-[var(--err)]">
            {{ ruleErrors.length }} rule(s) do not compile — {{ ruleErrors[0] }}
          </p>

          <ul v-if="customRules.length" class="mt-2 flex max-h-[240px] flex-col gap-1 overflow-y-auto">
            <li
              v-for="rule in customRules"
              :key="rule.id"
              class="flex items-center gap-2 rounded border border-[var(--border-soft)] bg-[var(--bg)] px-2 py-1"
            >
              <span class="h-2.5 w-2.5 shrink-0 rounded-full" :style="{ background: swatch(rule.colour) }" />
              <code class="min-w-0 flex-1 truncate-1 font-mono text-[11px] text-[var(--text)]" :title="rule.pattern">{{ rule.pattern }}</code>
              <span class="shrink-0 text-[10px] uppercase text-[var(--text-dim)]">
                {{ rule.regex ? 'regex' : 'word' }}{{ rule.ignoreCase ? ' · i' : '' }}
              </span>
              <select
                :value="rule.colour"
                class="focus-ring rounded border border-[var(--border)] bg-[var(--panel)] px-1 py-0.5 text-[10px] text-[var(--text-dim)]"
                @change="setRuleColour(rule.id, ($event.target as HTMLSelectElement).value as HighlightColour)"
              >
                <option v-for="colour in COLOURS" :key="colour" :value="colour">{{ colour }}</option>
              </select>
              <input
                type="checkbox"
                class="focus-ring accent-[var(--accent-ink)]"
                :checked="rule.enabled"
                :title="rule.enabled ? 'Disable' : 'Enable'"
                @change="toggleRule(rule.id, ($event.target as HTMLInputElement).checked)"
              />
              <button
                type="button"
                class="focus-ring rounded p-0.5 text-[var(--text-dim)] hover:text-[var(--err)]"
                title="Remove"
                @click="removeRule(rule.id)"
              >
                <Trash2 :size="12" />
              </button>
            </li>
          </ul>
        </section>
      </div>

      <footer class="flex shrink-0 items-center gap-2 border-t border-[var(--border)] px-4 py-2.5">
        <span class="text-[11px] text-[var(--text-dim)]">Saved as you change it.</span>
        <button
          type="button"
          class="focus-ring ml-auto rounded bg-[var(--accent)] px-3 py-1.5 text-[12px] font-medium text-[var(--accent-ink)]"
          @click="emit('close')"
        >
          Done
        </button>
      </footer>
    </div>
  </div>
</template>
