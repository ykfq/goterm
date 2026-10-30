/*
 * Two-factor logins and the export/import prompts.
 *
 * The contract this pins: a two-factor host is asked for its code *before* the
 * first attempt, the code travels as its own argument (Go appends it to the
 * saved password, which the front end never sees), a refusal earns one more ask
 * rather than a dead end, and cancelling is a cancellation.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const backendMock = vi.hoisted(() => ({
  listHosts: vi.fn(async () => [] as unknown[]),
  exportHosts: vi.fn(),
  importHosts: vi.fn(),
}));

vi.mock('./backend', () => ({
  backend: backendMock,
  errorMessage: (error: unknown) => (error instanceof Error ? error.message : String(error)),
  fileBridgeApi: () => ({}),
}));

import {
  answerPrompt,
  answerSecretPrompt,
  connectWithPassphrasePrompt,
  exportHosts,
  importHosts,
  looksLikeAuthFailure,
  prompt,
  secretPrompt,
  state,
  wasCancelled,
} from './store';

const AUTH_FAILED = 'authentication failed for bob@server:22 (offered password)';

type Attempt = (passphrase: string, proxyPassphrase: string, twoFactorCode: string) => Promise<string>;

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

beforeEach(() => {
  backendMock.listHosts.mockReset().mockResolvedValue([]);
  backendMock.exportHosts.mockReset();
  backendMock.importHosts.mockReset();
  state.hosts.splice(0, state.hosts.length);
  state.notices.splice(0, state.notices.length);
});

afterEach(() => {
  if (prompt.open) answerPrompt(null);
  if (secretPrompt.open) answerSecretPrompt(null);
});

describe('looksLikeAuthFailure', () => {
  it('matches what Go says when the credentials are refused', () => {
    expect(looksLikeAuthFailure(new Error(AUTH_FAILED))).toBe(true);
    expect(looksLikeAuthFailure(new Error('unable to authenticate, attempted methods [none password]'))).toBe(true);
  });

  it('does not match transport failures or host-key changes', () => {
    expect(looksLikeAuthFailure(new Error('connection refused by 10.0.0.8:22'))).toBe(false);
    expect(looksLikeAuthFailure(new Error('host key for 10.0.0.8:22 has changed'))).toBe(false);
    expect(looksLikeAuthFailure(new Error('passphrase required: private key is encrypted'))).toBe(false);
  });
});

describe('two-factor connections', () => {
  it('asks for the code before the first attempt', async () => {
    const attempt = vi.fn<Attempt>().mockResolvedValue('s_1');

    const result = connectWithPassphrasePrompt(attempt, {
      title: 'Key passphrase',
      hint: 'unused',
      twoFactor: { label: 'bob@server:22' },
    });

    await settle();
    expect(prompt.open).toBe(true);
    expect(prompt.title).toBe('Two-factor code');
    expect(prompt.hint).toContain('bob@server:22');
    // Nothing was attempted with a password-only login.
    expect(attempt).not.toHaveBeenCalled();

    answerPrompt(' 123456 ');
    await expect(result).resolves.toBe('s_1');
    // The code is trimmed and handed over as its own argument.
    expect(attempt).toHaveBeenCalledWith('', '', '123456');
  });

  it('asks again, quoting the refusal, when the code was refused', async () => {
    const attempt = vi
      .fn<Attempt>()
      .mockRejectedValueOnce(new Error(AUTH_FAILED))
      .mockResolvedValueOnce('s_2');

    const result = connectWithPassphrasePrompt(attempt, {
      title: 'Key passphrase',
      hint: 'unused',
      twoFactor: { label: 'bob@server:22' },
    });

    await settle();
    answerPrompt('111111'); // the expired one
    await settle();

    expect(prompt.open).toBe(true);
    expect(prompt.error).toContain('authentication failed');
    answerPrompt('222222');

    await expect(result).resolves.toBe('s_2');
    expect(attempt).toHaveBeenNthCalledWith(1, '', '', '111111');
    expect(attempt).toHaveBeenNthCalledWith(2, '', '', '222222');
  });

  it('cancelling the code prompt means no connection, not a fallback', async () => {
    const attempt = vi.fn<Attempt>().mockResolvedValue('s_3');

    const result = connectWithPassphrasePrompt(attempt, {
      title: 'Key passphrase',
      hint: 'unused',
      twoFactor: { label: 'bob@server:22' },
    });
    const settled = result.catch((error: unknown) => error);
    await settle();
    answerPrompt(null);

    expect(wasCancelled(await settled)).toBe(true);
    expect(attempt).not.toHaveBeenCalled();
  });

  it('an empty code is treated as a cancel', async () => {
    const attempt = vi.fn<Attempt>().mockResolvedValue('s_4');

    const result = connectWithPassphrasePrompt(attempt, {
      title: 'Key passphrase',
      hint: 'unused',
      twoFactor: { label: 'bob@server:22' },
    });
    const settled = result.catch((error: unknown) => error);
    await settle();
    answerPrompt('   ');

    expect(wasCancelled(await settled)).toBe(true);
    expect(attempt).not.toHaveBeenCalled();
  });

  it('does not ask a host that is not flagged for two-factor', async () => {
    const attempt = vi.fn<Attempt>().mockResolvedValue('s_5');

    await expect(
      connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted' }),
    ).resolves.toBe('s_5');
    expect(prompt.open).toBe(false);
    expect(attempt).toHaveBeenCalledWith('', '', '');
  });

  it('a plain wrong password is still a plain failure (no code loop)', async () => {
    const attempt = vi.fn<Attempt>().mockRejectedValue(new Error(AUTH_FAILED));

    const result = connectWithPassphrasePrompt(attempt, { title: 'Key passphrase', hint: 'encrypted' });
    await expect(result).rejects.toThrow(/authentication failed/);
    expect(attempt).toHaveBeenCalledTimes(1);
  });
});

describe('export / import prompts', () => {
  function host(id: string) {
    return {
      id,
      name: `host-${id}`,
      group: '',
      host: '10.0.0.8',
      port: 22,
      user: 'deploy',
      authType: 'password' as const,
      keyPath: '',
      colour: '',
      notes: '',
      hasSecret: true,
      createdAt: null,
      updatedAt: null,
    };
  }

  it('export asks for a passphrase first and passes it to Go', async () => {
    state.hosts.push(host('h_1') as never);
    backendMock.exportHosts.mockResolvedValue({ path: 'C:/tmp/bundle.json', hosts: 1, secrets: 1, missing: 0 });

    const running = exportHosts();
    await settle();
    expect(prompt.open).toBe(true);
    expect(prompt.title).toBe('Export hosts');
    expect(prompt.secret).toBe(true);

    answerPrompt('correct horse');
    await running;

    expect(backendMock.exportHosts).toHaveBeenCalledWith('correct horse');
    expect(state.notices.at(-1)?.message).toContain('C:/tmp/bundle.json');
  });

  it('export with nothing to export does not even ask', async () => {
    await exportHosts();
    expect(prompt.open).toBe(false);
    expect(backendMock.exportHosts).not.toHaveBeenCalled();
  });

  it('cancelling the export prompt opens no dialog', async () => {
    state.hosts.push(host('h_1') as never);
    const running = exportHosts();
    await settle();
    answerPrompt(null);
    await running;
    expect(backendMock.exportHosts).not.toHaveBeenCalled();
  });

  it('import passes the passphrase, then reloads the list', async () => {
    backendMock.importHosts.mockResolvedValue({ path: 'C:/tmp/bundle.json', added: 2, updated: 1, skipped: 0, secrets: 2 });

    const running = importHosts();
    await settle();
    expect(prompt.title).toBe('Import hosts');
    answerPrompt('correct horse');
    await running;

    expect(backendMock.importHosts).toHaveBeenCalledWith('correct horse');
    expect(backendMock.listHosts).toHaveBeenCalled();
    expect(state.notices.at(-1)?.message).toContain('2 added');
    expect(state.notices.at(-1)?.message).toContain('2 password(s) restored');
  });

  it('a wrong passphrase surfaces Go\u2019s message instead of a silent no-op', async () => {
    backendMock.importHosts.mockRejectedValue(new Error('wrong passphrase, or the file is damaged'));

    const running = importHosts();
    await settle();
    answerPrompt('nope');
    await running;

    const last = state.notices.at(-1);
    expect(last?.level).toBe('error');
    expect(last?.message).toContain('wrong passphrase');
  });
});
