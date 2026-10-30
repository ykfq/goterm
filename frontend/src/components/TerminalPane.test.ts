/*
 * The terminal context menu: it opens on right-click and *every* item actually
 * does something.
 *
 * This exists because the first version looked right and was completely dead —
 * a capture-phase window listener closed the menu on pointerdown, before the
 * item's own click handler could run. A screenshot cannot catch that, so the
 * items are clicked here.
 */
import { mount } from '@vue/test-utils';
import { nextTick, reactive } from 'vue';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';


const bridge = {
  el: document.createElement('div'),
  term: {
    selectAll: vi.fn(),
    hasSelection: vi.fn(() => true),
    getSelection: vi.fn(() => 'selected text'),
    // What the program running inside asked for: 'none' until it enables mouse
    // reporting, which every full-screen TUI does.
    modes: { mouseTrackingMode: 'none' as 'none' | 'x10' | 'vt200' | 'drag' | 'any' },
  },
  trzsz: {
    isTransferringFiles: vi.fn(() => false),
    stopTransferringFiles: vi.fn(),
  },
  attach: vi.fn(),
  write: vi.fn(),
  fitNow: vi.fn(() => ({ cols: 100, rows: 30 })),
  focus: vi.fn(),
  setTheme: vi.fn(),
  setFontSize: vi.fn(),
  copySelection: vi.fn(() => 'selected text'),
  pasteText: vi.fn(),
  clear: vi.fn(),
  findNext: vi.fn(() => true),
  findPrevious: vi.fn(() => true),
  clearSearch: vi.fn(),
  uploadDropped: vi.fn(),
  requestUpload: vi.fn(() => true),
  dispose: vi.fn(),
};

const store = {
  state: { settings: { fontSize: 14 }, activeTabId: 'tab-1' },
  find: reactive({ open: false, index: 0, count: 0 }),
  ensureBridge: vi.fn(() => bridge),
  getBridge: vi.fn(() => bridge),
  releaseBridge: vi.fn(),
  activateTab: vi.fn(),
  closeTab: vi.fn(),
  copyActiveSelection: vi.fn(async () => undefined),
  pasteIntoActive: vi.fn(async () => undefined),
  pushNotice: vi.fn(),
  explainProgramMouse: vi.fn(),
  reconnect: vi.fn(async () => undefined),
  setFontSize: vi.fn(),
  openFind: vi.fn(),
  closeFind: vi.fn(),
};

vi.mock('../lib/store', () => store);

const { default: TerminalPane } = await import('./TerminalPane.vue');

const tab = {
  id: 'tab-1',
  title: 'tester@127.0.0.1:47222',
  target: 'tester@127.0.0.1:47222',
  hostId: 'host-1',
  status: 'open' as const,
  bytesIn: 0,
  bytesOut: 0,
  startedAt: Date.now(),
};

async function openMenu() {
  const wrapper = mount(TerminalPane, { props: { tab, active: true } });
  await wrapper.get('[data-testid="terminal-host"]').trigger('contextmenu', { clientX: 120, clientY: 200 });
  return wrapper;
}

type MenuWrapper = Awaited<ReturnType<typeof openMenu>>;

function item(wrapper: MenuWrapper, label: string) {
  const match = wrapper.findAll('button').find((button) => button.text().startsWith(label));
  if (!match) {
    const labels = wrapper
      .findAll('button')
      .map((button) => button.text())
      .join(' | ');
    throw new Error(`menu item not found: ${label} (have: ${labels})`);
  }
  return match;
}

