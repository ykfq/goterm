/*
 * A saved host whose password was never stored.
 *
 * The bug: add a host, leave the password box empty, save, then double-click it —
 * the connect failed immediately with an auth error and no prompt, while an
 * encrypted *key* in the same situation asked for its passphrase. Go's half of the
 * fix is `hoststore.ErrPasswordRequired` (pinned in
 * internal/hoststore/password_prompt_test.go); this file pins the JS half: the
 * marker is recognised, and `connectHost` asks *before* dialing so the remote never
 * sees an empty password spent as a failed login.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { Host } from './protocol';

vi.mock('./backend', async () => {
  const actual = await vi.importActual<typeof import('./backend')>('./backend');
  return {
    ...actual,
    backend: {
      ...actual.backend,
      connectHost: vi.fn(async () => 's_live'),
      connect: vi.fn(async () => 's_adhoc'),
      listSessions: vi.fn(async () => []),
      listKnownHosts: vi.fn(async () => []),
      setHostSecret: vi.fn(async () => undefined),
      setHostProxySecret: vi.fn(async () => undefined),
    },
  };
});

import { backend } from './backend';
import {
  answerSecretPrompt,
  connectHost,
  connectWithPassphrasePrompt,
  needsPassword,
  secretPrompt,
  state,
} from './store';

const NO_PASSWORD = 'password required: web-01 has no saved password';
const NO_PROXY_PASSWORD = 'password required (proxy): web-01 jump host has no saved password';

/** Lets a rejection (or a prompt) get as far as the UI before we answer it. */
async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function host(overrides: Partial<Host> = {}): Host {
  return {
    id: 'h_1',
    name: 'web-01',
    group: '',
    host: '10.0.0.8',
    port: 2222,
    user: 'deploy',
    authType: 'password',
    keyPath: '',
    colour: '',
    notes: '',
    hasSecret: false,
    createdAt: null,
    updatedAt: null,
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  state.hosts.splice(0, state.hosts.length);
  state.tabs.splice(0, state.tabs.length);
  state.busy = false;
});

afterEach(() => {
  if (secretPrompt.open) answerSecretPrompt(null);
});

describe('needsPassword', () => {
  it('recognises both markers Go can send', () => {
    expect(needsPassword(new Error(NO_PASSWORD))).toBe(true);
    expect(needsPassword(new Error(NO_PROXY_PASSWORD))).toBe(true);
  });

  it('leaves a real auth failure alone — that is a wrong password, not a missing one', () => {
    expect(needsPassword(new Error('authentication failed for bob@h (offered password)'))).toBe(false);
    expect(needsPassword(new Error('passphrase required: private key is encrypted'))).toBe(false);
    expect(needsPassword(new Error('connection refused by 10.0.0.8:22'))).toBe(false);
  });
});

