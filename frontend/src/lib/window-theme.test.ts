/*
 * The app theme has to reach the OS-drawn window frame, or a dark GoTerm sits
 * under a white title bar on a light-mode Windows. These tests watch the single
 * call that does it, including the case where there is no Go to call.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest';

const { setWindowTheme } = vi.hoisted(() => ({ setWindowTheme: vi.fn() }));

vi.mock('./backend', () => ({
  backend: { setWindowTheme },
  errorMessage: (error: unknown) => String(error),
  fileBridgeApi: () => null,
}));

async function freshStore() {
  vi.resetModules();
  return import('./store');
}

beforeEach(() => {
  localStorage.clear();
  setWindowTheme.mockReset();
  setWindowTheme.mockResolvedValue(undefined);
});

describe('the frame follows the app theme', () => {
  it('tells the backend on every theme switch', async () => {
    const store = await freshStore();

    store.setTheme('light');
    expect(setWindowTheme).toHaveBeenLastCalledWith(false);

    store.setTheme('dark');
    expect(setWindowTheme).toHaveBeenLastCalledWith(true);

    // And the setting is still the persisted one, frame call or not.
    expect(JSON.parse(localStorage.getItem('goterm.settings') ?? '{}')).toMatchObject({ theme: 'dark' });
  });

  it('survives a frame it cannot repaint', async () => {
    // No bridge (a plain browser, a headless start) or an OS that refuses the
    // attribute: the theme switch itself must still go through.
    setWindowTheme.mockRejectedValue(new Error('no window'));
    const store = await freshStore();

    store.setTheme('light');
    await Promise.resolve();

    expect(store.state.settings.theme).toBe('light');
    expect(document.documentElement.dataset.theme).toBe('light');
    expect(store.state.notices.length).toBe(0);
  });
});
