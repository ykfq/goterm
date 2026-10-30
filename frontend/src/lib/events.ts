/*
 * One subscription per event topic, for the lifetime of the app.
 *
 * Subscribing at startup (rather than when a session opens) removes the race
 * between "Connect returned a session id" and "the tab started listening" —
 * output produced in that gap would otherwise be dropped on the floor.
 *
 * Wails v3 delivers events through the runtime package: `Events.On(name, cb)`
 * hands the handler a { name, data } object, and Go's Emit payload is `data`.
 */
import { Events } from '@wailsio/runtime';

import type { ClosedEvent, DataEvent, NoticeEvent } from './protocol';
import { EVENT_CLOSED, EVENT_DATA, EVENT_NOTICE } from './protocol';
import { base64ToBytes } from './bytes';
import { backend } from './backend';

type Handler<T> = (payload: T) => void;

const dataHandlers = new Set<Handler<DataEvent>>();
const closedHandlers = new Set<Handler<ClosedEvent>>();
const noticeHandlers = new Set<Handler<NoticeEvent>>();

let started = false;
let bridgeMissing = false;
let bridgeFailure = '';

/** Why the backend probe failed, for the status bar and the notice. */
export function bridgeError(): string {
  return bridgeFailure;
}

/** How long to wait for the runtime to announce itself before probing anyway. */
const RUNTIME_WAIT_MS = 4000;
const PING_ATTEMPTS = 5;
const PING_GAP_MS = 200;

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/**
 * Waits for Wails' own readiness event, with a deadline: in a plain browser
 * that event never comes, and waiting forever would hang startup.
 *
 * The name is a literal on purpose — the runtime exposes it as a type-only
 * `Types` table in this beta, and a throw here would silently take the whole
 * bootstrap path down with it.
 */
const WINDOW_RUNTIME_READY = 'common:WindowRuntimeReady';

async function waitForRuntime(): Promise<void> {
  try {
    await new Promise<void>((resolve) => {
      const timer = setTimeout(resolve, RUNTIME_WAIT_MS);
      Events.Once(WINDOW_RUNTIME_READY, () => {
        clearTimeout(timer);
        resolve();
      });
    });
  } catch (error) {
    // Readiness is an optimisation, not a requirement: the ping below still
    // decides, with retries.
    console.warn('[goterm] runtime readiness event unavailable, probing anyway', error);
  }
}

/** True once a real call has proved the Go backend is there. */
export function bridgeAvailable(): boolean {
  return started && !bridgeMissing;
}

/**
 * Subscribes once. Safe to call repeatedly; resolves false without a backend.
 *
 * The check is a real round trip (`Ping`), not a sniff of some injected global.
 * It waits for the runtime's own "window runtime ready" event first, then
 * retries briefly: at page load the native bridge may not be wired up yet, and
 * deciding "there is no backend" one frame too early would leave the whole app
 * permanently read-only.
 */
export async function initBridge(): Promise<boolean> {
  if (started) return !bridgeMissing;
  started = true;

  await waitForRuntime();

  let lastError: unknown;
  for (let attempt = 0; attempt < PING_ATTEMPTS; attempt += 1) {
    try {
      await backend.ping();
      lastError = undefined;
      break;
    } catch (error) {
      lastError = error;
      await sleep(PING_GAP_MS * (attempt + 1));
    }
  }
  if (lastError !== undefined) {
    bridgeMissing = true;
    bridgeFailure = lastError instanceof Error ? lastError.message : String(lastError);
    console.warn('[goterm] no Wails bridge: the page is not running inside the Go app', lastError);
    return false;
  }

  Events.On(EVENT_DATA, (event) => {
    const payload = event.data as DataEvent;
    if (!payload || typeof payload.sessionId !== 'string') return;
    for (const handler of [...dataHandlers]) handler(payload);
  });
  Events.On(EVENT_CLOSED, (event) => {
    const payload = event.data as ClosedEvent;
    if (!payload || typeof payload.sessionId !== 'string') return;
    for (const handler of [...closedHandlers]) handler(payload);
  });
  Events.On(EVENT_NOTICE, (event) => {
    const payload = event.data as NoticeEvent;
    if (!payload || typeof payload.message !== 'string') return;
    for (const handler of [...noticeHandlers]) handler(payload);
  });
  return true;
}

/** Decodes one data event into bytes, or null if Go ever sends something else. */
export function decodeData(event: DataEvent): Uint8Array | null {
  try {
    return base64ToBytes(event.data ?? '');
  } catch (error) {
    console.error('[goterm] undecodable ssh:data payload', error);
    return null;
  }
}

export function onDataEvent(handler: Handler<DataEvent>): () => void {
  dataHandlers.add(handler);
  return () => dataHandlers.delete(handler);
}

export function onClosedEvent(handler: Handler<ClosedEvent>): () => void {
  closedHandlers.add(handler);
  return () => closedHandlers.delete(handler);
}

export function onNoticeEvent(handler: Handler<NoticeEvent>): () => void {
  noticeHandlers.add(handler);
  return () => noticeHandlers.delete(handler);
}

/** Test seam: lets a test replay events without a backend. */
export function emitForTest(topic: 'data' | 'closed' | 'notice', payload: unknown): void {
  if (topic === 'data') for (const handler of [...dataHandlers]) handler(payload as DataEvent);
  if (topic === 'closed') for (const handler of [...closedHandlers]) handler(payload as ClosedEvent);
  if (topic === 'notice') for (const handler of [...noticeHandlers]) handler(payload as NoticeEvent);
}