describe('terminal context menu', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    bridge.trzsz.isTransferringFiles.mockReturnValue(false);
    bridge.term.modes.mouseTrackingMode = 'none';
  });

  it('opens on right-click', async () => {
    const wrapper = await openMenu();
    expect(wrapper.text()).toContain('Select all');
    expect(wrapper.text()).toContain('Upload files…');
  });

  it('leaves the right-click to a program that asked for the mouse', async () => {
    // A TUI on mouse tracking has already received the button-2 press and draws
    // its own menu; ours on top of it made neither menu usable.
    bridge.term.modes.mouseTrackingMode = 'vt200';
    const wrapper = mount(TerminalPane, { props: { tab, active: true } });
    await wrapper.get('[data-testid="terminal-host"]').trigger('contextmenu', { clientX: 120, clientY: 200 });

    expect(wrapper.text()).not.toContain('Select all');
    expect(store.explainProgramMouse).toHaveBeenCalledTimes(1);
  });

  it('still opens with Shift, which xterm reserves for the local terminal', async () => {
    bridge.term.modes.mouseTrackingMode = 'drag';
    const wrapper = mount(TerminalPane, { props: { tab, active: true } });
    await wrapper
      .get('[data-testid="terminal-host"]')
      .trigger('contextmenu', { clientX: 120, clientY: 200, shiftKey: true });

    expect(wrapper.text()).toContain('Select all');
    expect(store.explainProgramMouse).not.toHaveBeenCalled();
  });

  it('runs Select all', async () => {
    const wrapper = await openMenu();
    await item(wrapper, 'Select all').trigger('click');
    expect(bridge.term.selectAll).toHaveBeenCalledTimes(1);
  });

  it('runs Copy', async () => {
    const wrapper = await openMenu();
    await item(wrapper, 'Copy').trigger('click');
    expect(store.copyActiveSelection).toHaveBeenCalledWith('tab-1');
  });

  it('runs Paste', async () => {
    const wrapper = await openMenu();
    await item(wrapper, 'Paste').trigger('click');
    expect(store.pasteIntoActive).toHaveBeenCalledWith('tab-1');
  });

  it('runs Clear buffer', async () => {
    const wrapper = await openMenu();
    await item(wrapper, 'Clear buffer').trigger('click');
    expect(bridge.clear).toHaveBeenCalledTimes(1);
  });

  it('offers Find in terminal in the context menu', async () => {
    const wrapper = await openMenu();
    await item(wrapper, 'Find in terminal').trigger('click');
    expect(store.openFind).toHaveBeenCalledTimes(1);
    expect(wrapper.find('[data-test="find-bar"]').exists()).toBe(false);
  });

  it('runs Stop transfer when a transfer is in flight', async () => {
    bridge.trzsz.isTransferringFiles.mockReturnValue(true);
    const wrapper = await openMenu();
    await item(wrapper, 'Stop transfer').trigger('click');
    expect(bridge.trzsz.stopTransferringFiles).toHaveBeenCalledTimes(1);
  });

  it('runs Zoom in through the store', async () => {
    const wrapper = await openMenu();
    await item(wrapper, 'Zoom in').trigger('click');
    expect(store.setFontSize).toHaveBeenCalledWith(15);
  });

  it('runs Fit to window', async () => {
    const wrapper = await openMenu();
    await item(wrapper, 'Fit to window').trigger('click');
    expect(bridge.fitNow).toHaveBeenCalled();
  });

  it('runs Reconnect for a saved host', async () => {
    const wrapper = await openMenu();
    await item(wrapper, 'Reconnect').trigger('click');
    expect(store.reconnect).toHaveBeenCalledWith('tab-1');
  });

  it('asks trzsz to start an upload, instead of faking a drop', async () => {
    // The old version opened a hidden <input type=file> and rebuilt a
    // DataTransferItemList from it. Script-made items have no filesystem entry,
    // so trzsz's webkitGetAsEntry() came back null and the user watched
    // “No files to upload” after selecting real files.
    const wrapper = await openMenu();
    expect(wrapper.find('input[type="file"]').exists()).toBe(false);
    await item(wrapper, 'Upload files…').trigger('click');
    expect(bridge.requestUpload).toHaveBeenCalledTimes(1);
  });

  it('keeps the menu alive when the press lands inside it (the old bug)', async () => {
    const wrapper = await openMenu();
    const paste = item(wrapper, 'Paste');
    // The regression: a capture-phase pointerdown used to unmount the menu here,
    // so the click that followed had nothing to land on.
    await paste.trigger('pointerdown');
    expect(wrapper.text()).toContain('Paste');
    await paste.trigger('click');
    expect(store.pasteIntoActive).toHaveBeenCalledWith('tab-1');
  });

  it('closes when the press lands outside', async () => {
    const wrapper = await openMenu();
    expect(wrapper.text()).toContain('Paste');
    window.dispatchEvent(new Event('pointerdown'));
    await wrapper.vm.$nextTick();
    expect(wrapper.text()).not.toContain('Select all');
  });
});

