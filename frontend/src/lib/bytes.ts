/*
 * Byte plumbing between a JSON transport and a binary protocol.
 *
 * Everything crossing the Go boundary is base64: the Wails bridge marshals to
 * JSON, so a raw 0x00-0xff byte cannot survive any other encoding. The rules
 * below are the whole defence against the mojibake that motivated this app.
 */

/** Chunk size for base64 encoding; keeps btoa's argument under engine limits. */
const B64_CHUNK = 0x8000;

const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder('utf-8', { fatal: false });

export function bytesToBase64(bytes: Uint8Array): string {
  if (bytes.length === 0) return '';
  let binary = '';
  for (let offset = 0; offset < bytes.length; offset += B64_CHUNK) {
    const chunk = bytes.subarray(offset, Math.min(offset + B64_CHUNK, bytes.length));
    // fromCharCode (not ...spread) so a 64 KiB chunk cannot blow the stack.
    let part = '';
    for (let i = 0; i < chunk.length; i += 1) part += String.fromCharCode(chunk[i] as number);
    binary += part;
  }
  return btoa(binary);
}

export function base64ToBytes(value: string): Uint8Array {
  if (!value) return new Uint8Array(0);
  // Go's StdEncoding never emits whitespace, but a stray newline from a log or
  // a clipboard must not corrupt a transfer.
  const cleaned = value.replace(/\s+/g, '');
  const binary = atob(cleaned);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) out[i] = binary.charCodeAt(i) & 0xff;
  return out;
}

export function textToUtf8(text: string): Uint8Array {
  return textEncoder.encode(text);
}

export function utf8ToText(bytes: Uint8Array): string {
  return textDecoder.decode(bytes);
}

/**
 * One character per byte, 0x00-0xff, and back again.
 *
 * Hand-rolled on purpose: WHATWG's "latin1" decoder is windows-1252, whose
 * 0x80-0x9f range maps to curly quotes and euro signs, so `TextDecoder('latin1')`
 * followed by `charCodeAt & 0xff` silently rewrites those bytes. This pair is
 * exactly invertible, which is what the banner rewrite and the highlighter need.
 */
export function bytesToLatin1(bytes: Uint8Array): string {
  let out = '';
  const CHUNK = 8192; // String.fromCharCode has an argument-count limit
  for (let start = 0; start < bytes.length; start += CHUNK) {
    const slice = bytes.subarray(start, Math.min(start + CHUNK, bytes.length));
    out += String.fromCharCode.apply(null, Array.from(slice));
  }
  return out;
}

/** One character per byte, 0x00-0xff. Used by tests to pin down trzsz framing. */
export function latin1ToBytes(text: string): Uint8Array {
  const out = new Uint8Array(text.length);
  for (let i = 0; i < text.length; i += 1) out[i] = text.charCodeAt(i) & 0xff;
  return out;
}

/**
 * Turn whatever trzsz hands the send callback into bytes for the PTY.
 *
 * trzsz gives protocol frames as strings built from ASCII (base64 payloads,
 * '#ID:' framing) — identical in UTF-8 and latin1 — and file payloads as
 * Uint8Array. Typed text is the one case that is not ASCII, and there UTF-8 is
 * exactly what a remote PTY expects. So: bytes pass through, strings are UTF-8.
 */
export function toSendBytes(input: string | Uint8Array | ArrayBuffer): Uint8Array {
  if (typeof input === 'string') return textToUtf8(input);
  if (input instanceof Uint8Array) return input;
  return new Uint8Array(input);
}

export function formatBytes(count: number): string {
  if (!Number.isFinite(count) || count < 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = count;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const digits = unit === 0 || value >= 100 ? 0 : value >= 10 ? 1 : 2;
  return value.toFixed(digits) + ' ' + (units[unit] ?? 'B');
}

/**
 * Wails maps Go's time.Time to `any`; over JSON it is an RFC 3339 string.
 * Anything else (null, an object from a future Wails change) becomes null
 * rather than an Invalid Date that renders as "Invalid Date" in the UI.
 */
export function toDate(value: unknown): Date | null {
  if (value instanceof Date) return Number.isNaN(value.getTime()) ? null : value;
  if (typeof value === 'string' && value.trim() !== '') {
    const parsed = new Date(value);
    return Number.isNaN(parsed.getTime()) ? null : parsed;
  }
  return null;
}

/** Shortens SHA256:xxxx for a status bar that has other things to say. */
export function shortFingerprint(fingerprint: string): string {
  const idx = fingerprint.indexOf(':');
  if (idx < 0) return fingerprint.slice(0, 12);
  if (fingerprint.length <= idx + 9) return fingerprint;
  return `${fingerprint.slice(0, idx + 9)}…`;
}