describe('connectWithPassphrasePrompt and a missing password', () => {
  it('hands the ask to the caller and retries with what it sends next', async () => {
    const attempt = vi
      .fn<(passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>>()
      .mockRejectedValueOnce(new Error(NO_PASSWORD))
      .mockResolvedValueOnce('s_2');
    const refresh = vi.fn(async () => undefined);

    const result = connectWithPassphrasePrompt(attempt, {
      title: 'Key passphrase',
      hint: 'encrypted',
      hostId: 'h_1',
      refreshPassword: refresh,
    });

    await expect(result).resolves.toBe('s_2');
    expect(refresh).toHaveBeenCalledWith('target', NO_PASSWORD);
    expect(attempt).toHaveBeenCalledTimes(2);
  });

  it('attributes the jump host marker to the jump host', async () => {
    const attempt = vi
      .fn<(passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>>()
      .mockRejectedValueOnce(new Error(NO_PROXY_PASSWORD))
      .mockResolvedValueOnce('s_3');
    const refresh = vi.fn(async () => undefined);

    await connectWithPassphrasePrompt(attempt, {
      title: 'Key passphrase',
      hint: 'encrypted',
      refreshPassword: refresh,
    });

    expect(refresh).toHaveBeenCalledWith('proxy', NO_PROXY_PASSWORD);
  });

  it('does not swallow the marker for a caller with nothing to refresh', async () => {
    // Ad-hoc connect: the password came from the form, so a "no saved password"
    // marker means something is genuinely wrong and must surface.
    const attempt = vi
      .fn<(passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>>()
      .mockRejectedValue(new Error(NO_PASSWORD));

    await expect(
      connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted' }),
    ).rejects.toThrow('password required');
    expect(attempt).toHaveBeenCalledTimes(1);
  });

  it('stops when the user cancels the password prompt', async () => {
    const attempt = vi
      .fn<(passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>>()
      .mockRejectedValue(new Error(NO_PASSWORD));
    const refresh = vi.fn(async () => {
      throw new Error('connection cancelled');
    });

    await expect(
      connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted', refreshPassword: refresh }),
    ).rejects.toThrow('connection cancelled');
  });
});

describe('connectHost with a host that has no stored password', () => {
  it('asks first, and dials only with the answer', async () => {
    state.hosts.push(host());
    const connecting = connectHost('h_1');

    await settle();
    // The whole point: the prompt appears before Go is asked to dial, so no empty
    // password is ever spent as a failed login on the remote.
    expect(secretPrompt.open).toBe(true);
    expect(secretPrompt.title).toBe('Password');
    expect(secretPrompt.rememberable).toBe(true);
    expect(backend.connectHost).not.toHaveBeenCalled();

    answerSecretPrompt({ secret: 'hunter2', remember: false });
    await expect(connecting).resolves.toBe(true);

    expect(backend.connectHost).toHaveBeenCalledWith(
      'h_1',
      expect.any(Number),
      expect.any(Number),
      expect.objectContaining({ password: 'hunter2', proxyPassword: '' }),
    );
    // Not remembered, so nothing was written.
    expect(backend.setHostSecret).not.toHaveBeenCalled();
  });

  it('stores the password when the user ticks remember', async () => {
    state.hosts.push(host());
    const connecting = connectHost('h_1');
    await settle();
    answerSecretPrompt({ secret: 'hunter2', remember: true });
    await expect(connecting).resolves.toBe(true);
    expect(backend.setHostSecret).toHaveBeenCalledWith('h_1', 'hunter2');
  });

  it('cancelling leaves a closed tab, not an error', async () => {
    state.hosts.push(host());
    const connecting = connectHost('h_1');
    await settle();
    answerSecretPrompt(null);
    await expect(connecting).resolves.toBe(false);
    expect(backend.connectHost).not.toHaveBeenCalled();
    expect(state.tabs[0]?.detail).toBe('connection cancelled');
  });

  it('does not ask when the password is already in the key ring', async () => {
    state.hosts.push(host({ hasSecret: true }));
    await expect(connectHost('h_1')).resolves.toBe(true);
    expect(secretPrompt.open).toBe(false);
    expect(backend.connectHost).toHaveBeenCalledTimes(1);
  });

  it('asks for the jump host password too, and keeps the two apart', async () => {
    state.hosts.push(host({ hasSecret: true, proxyHost: 'bastion.internal', proxyPort: 2200, proxyUser: 'jump', proxyAuthType: 'password', hasProxySecret: false }));
    const connecting = connectHost('h_1');

    await settle();
    expect(secretPrompt.title).toBe('Jump-host password');
    answerSecretPrompt({ secret: 'jump-pw', remember: false });
    await expect(connecting).resolves.toBe(true);

    expect(backend.connectHost).toHaveBeenCalledWith(
      'h_1',
      expect.any(Number),
      expect.any(Number),
      // The typed jump-host password must not land in the target's field.
      expect.objectContaining({ password: '', proxyPassword: 'jump-pw' }),
    );
  });
});
