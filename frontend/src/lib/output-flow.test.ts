/*
 * The rule that keeps a firehose from becoming a gigabyte of webview memory.
 *
 * A server that sends faster than the terminal parses cannot be managed by
 * growing a buffer: Go has to stop reading. What is tested here is the decision
 * — pause high, resume low, and only ask when the answer changes — not the IPC.
 */
import { describe, expect, it } from 'vitest';

import { FLOW_PAUSE_AT, FLOW_RESUME_AT, OutputFlow } from './output-flow';

function harness(): { flow: OutputFlow; calls: [string, boolean][] } {
  const calls: [string, boolean][] = [];
  const flow = new OutputFlow(undefined, undefined, (sessionId, paused) => calls.push([sessionId, paused]));
  return { flow, calls };
}

describe('OutputFlow', () => {
  it('does nothing while the backlog is ordinary', () => {
    const { flow, calls } = harness();
    flow.track('s1', 0);
    flow.track('s1', FLOW_RESUME_AT);
    flow.track('s1', FLOW_PAUSE_AT - 1);
    expect(calls).toEqual([]);
    expect(flow.isPaused('s1')).toBe(false);
  });

  it('pauses once at the high-water mark, not once per chunk', () => {
    const { flow, calls } = harness();
    flow.track('s1', FLOW_PAUSE_AT);
    flow.track('s1', FLOW_PAUSE_AT + 1024);
    flow.track('s1', FLOW_PAUSE_AT * 2);
    expect(calls).toEqual([['s1', true]]);
  });

  it('resumes only after the queue has really drained', () => {
    const { flow, calls } = harness();
    flow.track('s1', FLOW_PAUSE_AT);
    // Between the marks: still paused. A terminal hovers here on a busy stream,
    // and resuming on every parsed chunk would be one round trip per chunk.
    flow.track('s1', FLOW_RESUME_AT + 1);
    expect(flow.isPaused('s1')).toBe(true);
    expect(calls).toEqual([['s1', true]]);

    flow.track('s1', FLOW_RESUME_AT);
    expect(flow.isPaused('s1')).toBe(false);
    expect(calls).toEqual([
      ['s1', true],
      ['s1', false],
    ]);
  });

  it('keeps sessions apart', () => {
    const { flow, calls } = harness();
    flow.track('a', FLOW_PAUSE_AT);
    flow.track('b', 0);
    expect(flow.isPaused('a')).toBe(true);
    expect(flow.isPaused('b')).toBe(false);
    expect(calls).toEqual([['a', true]]);
  });

  it('holds a session for output that has no terminal yet, and releases it once', () => {
    const { flow, calls } = harness();
    flow.hold('s1');
    flow.hold('s1');
    expect(calls).toEqual([['s1', true]]);

    // A backlog below the mark must not resume a hold that was asked for another
    // reason — the tab still has nowhere to draw.
    flow.track('s1', 0);
    expect(flow.isPaused('s1')).toBe(true);
    expect(calls).toEqual([['s1', true]]);

    flow.release('s1');
    flow.release('s1');
    expect(calls).toEqual([
      ['s1', true],
      ['s1', false],
    ]);
  });

  it('forgets a session without talking to it', () => {
    const { flow, calls } = harness();
    flow.track('s1', FLOW_PAUSE_AT);
    flow.forget('s1');
    expect(flow.isPaused('s1')).toBe(false);
    // The connection is gone; there is nobody to tell.
    expect(calls).toEqual([['s1', true]]);
  });
});
