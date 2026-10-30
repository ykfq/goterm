/*
 * The settings screen writes through to the terminal, not into a form that is
 * forgotten on close. These tests mount the real component against the real
 * store and check the values that actually reach the next terminal.
 */
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const backendMock = vi.hoisted(() => ({
  fonts: vi.fn(async () => [
    { name: 'Consolas', mono: true },
    { name: 'JetBrains Mono', mono: true },
  ]),
  fileBridgeApi: vi.fn(),
  // The session-lifetime setting is pushed to Go as well as stored; without this
  // the setter would throw here rather than quietly falling back.
  setSessionLifetime: vi.fn(async (hours: number) => hours),
  // Storage lives in Go: the dialog only renders it and asks for changes.
  storage: vi.fn(async () => storageInfo()),
  setStorageDir: vi.fn(async (dir: string) =>
    storageInfo({
      configuredDir: dir,
      pendingDir: dir || 'C:/Users/me/AppData/Roaming/goterm',
      restartPending: dir !== '',
    }),
  ),
  pickStorageDir: vi.fn(async () => 'D:/goterm-state'),
}));

const STORAGE_DEFAULT = 'C:/Users/me/AppData/Roaming/goterm';

function storageInfo(over: Partial<Record<string, unknown>> = {}) {
  return {
    dir: STORAGE_DEFAULT,
    defaultDir: STORAGE_DEFAULT,
    configuredDir: '',
    pendingDir: STORAGE_DEFAULT,
    envDir: '',
    envOverride: false,
    restartPending: false,
    ...over,
  };
}

