/*
 * Settings persistence: what survives a restart, and what is refused on the way
 * back in.
 *
 * These load the store as a fresh module, because the interesting part happens
 * once, at startup, when localStorage is read.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

const KEY = 'goterm.settings';

async function freshStore() {
  vi.resetModules();
  return import('./store');
}

beforeEach(() => {
  localStorage.clear();
});

describe('settings on disk', () => {
  it('starts from defaults that include the data-shape set', async () => {
    const store = await freshStore();
    expect(store.state.settings.highlight).toContain('data');
    expect(store.state.settings.background).toBe('');
    expect(store.state.settings.customRules).toEqual([]);
    expect(store.state.settings.fontSize).toBe(14);
  });

  it('turns the data set on once for settings saved before it existed', async () => {
    localStorage.setItem(KEY, JSON.stringify({ theme: 'light', fontSize: 13, highlight: ['shell', 'nginx'] }));
    const store = await freshStore();
    expect(store.state.settings.highlight).toEqual(['shell', 'nginx', 'data']);
    expect(store.state.settings.fontSize).toBe(13);
    expect(store.state.settings.theme).toBe('light');
  });

  it('does not resurrect the data set after the user switched it off', async () => {
    localStorage.setItem(
      KEY,
      JSON.stringify({ settingsVersion: 2, theme: 'dark', highlight: ['shell'], wordSeparator: ' ' }),
    );
    const store = await freshStore();
    expect(store.state.settings.highlight).toEqual(['shell']);
  });

  it('drops a background that is not a colour', async () => {
    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, background: 'javascript:alert(1)' }));
    const store = await freshStore();
    expect(store.state.settings.background).toBe('');
  });

  it('keeps copy-on-select on unless it was switched off', async () => {
    // On by default, because selecting is how a terminal user copies; a saved
    // `false` is a deliberate choice and has to survive.
    const first = await freshStore();
    expect(first.state.settings.copyOnSelect).toBe(true);

    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, copyOnSelect: false }));
    const second = await freshStore();
    expect(second.state.settings.copyOnSelect).toBe(false);

    // A settings file written before the option existed keeps the default.
    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, theme: 'dark' }));
    const third = await freshStore();
    expect(third.state.settings.copyOnSelect).toBe(true);
  });

  it('keeps the anti-idle nudge to the three things the terminal can send', async () => {
    // A space+backspace is the safe default. `line` and `space-only` are deliberate
    // choices and have to survive; anything else is not a nudge at all.
    const first = await freshStore();
    expect(first.state.settings.idleNudge).toBe('space');

    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, idleNudge: 'line' }));
    const second = await freshStore();
    expect(second.state.settings.idleNudge).toBe('line');

    // The one that works inside a TUI is a setting like any other.
    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, idleNudge: 'space-only' }));
    const between = await freshStore();
    expect(between.state.settings.idleNudge).toBe('space-only');

    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, idleNudge: 'nul' }));
    const third = await freshStore();
    expect(third.state.settings.idleNudge).toBe('space');

    // A settings file written before the option existed keeps the default.
    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, theme: 'dark' }));
    const fourth = await freshStore();
    expect(fourth.state.settings.idleNudge).toBe('space');
  });

  it('rebuilds custom rules safely', async () => {
    localStorage.setItem(
      KEY,
      JSON.stringify({
        settingsVersion: 2,
        customRules: [
          { pattern: 'panic', colour: 'red' },
          { pattern: '', colour: 'red' },
          { pattern: 'x', colour: 'not-a-colour', regex: true },
          'nonsense',
          null,
        ],
      }),
    );
    const store = await freshStore();
    expect(store.state.settings.customRules).toHaveLength(2);
    expect(store.state.settings.customRules[0]).toMatchObject({
      pattern: 'panic',
      colour: 'red',
      enabled: true,
      ignoreCase: true,
      regex: false,
    });
    // An unknown colour becomes yellow rather than undefined.
    expect(store.state.settings.customRules[1]).toMatchObject({ pattern: 'x', colour: 'yellow', regex: true });
  });

  it('persists a change immediately', async () => {
    const store = await freshStore();
    store.setBackgroundColor('#101820');
    store.setCustomRules([
      { id: 'r1', pattern: 'boom', colour: 'red', regex: false, ignoreCase: true, enabled: true },
    ]);
    store.setIdleNudge('line');

    const raw = JSON.parse(localStorage.getItem(KEY) ?? '{}') as {
      background?: string;
      customRules?: { pattern: string }[];
      idleNudge?: string;
    };
    expect(raw.background).toBe('#101820');
    expect(raw.customRules?.[0]?.pattern).toBe('boom');
    expect(raw.idleNudge).toBe('line');
  });

  it('keeps the app’s terminal surfaces readable on a custom pane', async () => {
    // The same rule the terminal follows: the pane decides the ink, so a black
    // pane under the light theme is not black text on black.
    const store = await freshStore();
    const root = document.documentElement;

    store.setTheme('light');
    store.setBackgroundColor('#000000');
    expect(root.style.getPropertyValue('--term-bg')).toBe('#000000');
    expect(root.style.getPropertyValue('--term-fg')).toBe('#d7e0ea');

    // Clearing hands both back to the stylesheet, which is the theme's own pair.
    store.setBackgroundColor('');
    expect(root.style.getPropertyValue('--term-bg')).toBe('');
    expect(root.style.getPropertyValue('--term-fg')).toBe('');
  });

  it('turning anti-idle on from a timeout notice promises the safe nudge', async () => {
    const store = await freshStore();
    store.enableAntiIdle(3);

    expect(store.state.settings.idleKeepAliveMinutes).toBe(3);
    const message = store.state.notices.at(-1)?.message ?? '';
    expect(message).toMatch(/space and a backspace/);
    // It never quietly turns into the nudge that submits a line.
    expect(message).not.toMatch(/empty line every/);

    // With the louder nudge chosen, the notice says what it will actually send.
    store.setIdleNudge('line');
    store.enableAntiIdle(5);
    expect(store.state.notices.at(-1)?.message).toMatch(/an empty line every 5 min/);

    // And the bare space says so too — it is the one that leaves a character.
    store.setIdleNudge('space-only');
    store.enableAntiIdle(4);
    const bare = store.state.notices.at(-1)?.message ?? '';
    expect(bare).toMatch(/Anti-idle on: a space \(/);
    expect(bare).toMatch(/stays in the input box/);

    // An unknown value cannot ask for a nudge the terminal will not send.
    store.setIdleNudge('weird' as never);
    expect(store.state.settings.idleNudge).toBe('space');
  });
});

describe('block and box glyphs', () => {
  it('starts with stacked bars separated, and keeps a saved choice', async () => {
    // Fused `dust` bars are the bug this default avoids, so the fusing rendering
    // has to be the one the user asks for.
    const first = await freshStore();
    expect(first.state.settings.continuousGlyphs).toBe(false);

    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, continuousGlyphs: true }));
    const second = await freshStore();
    expect(second.state.settings.continuousGlyphs).toBe(true);

    // Something that is not a boolean is not a choice anybody made.
    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, continuousGlyphs: 'edge-to-edge' }));
    const third = await freshStore();
    expect(third.state.settings.continuousGlyphs).toBe(false);
  });

  it('writes the choice to disk and hands it back on reset', async () => {
    const store = await freshStore();
    store.setContinuousGlyphs(true);
    expect(JSON.parse(localStorage.getItem(KEY) ?? '{}').continuousGlyphs).toBe(true);

    store.resetSettings();
    expect(store.state.settings.continuousGlyphs).toBe(false);
  });
});

describe('session lifetime', () => {
  it('starts at 12 hours — never at "no limit"', async () => {
    const store = await freshStore();
    expect(store.state.settings.sessionHours).toBe(12);
  });

  it('clamps what a hand-edited file says', async () => {
    // There is no way to ask for an endless session, in either direction.
    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, sessionHours: 99 }));
    expect((await freshStore()).state.settings.sessionHours).toBe(24);

    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, sessionHours: 0 }));
    expect((await freshStore()).state.settings.sessionHours).toBe(12);

    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, sessionHours: 'forever' }));
    expect((await freshStore()).state.settings.sessionHours).toBe(12);

    localStorage.setItem(KEY, JSON.stringify({ settingsVersion: 2, sessionHours: 2.6 }));
    expect((await freshStore()).state.settings.sessionHours).toBe(3);
  });

  it('persists the chosen value, clamps on the way in, and resets to the default', async () => {
    const store = await freshStore();

    store.setSessionHours(2);
    expect(store.state.settings.sessionHours).toBe(2);
    expect(JSON.parse(localStorage.getItem(KEY) ?? '{}').sessionHours).toBe(2);

    store.setSessionHours(40);
    expect(store.state.settings.sessionHours).toBe(24);
    expect(JSON.parse(localStorage.getItem(KEY) ?? '{}').sessionHours).toBe(24);

    store.setSessionHours(0);
    expect(store.state.settings.sessionHours).toBe(12);

    store.setSessionHours(6);
    store.resetSettings();
    expect(store.state.settings.sessionHours).toBe(12);
  });
});
