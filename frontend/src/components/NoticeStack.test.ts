/*
 * A notice that offers a fix has to be clickable, and the click has to land in
 * the real settings — not in a copy the dialog never reads.
 */
import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const backendMock = vi.hoisted(() => ({
  fonts: vi.fn(async () => []),
  fileBridgeApi: vi.fn(),
}));

vi.mock('../lib/backend', () => ({
  backend: backendMock,
  fileBridgeApi: () => backendMock.fileBridgeApi(),
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
}));

import NoticeStack from './NoticeStack.vue';
import { applyNoticeEvent, pushNotice, resetSettings, state } from '../lib/store';

beforeEach(() => {
  resetSettings();
  localStorage.clear();
  state.notices.splice(0);
});

describe('NoticeStack', () => {
  it('renders no button for an ordinary notice', async () => {
    pushNotice('info', 'Connected.');
    const wrapper = mount(NoticeStack);
    await flushPromises();
    expect(wrapper.text()).toContain('Connected.');
    expect(wrapper.find('[data-test="notice-action"]').exists()).toBe(false);
  });

  it('offers to turn anti-idle on when a shell timed itself out', async () => {
    applyNoticeEvent({
      level: 'warn',
      message: 'the remote shell logged itself out after an input timeout (bash TMOUT)',
      action: 'enable-anti-idle',
    });
    const wrapper = mount(NoticeStack);
    await flushPromises();

    // Sticky: an offer that vanishes after nine seconds is not an offer.
    const notice = state.notices.find((candidate) => candidate.action === 'enable-anti-idle');
    expect(notice?.sticky).toBe(true);
    const button = wrapper.find('[data-test="notice-action"]');
    expect(button.exists()).toBe(true);

    await button.trigger('click');
    await flushPromises();

    expect(state.settings.idleKeepAliveMinutes).toBe(3);
    expect(JSON.parse(localStorage.getItem('goterm.settings') ?? '{}').idleKeepAliveMinutes).toBe(3);
    // The offer is gone; what is left is the confirmation.
    expect(wrapper.find('[data-test="notice-action"]').exists()).toBe(false);
    expect(state.notices.some((notice) => notice.message.startsWith('Anti-idle on:'))).toBe(true);
  });

  it('ignores an action it does not know', async () => {
    applyNoticeEvent({ level: 'warn', message: 'something happened', action: 'launch-rocket' });
    const wrapper = mount(NoticeStack);
    await flushPromises();
    expect(wrapper.text()).toContain('something happened');
    expect(wrapper.find('[data-test="notice-action"]').exists()).toBe(false);
  });

  it('never sits over the closed-session buttons at the bottom of the pane', () => {
    // Regression: anchored bottom-right, the toast stack covered the
    // "Connection closed" bar's Reconnect/Close buttons — and an error toast
    // never auto-dismisses, so those buttons stayed dead until it was closed.
    const wrapper = mount(NoticeStack);
    const stack = wrapper.get('[data-test="notice-stack"]');
    expect(stack.classes().some((name) => name.startsWith('top-'))).toBe(true);
    expect(stack.classes().some((name) => name.startsWith('bottom-'))).toBe(false);
  });
});
