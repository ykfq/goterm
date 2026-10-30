/*
 * The prompt that asks before a multi-line paste is sent.
 *
 * The keys are the whole interface: Enter confirms, Shift+Enter is a line break,
 * Escape cancels. Getting that backwards would either make the box useless for
 * editing or send a script nobody confirmed.
 */
import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';

import PromptDialog from './PromptDialog.vue';

function mountMultiline(initial = 'line one\nline two') {
  return mount(PromptDialog, {
    props: { title: 'Paste several lines?', multiline: true, initial, confirm: 'Paste' },
  });
}

describe('PromptDialog, multi-line', () => {
  it('opens with the text it was given, as a textarea', () => {
    const wrapper = mountMultiline();
    const area = wrapper.get('[data-test="prompt-textarea"]');
    expect((area.element as HTMLTextAreaElement).value).toBe('line one\nline two');
    expect(wrapper.find('form input').exists()).toBe(false);
  });

  it('confirms with Enter', async () => {
    const wrapper = mountMultiline();
    await wrapper.get('[data-test="prompt-textarea"]').trigger('keydown', { key: 'Enter' });
    expect(wrapper.emitted('submit')?.[0]).toEqual(['line one\nline two']);
  });

  it('sends what was edited, not what it opened with', async () => {
    const wrapper = mountMultiline();
    const area = wrapper.get('[data-test="prompt-textarea"]');
    await area.setValue('edited\nlines');
    await area.trigger('keydown', { key: 'Enter' });
    expect(wrapper.emitted('submit')?.[0]).toEqual(['edited\nlines']);
  });

  it('leaves Enter for line breaks while Shift is held', async () => {
    const wrapper = mountMultiline();
    await wrapper.get('[data-test="prompt-textarea"]').trigger('keydown', { key: 'Enter', shiftKey: true });
    expect(wrapper.emitted('submit')).toBeUndefined();
  });

  it('cancels on Escape', async () => {
    const wrapper = mountMultiline();
    await wrapper.get('[data-test="prompt-textarea"]').trigger('keydown', { key: 'Escape' });
    expect(wrapper.emitted('cancel')).toHaveLength(1);
    expect(wrapper.emitted('submit')).toBeUndefined();
  });

  it('will not confirm an empty box', async () => {
    const wrapper = mountMultiline();
    const area = wrapper.get('[data-test="prompt-textarea"]');
    await area.setValue('   ');
    await area.trigger('keydown', { key: 'Enter' });
    expect(wrapper.emitted('submit')).toBeUndefined();
  });
});
