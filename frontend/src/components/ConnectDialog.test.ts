/*
 * The host form's two-factor switch.
 *
 * The checkbox itself is one line of markup, and that is exactly why it earned a
 * test: the flag has to reach `SaveHost` (or a quick connect), and it has to
 * disappear — and stop being sent — when the authentication method cannot carry
 * a code. A box that looks ticked but saves `false` is the failure mode.
 */
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const backendMock = vi.hoisted(() => ({
  saveHost: vi.fn(),
  setHostSecret: vi.fn(async () => undefined),
  setHostProxySecret: vi.fn(async () => undefined),
  pickKeyFile: vi.fn(async () => ''),
  deleteHost: vi.fn(async () => undefined),
}));

const storeMock = vi.hoisted(() => ({
  connectHost: vi.fn(async (_hostId: string) => true),
  connectAdhoc: vi.fn(
    async (_config: {
      host: string;
      port: number;
      user: string;
      authType: string;
      twoFactor?: boolean;
    }) => true,
  ),
  loadHosts: vi.fn(async () => undefined),
  pushNotice: vi.fn(),
}));

vi.mock('../lib/backend', () => ({
  backend: backendMock,
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));
vi.mock('../lib/store', () => storeMock);

import ConnectDialog from './ConnectDialog.vue';

/** Fills the three fields every connect needs, whatever the mode. */
async function fillBasics(wrapper: ReturnType<typeof mount>): Promise<void> {
  await wrapper.find('input[placeholder="10.0.0.8 or server.example.com"]').setValue('10.0.0.8');
  await wrapper.find('input[placeholder="root"]').setValue('deploy');
  await wrapper.find('input[type="password"]').setValue('hunter2');
}

function buttonWithText(wrapper: ReturnType<typeof mount>, label: string) {
  const button = wrapper.findAll('button').find((candidate) => candidate.text() === label);
  if (!button) throw new Error(`no button labelled ${label}`);
  return button;
}

/** The private-key path box, found by content: CSS attribute selectors treat a
 *  backslash as an escape, so a Windows path cannot be used as a selector. */
function inputWithPlaceholderPart(wrapper: ReturnType<typeof mount>, part: string) {
  const input = wrapper.findAll('input').find((candidate) => (candidate.attributes('placeholder') ?? '').includes(part));
  if (!input) throw new Error(`no input with ${part} in its placeholder`);
  return input;
}

/** Submit buttons only fire their form's submit event, not a click handler. */
async function submitForm(wrapper: ReturnType<typeof mount>): Promise<void> {
  await wrapper.find('form').trigger('submit');
  await flushPromises();
}

beforeEach(() => {
  backendMock.saveHost.mockReset().mockResolvedValue({
    id: 'h_new',
    name: '10.0.0.8',
    host: '10.0.0.8',
    port: 22,
    user: 'deploy',
    authType: 'password',
    twoFactor: true,
    keyPath: '',
    colour: '',
    notes: '',
    hasSecret: false,
    createdAt: null,
    updatedAt: null,
  });
  Object.values(backendMock).forEach((fn) => {
    if (typeof fn === 'function' && 'mockClear' in fn) fn.mockClear();
  });
  storeMock.connectHost.mockClear();
  storeMock.connectAdhoc.mockClear();
  storeMock.loadHosts.mockClear();
  storeMock.pushNotice.mockClear();
});

describe('ConnectDialog two-factor switch', () => {
  it('saves the flag when it is ticked', async () => {
    const wrapper = mount(ConnectDialog, { props: { mode: 'new' } });
    await fillBasics(wrapper);
    await wrapper.find('[data-test="two-factor"]').setValue(true);
    await buttonWithText(wrapper, 'Save').trigger('click');
    await flushPromises();

    const payload = backendMock.saveHost.mock.calls[0]?.[0] as { twoFactor?: boolean; authType?: string };
    expect(payload.twoFactor).toBe(true);
    expect(payload.authType).toBe('password');
  });

  it('saves false when it is left alone', async () => {
    const wrapper = mount(ConnectDialog, { props: { mode: 'new' } });
    await fillBasics(wrapper);
    await buttonWithText(wrapper, 'Save').trigger('click');
    await flushPromises();

    const payload = backendMock.saveHost.mock.calls[0]?.[0] as { twoFactor?: boolean };
    expect(payload.twoFactor).toBe(false);
  });

  it('disappears — and is not saved — when the method is a key', async () => {
    const wrapper = mount(ConnectDialog, { props: { mode: 'new' } });
    await fillBasics(wrapper);
    await wrapper.find('[data-test="two-factor"]').setValue(true);

    await buttonWithText(wrapper, 'key').trigger('click');
    expect(wrapper.find('[data-test="two-factor"]').exists()).toBe(false);

    // Give the key form something to validate against, then save.
    await inputWithPlaceholderPart(wrapper, 'id_ed25519').setValue('C:\\keys\\id');
    await buttonWithText(wrapper, 'Save').trigger('click');
    await flushPromises();

    const payload = backendMock.saveHost.mock.calls[0]?.[0] as { twoFactor?: boolean; authType?: string };
    expect(payload.authType).toBe('key');
    expect(payload.twoFactor).toBe(false);
  });

  it('carries the flag into a quick connect', async () => {
    const wrapper = mount(ConnectDialog, { props: { mode: 'adhoc' } });
    await fillBasics(wrapper);
    await wrapper.find('[data-test="two-factor"]').setValue(true);
    await submitForm(wrapper);

    expect(storeMock.connectAdhoc).toHaveBeenCalledTimes(1);
    expect(storeMock.connectAdhoc.mock.calls[0]?.[0]).toMatchObject({ twoFactor: true, user: 'deploy' });
  });
});