vi.mock('../lib/backend', () => ({
  backend: backendMock,
  fileBridgeApi: () => backendMock.fileBridgeApi(),
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import SettingsDialog from './SettingsDialog.vue';
import { bytesToBase64, textToUtf8 } from '../lib/bytes';
import { resetSettings, state } from '../lib/store';
import { DEFAULT_WORD_SEPARATOR } from '../lib/terminal';
import { DEFAULT_HIGHLIGHT_SETS, HIGHLIGHT_SETS } from '../lib/highlight';

beforeEach(() => {
  backendMock.fonts.mockClear();
  backendMock.fileBridgeApi.mockReset();
  backendMock.storage.mockClear();
  backendMock.setStorageDir.mockClear();
  backendMock.pickStorageDir.mockClear();
  resetSettings();
  localStorage.clear();
});

async function mountDialog() {
  const wrapper = mount(SettingsDialog);
  await flushPromises();
  return wrapper;
}

describe('SettingsDialog', () => {
  it('offers the monospace families the backend found', async () => {
    const wrapper = await mountDialog();
    const options = wrapper.findAll('option').map((option) => option.text());
    expect(options).toContain('Default stack');
    expect(options).toContain('Consolas');
    expect(options).toContain('JetBrains Mono');
  });

  it('applies and persists a chosen font', async () => {
    const wrapper = await mountDialog();
    const select = wrapper.find('[data-test="font-family"]');
    await select.setValue('JetBrains Mono');

    expect(state.settings.fontFamily).toBe('JetBrains Mono');
    const stored = JSON.parse(localStorage.getItem('goterm.settings') ?? '{}') as { fontFamily?: string };
    expect(stored.fontFamily).toBe('JetBrains Mono');
    // The preview is rendered in the chosen family.
    expect(wrapper.find('[style*="JetBrains Mono"]').exists()).toBe(true);
  });

  it('changes the word separator, and refuses to make it empty', async () => {
    const wrapper = await mountDialog();
    const input = wrapper.find('[data-test="word-separator"]');

    await input.setValue(' .:-/');
    expect(state.settings.wordSeparator).toBe(' .:-/');

    await input.setValue('');
    // Still the last usable value: an empty separator would select whole lines.
    expect(state.settings.wordSeparator).toBe(' .:-/');
    expect(wrapper.text()).toContain('whole lines');

    // Characters that would break a naive character class must not break the
    // live preview either.
    await input.setValue('[.-\\');
    expect(state.settings.wordSeparator).toBe('[.-\\');
    expect(wrapper.text()).toContain('selects');
  });

  it('turns each highlight set on and off', async () => {
    const wrapper = await mountDialog();
    for (const set of HIGHLIGHT_SETS) {
      expect(wrapper.find(`[data-test="set-${set.id}"]`).exists()).toBe(true);
    }

    // All on by default; switch nginx off.
    const nginx = wrapper.find('[data-test="set-nginx"]');
    await nginx.setValue(false);
    expect(state.settings.highlight).not.toContain('nginx');
    expect(state.settings.highlight).toContain('shell');

    await nginx.setValue(true);
    expect(state.settings.highlight).toContain('nginx');
  });

  it('resets everything to the defaults', async () => {
    const wrapper = await mountDialog();
    await wrapper.find('[data-test="font-family"]').setValue('Consolas');
    await wrapper.find('[data-test="word-separator"]').setValue('|');

    const reset = wrapper.findAll('button').find((button) => button.text().includes('Reset'));
    if (!reset) throw new Error('no Reset button');
    await reset.trigger('click');

    expect(state.settings.fontFamily).toBe('');
    expect(state.settings.wordSeparator).toBe(DEFAULT_WORD_SEPARATOR);
    expect(state.settings.highlight).toEqual(DEFAULT_HIGHLIGHT_SETS);
  });

    it('turns the anti-idle on with a sane default, and off again', async () => {
    const wrapper = await mountDialog();
    const box = wrapper.find('[data-test="idle-keepalive"]');

    await box.setValue(true);
    expect(state.settings.idleKeepAliveMinutes).toBe(3);

    await wrapper.find('[data-test="idle-minutes"]').setValue('10');
    expect(state.settings.idleKeepAliveMinutes).toBe(10);

    await box.setValue(false);
    expect(state.settings.idleKeepAliveMinutes).toBe(0);
  });

  it('lets the nudge be a space or an empty line, and only when it is on', async () => {
    const wrapper = await mountDialog();

    // Nothing to choose while the anti-idle is off.
    expect(wrapper.find('[data-test="idle-nudge"]').exists()).toBe(false);

    await wrapper.find('[data-test="idle-keepalive"]').setValue(true);
    const select = wrapper.find('[data-test="idle-nudge"]');
    expect(select.exists()).toBe(true);
    expect(state.settings.idleNudge).toBe('space');

    await select.setValue('line');
    expect(state.settings.idleNudge).toBe('line');
    const saved = JSON.parse(localStorage.getItem('goterm.settings') ?? '{}') as { idleNudge?: string };
    expect(saved.idleNudge).toBe('line');

    await select.setValue('space');
    expect(state.settings.idleNudge).toBe('space');
  });

  it('names the cost of the bare space when it is chosen', async () => {
    // The mode that fires inside a TUI is the one that leaves a character, so the
    // dialog has to say that at the moment it is chosen, not in a manual.
    const wrapper = await mountDialog();
    await wrapper.find('[data-test="idle-keepalive"]').setValue(true);
    const select = wrapper.find('[data-test="idle-nudge"]');

    expect(wrapper.find('[data-test="nudge-tui-warning"]').exists()).toBe(false);

    await select.setValue('space-only');
    expect(state.settings.idleNudge).toBe('space-only');
    const warning = wrapper.find('[data-test="nudge-tui-warning"]');
    expect(warning.exists()).toBe(true);
    expect(warning.text()).toContain('keystroke');

    await select.setValue('space');
    expect(wrapper.find('[data-test="nudge-tui-warning"]').exists()).toBe(false);
  });

  it('shows where state lives, and records a new folder for the next start', async () => {
    const wrapper = await mountDialog();
    expect(wrapper.find('[data-test="storage-dir"]').text()).toBe(STORAGE_DEFAULT);
    expect(wrapper.find('[data-test="storage-pending"]').exists()).toBe(false);

    await wrapper.find('[data-test="storage-change"]').trigger('click');
    await flushPromises();

    expect(backendMock.pickStorageDir).toHaveBeenCalledTimes(1);
    expect(backendMock.setStorageDir).toHaveBeenCalledWith('D:/goterm-state');
    // The change is about the next start, and the dialog says so.
    const pending = wrapper.find('[data-test="storage-pending"]');
    expect(pending.exists()).toBe(true);
    expect(pending.text()).toContain('D:/goterm-state');
    expect(state.notices.at(-1)?.message).toMatch(/Restart GoTerm/);

    // "Use default" asks Go for the default folder again.
    await wrapper.find('[data-test="storage-default"]').trigger('click');
    await flushPromises();
    expect(backendMock.setStorageDir).toHaveBeenLastCalledWith('');
  });

  it('says the environment wins, and refuses to promise otherwise', async () => {
    backendMock.storage.mockResolvedValueOnce(
      storageInfo({ envOverride: true, envDir: 'E:/portable-state', dir: 'E:/portable-state', pendingDir: 'E:/portable-state' }),
    );
    const wrapper = await mountDialog();

    const env = wrapper.find('[data-test="storage-env"]');
    expect(env.exists()).toBe(true);
    expect(env.text()).toContain('E:/portable-state');
    expect(wrapper.find('[data-test="storage-change"]').attributes('disabled')).toBeDefined();
    expect(wrapper.find('[data-test="storage-default"]').attributes('disabled')).toBeDefined();
  });

  it('turns copy-on-select off and back on', async () => {
    const wrapper = await mountDialog();
    const box = wrapper.find('[data-test="copy-on-select"]');
    expect(state.settings.copyOnSelect).toBe(true);

    await box.setValue(false);
    expect(state.settings.copyOnSelect).toBe(false);
    expect(JSON.parse(localStorage.getItem('goterm.settings') ?? '{}').copyOnSelect).toBe(false);

    await box.setValue(true);
    expect(state.settings.copyOnSelect).toBe(true);
  });

  it('turns edge-to-edge block characters on and back off', async () => {
    // Off is the default because on is what fuses a `dust` chart into one shape;
    // the control exists for the frame-line trade-off, not for the default.
    const wrapper = await mountDialog();
    const box = wrapper.find('[data-test="continuous-glyphs"]');
    expect(state.settings.continuousGlyphs).toBe(false);

    await box.setValue(true);
    expect(state.settings.continuousGlyphs).toBe(true);
    expect(JSON.parse(localStorage.getItem('goterm.settings') ?? '{}').continuousGlyphs).toBe(true);

    await box.setValue(false);
    expect(state.settings.continuousGlyphs).toBe(false);
  });

  it('changes how remote bytes are decoded, and remembers it', async () => {
    // The dropdown is the only way to read a GBK host; it has to reach the store
    // and survive a restart, or the next launch is mojibake again.
    const wrapper = await mountDialog();
    const select = wrapper.get('[data-test="encoding"]');
    expect(state.settings.encoding).toBe('utf-8');
    expect(wrapper.findAll('option').map((option) => option.text()).join(' | ')).toContain('GB18030');

    await select.setValue('gb18030');
    expect(state.settings.encoding).toBe('gb18030');
    expect(JSON.parse(localStorage.getItem('goterm.settings') ?? '{}').encoding).toBe('gb18030');
  });

  it('sets the session lifetime, and tells Go what it now enforces', async () => {
    // The number is not decoration: Go holds the cap, so the dialog has to push
    // it. A value that only lived in localStorage would look right and do nothing.
    // `bridgeReady` is what marks "there is a Go behind this window" — outside
    // that, pushing is pointless and is skipped on purpose.
    state.bridgeReady = true;
    try {
      const wrapper = await mountDialog();
      const field = wrapper.get('[data-test="session-hours"]');
      expect((field.element as HTMLInputElement).value).toBe('12');

      await field.setValue('6');
      expect(state.settings.sessionHours).toBe(6);
      expect(JSON.parse(localStorage.getItem('goterm.settings') ?? '{}').sessionHours).toBe(6);
      expect(backendMock.setSessionLifetime).toHaveBeenCalledWith(6);
    } finally {
      state.bridgeReady = false;
    }
  });

  it('sets the terminal background and paints the pane with it', async () => {
    const wrapper = await mountDialog();
    await wrapper.find('[data-test="background"]').setValue('#002b36');

    expect(state.settings.background).toBe('#002b36');
    expect(document.documentElement.style.getPropertyValue('--term-bg')).toBe('#002b36');

    const presets = wrapper.findAll('button[title="Solarized light"]');
    await presets[0]?.trigger('click');
    expect(state.settings.background).toBe('#fdf6e3');

    const reset = wrapper.findAll('button').find((button) => button.text() === 'Use theme colour');
    if (!reset) throw new Error('no background reset');
    await reset.trigger('click');
    expect(state.settings.background).toBe('');
    expect(document.documentElement.style.getPropertyValue('--term-bg')).toBe('');
  });

  it('warns when the pane fights the theme', async () => {
    const wrapper = await mountDialog();
    // The dark theme with a near-white pane.
    await wrapper.find('[data-test="background"]').setValue('#f4ecd8');
    expect(wrapper.text()).toContain('light background under the dark theme');
  });

  it('adds and removes a custom keyword', async () => {
    const wrapper = await mountDialog();
    await wrapper.find('[data-test="custom-pattern"]').setValue('panic');
    await wrapper.find('[data-test="custom-colour"]').setValue('red');

    const add = wrapper.findAll('button').find((button) => button.text() === 'Add');
    if (!add) throw new Error('no Add button');
    await wrapper.find('form').trigger('submit');

    expect(state.settings.customRules).toHaveLength(1);
    expect(state.settings.customRules[0]).toMatchObject({ pattern: 'panic', colour: 'red', regex: false, enabled: true });

    const remove = wrapper.find('ul button[title="Remove"]');
    await remove.trigger('click');
    expect(state.settings.customRules).toHaveLength(0);
  });

  it('refuses a regex that does not compile', async () => {
    const wrapper = await mountDialog();
    await wrapper.find('[data-test="custom-pattern"]').setValue('(unclosed');
    // Tick the "regex" box by its label text.
    const regexLabel = wrapper.findAll('label').find((label) => label.text().trim().startsWith('regex'));
    if (!regexLabel) throw new Error('no regex toggle');
    await regexLabel.find('input').setValue(true);

    await wrapper.find('form').trigger('submit');

    expect(state.settings.customRules).toHaveLength(0);
    expect(wrapper.text()).toContain('does not compile');
  });

  it('imports a MobaXterm .msyn file through the Go-backed picker', async () => {
    const msyn = [
      '[CustomSyntax]',
      'Name=Custom: test set',
      'UseRegex=1',
      'CaseSensitive=0',
      'Red=[^A-Za-z_&-](rejected|refused)[^A-Za-z_-]',
      'Cyan=([^A-Za-z_&-](notice)[^A-Za-z_-])|(unclosed',
    ].join('\r\n');
    backendMock.fileBridgeApi.mockReturnValue({
      chooseFiles: async () => ['C:/docs/test.msyn'],
      stat: async () => ({ name: 'test.msyn', dir: false, size: msyn.length }),
      readRange: async () => bytesToBase64(textToUtf8(msyn)),
      chooseDirectory: async () => '',
      list: async () => [],
      writeChunk: async () => undefined,
      createFile: async () => undefined,
      mkdir: async () => undefined,
    });

    const wrapper = await mountDialog();
    const button = wrapper.findAll('button').find((candidate) => candidate.text().includes('Import MobaXterm'));
    if (!button) throw new Error('no import button');
    await button.trigger('click');
    await flushPromises();

    const patterns = state.settings.customRules.map((rule) => rule.pattern);
    expect(patterns).toContain('[^A-Za-z_&-](rejected|refused)[^A-Za-z_-]');
    expect(patterns).toContain('([^A-Za-z_&-](notice)[^A-Za-z_-])');
    // The damaged branch was dropped, not fatal.
    expect(patterns).not.toContain('(unclosed');
    expect(wrapper.text()).toContain('Imported 2 rule(s)');
  });
});

describe('section tabs', () => {
  /** v-show writes `display: none` on the section itself; that is the contract. */
  const shown = (wrapper: Awaited<ReturnType<typeof mountDialog>>, selector: string): boolean => {
    const section = wrapper.get(selector).element.closest('section') as HTMLElement | null;
    return section !== null && section.style.display !== 'none';
  };

  it('opens on Appearance and hides the sections that are not in it', async () => {
    const wrapper = await mountDialog();

    expect(wrapper.findAll('[role="tab"]').map((item) => item.text())).toEqual([
      'Appearance',
      'Behaviour',
      'Highlighting',
    ]);
    expect(wrapper.get('[data-test="settings-tab-appearance"]').attributes('aria-selected')).toBe('true');

    expect(shown(wrapper, '[data-test="font-family"]')).toBe(true);
    expect(shown(wrapper, '[data-test="background"]')).toBe(true);
    expect(shown(wrapper, '[data-test="word-separator"]')).toBe(false);
    expect(shown(wrapper, '[data-test="custom-pattern"]')).toBe(false);
  });

  it('labels its sections in sentence case and in the accent colour', async () => {
    const wrapper = await mountDialog();
    const headings = wrapper.findAll('h3');
    // Every group of controls has one, across all three tabs (v-show keeps them
    // all in the DOM).
    expect(headings.length).toBeGreaterThanOrEqual(7);
    for (const heading of headings) {
      // One shared class rather than a utility string per heading, so the style
      // cannot drift between them — and no `uppercase`, which is what made the
      // labels read as captions.
      expect(heading.classes()).toContain('settings-section');
      expect(heading.classes().join(' ')).not.toContain('uppercase');
      expect(heading.text()).toMatch(/[a-z]/);
    }
  });

  it('switches section without throwing the others away', async () => {
    const wrapper = await mountDialog();

    await wrapper.get('[data-test="settings-tab-highlighting"]').trigger('click');

    expect(wrapper.get('[data-test="settings-tab-highlighting"]').attributes('aria-selected')).toBe('true');
    expect(shown(wrapper, '[data-test="custom-pattern"]')).toBe(true);
    expect(shown(wrapper, '[data-test="font-family"]')).toBe(false);
    // Still mounted: a half-typed custom rule must survive a look at another tab.
    expect(wrapper.find('[data-test="font-family"]').exists()).toBe(true);
  });

  it('reopens on the section last used', async () => {
    localStorage.setItem('goterm.settingsTab', 'behaviour');
    const wrapper = await mountDialog();

    expect(wrapper.get('[data-test="settings-tab-behaviour"]').attributes('aria-selected')).toBe('true');
    expect(shown(wrapper, '[data-test="idle-keepalive"]')).toBe(true);
    expect(shown(wrapper, '[data-test="font-family"]')).toBe(false);
  });
});
