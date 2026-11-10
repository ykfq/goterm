/*
 * Output flow control, as a rule rather than as scattered conditions.
 *
 * The problem this exists for: `term.write` only queues, and xterm parses on its
 * own schedule. A server that sends faster than the terminal parses — `cat` of a
 * huge file, a verbose build, a log tail — leaves the difference in the
 * webview's memory. xterm eventually refuses the queue (and drops data), so the
 * only correct place to slow down is the source: Go's per-session output pump.
 *
 * The rule has hysteresis because a single threshold would flap: a busy terminal
 * hovers around any one number, and pausing/resuming per chunk is a round trip
 * per chunk. Pause high, resume low, and only ask the backend when the answer
 * changes.
 *
 * A session can also be held for a reason that is not a backlog: output that
 * arrived before its tab had a terminal to draw into. That is `hold`, and it
 * shares the same bookkeeping so a session cannot be left paused twice or
 * resumed while another reason still applies.
 */
export const FLOW_PAUSE_AT = 4 * 1024 * 1024;
export const FLOW_RESUME_AT = 1024 * 1024;

/** How the flow state reaches a session; Go's `SetOutputFlow` in the app. */
export type FlowRequest = (sessionId: string, paused: boolean) => void;

export class OutputFlow {
  /** Sessions held for a reason of their own (output with no terminal yet). */
  private readonly held = new Set<string>();
  /** Sessions whose output Go has actually been asked to pause. */
  private readonly paused = new Set<string>();
  /** Last backlog reported per session, so a release can check it. */
  private readonly queued = new Map<string, number>();

  constructor(
    private readonly pauseAt: number = FLOW_PAUSE_AT,
    private readonly resumeAt: number = FLOW_RESUME_AT,
    private readonly request: FlowRequest = () => undefined,
  ) {}

  /** Report how much output a session's terminal has accepted but not parsed. */
  track(sessionId: string, queued: number): void {
    if (!sessionId) return;
    this.queued.set(sessionId, queued);
    // A hold is about somewhere else to put the bytes, not about how fast this
    // terminal is parsing. Only the caller that made it may lift it.
    if (this.held.has(sessionId)) return;
    if (queued >= this.pauseAt) {
      this.pause(sessionId);
      return;
    }
    // Resume only after a real drain; a queue that hovers at the low mark would
    // otherwise flip on every parsed chunk.
    if (queued <= this.resumeAt) this.resume(sessionId);
  }

  /** Stop reading this session for a reason other than its parse backlog. */
  hold(sessionId: string): void {
    if (!sessionId) return;
    this.held.add(sessionId);
    this.pause(sessionId);
  }

  /** Let it run again, if it was held — and if its own backlog has drained. */
  release(sessionId: string): void {
    if (!sessionId || !this.held.delete(sessionId)) return;
    if (this.held.has(sessionId)) return;
    if ((this.queued.get(sessionId) ?? 0) > this.resumeAt) return;
    this.resume(sessionId);
  }

  /** Forget a session whose connection is gone; nothing is asked of it. */
  forget(sessionId: string | null | undefined): void {
    if (!sessionId) return;
    this.held.delete(sessionId);
    this.paused.delete(sessionId);
    this.queued.delete(sessionId);
  }

  isPaused(sessionId: string): boolean {
    return this.paused.has(sessionId);
  }

  private pause(sessionId: string): void {
    if (this.paused.has(sessionId)) return;
    this.paused.add(sessionId);
    this.request(sessionId, true);
  }

  private resume(sessionId: string): void {
    if (!this.paused.delete(sessionId)) return;
    this.request(sessionId, false);
  }
}