describe('in-tab find', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    store.find.open = false;
    store.find.index = 0;
    store.find.count = 0;
  });

  afterEach(() => {
    store.find.open = false;
  });

  async function openBar(active = true, attach = false) {
    // Focus only lands somewhere real when the tree is in the document; every
    // other case here can stay detached and cheaper.
    const wrapper = mount(TerminalPane, {
      props: { tab, active },
      ...(attach ? { attachTo: document.body } : {}),
    });
    store.find.open = true;
    await nextTick();
    await nextTick();
    return wrapper;
  }

  async function type(wrapper: MenuWrapper, text: string) {
    const input = wrapper.get('[data-test="find-input"]');
    await input.setValue(text);
    return input;
  }

  it('stays out of the way until it is opened', () => {
    const wrapper = mount(TerminalPane, { props: { tab, active: true } });
    expect(wrapper.find('[data-test="find-bar"]').exists()).toBe(false);
  });

  it('shows only on the active tab', async () => {
    const wrapper = await openBar(false);
    expect(wrapper.find('[data-test="find-bar"]').exists()).toBe(false);
  });

  it('searches as you type, without asking for Enter', async () => {
    const wrapper = await openBar(true, true);
    await type(wrapper, 'error');
    expect(bridge.findNext).toHaveBeenCalledWith('error', { caseSensitive: false });
    // The field takes the keyboard so the next keystroke is not eaten by xterm.
    expect(document.activeElement).toBe(wrapper.get('[data-test="find-input"]').element);
    wrapper.unmount();
  });

  it('walks forward on Enter and backward on Shift+Enter', async () => {
    const wrapper = await openBar();
    const input = await type(wrapper, 'warn');
    bridge.findNext.mockClear();
    await input.trigger('keydown', { key: 'Enter' });
    expect(bridge.findNext).toHaveBeenCalledTimes(1);
    await input.trigger('keydown', { key: 'Enter', shiftKey: true });
    expect(bridge.findPrevious).toHaveBeenCalledTimes(1);
  });

  it('reports the match the terminal actually found', async () => {
    const wrapper = await openBar();
    store.find.index = 2;
    store.find.count = 7;
    await nextTick();
    expect(wrapper.get('[data-test="find-count"]').text()).toBe('2 / 7');
  });

  it('says so when a match exists but is past what the addon reports', async () => {
    // The addon stops listing matches at 1000 and reports index -1 for a selected
    // match outside that window; `0 / 1000` would read as "nothing found".
    const wrapper = await openBar();
    store.find.index = 0;
    store.find.count = 1000;
    await nextTick();
    expect(wrapper.get('[data-test="find-count"]').text()).toBe('? / 1000');
  });

  it('leaves an Escape that cancels an IME composition alone', async () => {
    // Chinese/Japanese input: Escape dismisses the candidate window. Swallowing it
    // here closed the bar and stranded a half-typed composition.
    const wrapper = await openBar(true, true);
    const input = await type(wrapper, 'zhong');
    await input.trigger('keydown', { key: 'Escape', isComposing: true });
    expect(store.closeFind).not.toHaveBeenCalled();
    await input.trigger('keydown', { key: 'Escape' });
    expect(store.closeFind).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });

  it('turns match case on for the next search', async () => {
    const wrapper = await openBar();
    await type(wrapper, 'Error');
    bridge.findNext.mockClear();
    await wrapper.get('[data-test="find-case"]').trigger('click');
    expect(bridge.findNext).toHaveBeenCalledWith('Error', { caseSensitive: true });
  });

  it('drops the highlights when the query is emptied', async () => {
    const wrapper = await openBar();
    const input = await type(wrapper, 'abc');
    await input.setValue('');
    expect(bridge.clearSearch).toHaveBeenCalled();
  });

  it('closes on Esc and gives the keyboard back to the terminal', async () => {
    const wrapper = await openBar();
    const input = await type(wrapper, 'abc');
    bridge.focus.mockClear();
    await input.trigger('keydown', { key: 'Escape' });
    expect(store.closeFind).toHaveBeenCalledTimes(1);
    expect(bridge.focus).toHaveBeenCalledTimes(1);
  });
});
