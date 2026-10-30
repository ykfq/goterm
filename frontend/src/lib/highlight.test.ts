/*
 * Keyword highlighting, tested as a byte-level transform.
 *
 * The claims that matter are not "it looked right on a screenshot":
 *   - unmatched bytes come out exactly as they went in (including every one of
 *     0x00-0xff, and a UTF-8 character split across two chunks);
 *   - nothing is inserted inside an escape sequence, so cursor movement, titles
 *     and charset switches survive;
 *   - the program's own colour comes back after each highlight;
 *   - a full-screen app is left completely alone.
 */
import { describe, expect, it } from 'vitest';

import { latin1ToBytes } from './bytes';
import { HIGHLIGHT_SETS, OutputHighlighter, type CustomRule } from './highlight';

const ALL = HIGHLIGHT_SETS.map((set) => set.id);
const decoder = new TextDecoder('utf-8');

function run(text: string, sets: string[] = ALL): string {
  return decoder.decode(new OutputHighlighter(sets).process(new TextEncoder().encode(text)));
}

/** Strip SGR sequences, so "the visible text is unchanged" is assertable. */
function visible(text: string): string {
  return text.replace(/\x1b\[[0-9;]*m/g, '');
}

describe('OutputHighlighter: fidelity', () => {
  it('leaves a pathologically long single line alone', () => {
    // A 64 KiB "line" is a minified blob, not something worth ten regex passes.
    const line = `ls ${'x'.repeat(9000)}`;
    expect(run(`${line}\n`)).toBe(`${line}\n`);
    // …and the next line is coloured again.
    expect(run('ls\n')).toMatch(/\x1b\[1;36mls/);
  });

  it('is a no-op when no set is enabled', () => {
    const bytes = latin1ToBytes('ls -la \x1b[31m error \x1b[0m');
    const out = new OutputHighlighter([]).process(bytes);
    expect(out).toBe(bytes);
  });

  it('returns every byte of unmatchable binary unchanged', () => {
    const bytes = new Uint8Array(256);
    for (let i = 0; i < 256; i += 1) bytes[i] = i;
    const out = new OutputHighlighter(ALL).process(bytes);
    expect(Array.from(out)).toEqual(Array.from(bytes));
  });

  it('keeps a UTF-8 character intact even when a chunk boundary splits it', () => {
    const text = '错误 ls 日志';
    const bytes = new TextEncoder().encode(text);
    const split = 2; // inside the first three-byte character
    const highlighter = new OutputHighlighter(ALL);
    const out = new Uint8Array([
      ...highlighter.process(bytes.subarray(0, split)),
      ...highlighter.process(bytes.subarray(split)),
    ]);
    const decoded = decoder.decode(out);
    // The three-byte character was split, so a naive byte-string round trip would
    // corrupt it; an SGR inserted between its bytes would corrupt it too.
    expect(decoded).not.toContain('\uFFFD');
    expect(decoded.replace(/\x1b\[[0-9;]*m/g, '')).toBe(text);
  });

  it('never changes the visible text of a realistic line', () => {
    const line = '2026-10-23T04:11:02Z web-01 systemd[1]: Started nginx.service.\n';
    expect(visible(run(line))).toBe(line);
  });
});

describe('OutputHighlighter: escape sequences', () => {
  it('leaves a CSI sequence byte-for-byte alone', () => {
    const out = run('\x1b[1;32mok\x1b[0m');
    expect(out).toContain('\x1b[1;32m');
    expect(out).toContain('\x1b[0m');
    expect(visible(out)).toBe('ok');
  });

  it('does not colour inside an OSC window title', () => {
    const out = run('\x1b]0;ls -la /root\x07prompt$ ');
    expect(out).toContain('\x1b]0;ls -la /root\x07');
    // The command inside the title was not decorated.
    expect(out).not.toMatch(/\x1b\[1;36mls/);
  });

  // The old contract was "paint over the program's colour, then put its colour
  // back". That is what made `grep --color` useless: the match the program was
  // pointing at got repainted in the same language as everything else on the
  // line. The program's colour is now the louder voice, and the line is left
  // exactly as it arrived.
  it('leaves a line the program coloured entirely alone', () => {
    const line = '\x1b[31mred ls here\x1b[0m';
    expect(run(line)).toBe(line);
  });

  it('leaves a grep match visible', () => {
    // What GNU grep actually sends: magenta filename, bold-red match, and plain
    // text around them. Nothing local may join in on this line.
    const line = '\x1b[35mserver.go\x1b[0m:\x1b[01;31mkeepalive\x1b[K at 10.0.0.8 on 2024-01-02\n';
    expect(run(line)).toBe(line);
    // ...and the line after it, which the program said nothing about, is
    // highlighted again.
    expect(run(`${line}ls /srv 10.0.0.8\n`)).toMatch(/\x1b\[1;36mls/);
  });

  it('stands down for reverse video too, because that is a colour choice', () => {
    const line = '\x1b[7mselected 10.0.0.8\x1b[27m';
    expect(run(line)).toBe(line);
  });

  it('still highlights under bold and underline, which are style not colour', () => {
    const out = run('\x1b[1m\x1b[4mbold underlined ls\x1b[0m');
    // The highlight gives the style back, both attributes of it.
    expect(out).toContain('\x1b[0m\x1b[1;36mls\x1b[0m\x1b[1m\x1b[4m');
  });

  it('hears a coloured prompt as one line only, not as the rest of the session', () => {
    const out = run('\x1b[32muser@host\x1b[0m $ ls\r\nls: cannot open /nope 10.0.0.8\r\n');
    // The prompt line keeps whatever the shell decided it should look like —
    // including the `ls` in it, which is command text, not output.
    expect(out).toContain('\x1b[32muser@host\x1b[0m $ ls\r\n');
    // ...and the output underneath it is highlighted normally.
    expect(out).toMatch(/\x1b\[1;36mls\x1b\[0m: cannot/);
  });

  it('stays out of the alternate screen until it is left', () => {
    const out = run('\x1b[?1049hls -la inside vim\x1b[?1049l ls after');
    expect(out).toContain('\x1b[?1049hls -la inside vim');
    expect(out).not.toMatch(/\x1b\[1;36mls -la inside/);
    // ...and picks up again once the app is gone.
    expect(out).toMatch(/\x1b\[1;36mls\x1b\[0m after/);
  });

  it('never decorates a trzsz banner', () => {
    const banner = '::TRZSZ:TRANSFER:R:1.2.0:1234\r\n';
    expect(run(banner)).toBe(banner);
  });

  it('reassembles a sequence split across chunks instead of painting inside it', () => {
    const highlighter = new OutputHighlighter(ALL);
    const first = highlighter.process(latin1ToBytes('\x1b[3'));
    const second = highlighter.process(latin1ToBytes('1m ls'));
    // The half sequence waits for the rest instead of being guessed at.
    expect(decoder.decode(first)).toBe('');
    // ...and comes back whole. The text after it is left alone: that sequence is
    // red, so the program is colouring this line and local rules stand down.
    const joined = decoder.decode(second);
    expect(joined).toContain('\x1b[31m');
    expect(joined).toBe('\x1b[31m ls');
  });

  it('paints normally after a split sequence that set no colour', () => {
    const highlighter = new OutputHighlighter(ALL);
    highlighter.process(latin1ToBytes('\x1b[?25'));
    const second = highlighter.process(latin1ToBytes('l ls -la'));
    // Cursor hide/show carries no colour, so there is nothing to yield to.
    expect(decoder.decode(second)).toMatch(/\x1b\[1;36mls/);
  });

  it('does not wait forever on a truncated sequence', () => {
    const highlighter = new OutputHighlighter(ALL);
    highlighter.process(latin1ToBytes(`\x1b${'x'.repeat(40)}`));
    const out = highlighter.process(latin1ToBytes('ls'));
    // The stuck fragment was released rather than carried into oblivion.
    expect(decoder.decode(out)).toContain('ls');
  });
});

describe('OutputHighlighter: highlight sets', () => {
  it('colours shell commands, flags and variables', () => {
    const out = run('sudo systemctl restart nginx --now $HOME');
    expect(out).toMatch(/\x1b\[1;36msudo/);
    expect(out).toMatch(/\x1b\[1;36msystemctl/);
    expect(out).toMatch(/\x1b\[33m--now/);
    expect(out).toMatch(/\x1b\[35m\$HOME/);
    expect(visible(out)).toBe('sudo systemctl restart nginx --now $HOME');
  });

  it('colours bash keywords and shebangs', () => {
    const out = run('#!/bin/bash\nif [ -f "$1" ]; then echo ok; fi');
    expect(out).toMatch(/\x1b\[1;32m#!\/bin\/bash/);
    expect(out).toMatch(/\x1b\[1;35mif/);
    expect(out).toMatch(/\x1b\[1;35mthen/);
    expect(out).toMatch(/\x1b\[1;35mfi/);
    expect(visible(out)).toBe('#!/bin/bash\nif [ -f "$1" ]; then echo ok; fi');
  });

  it('colours an nginx access line by status class', () => {
    const line =
      '10.0.0.8 - - [23/Oct/2026:04:11:02 +0800] "GET /api/v1/health HTTP/1.1" 200 1024 "-" "curl/8.5.0"';
    const out = run(line);
    expect(out).toMatch(/\x1b\[36m10\.0\.0\.8/);
    expect(out).toMatch(/\x1b\[90m\[23\/Oct/);
    expect(out).toMatch(/\x1b\[1;37m"GET \/api\/v1\/health HTTP\/1\.1"/);
    expect(out).toMatch(/\x1b\[32m200/);
    expect(visible(out)).toBe(line);

    const notFound =
      '192.168.1.20 - alice [23/Oct/2026:04:11:03 +0800] "POST /login HTTP/2.0" 404 812 "-" "Mozilla/5.0"';
    expect(run(notFound)).toMatch(/\x1b\[33m404/);
    const badGateway =
      '203.0.113.9 - - [23/Oct/2026:04:11:04 +0800] "GET /slow HTTP/1.1" 502 157 "-" "kube-probe/1.29"';
    expect(run(badGateway)).toMatch(/\x1b\[1;31m502/);

    // nginx's error log uses `YYYY/MM/DD`, its access log the bracketed form.
    const errorLog = '2026/10/23 04:11:05 [error] 812#0: *4 connect() failed (111: Connection refused)';
    expect(run(errorLog)).toMatch(/\x1b\[90m2026\/10\/23 04:11:05/);
    expect(run(errorLog)).toMatch(/\x1b\[31mfailed/);
  });

  it('colours a syslog line: time, service tag and severity', () => {
    const line = 'Oct 23 04:11:02 web-01 sshd[4012]: Failed password for root from 10.0.0.8 port 51234 ssh2';
    const out = run(line);
    expect(out).toMatch(/\x1b\[90mOct 23 04:11:02/);
    expect(out).toMatch(/\x1b\[1;34msshd\[4012\]:/);
    expect(out).toMatch(/\x1b\[1;31mFailed/);
    expect(visible(out)).toBe(line);

    expect(run('Oct 23 04:11:03 web-01 sshd[4012]: Accepted publickey for deploy from 10.0.0.9')).toMatch(
      /\x1b\[32mAccepted/,
    );
  });

  it('lets the first enabled set win where they overlap', () => {
    // "ls" is a command in both shell and bash; the shell colour is first.
    const out = run('ls', ['shell', 'bash']);
    expect(out).toMatch(/\x1b\[1;36mls/);
  });

  it('reads each line in its own vocabulary', () => {
    // A syslog line must not be painted by the shell rules: `for` is an English
    // word in "Failed password for root", not a bash keyword here.
    const syslog = 'Oct 23 04:11:02 web-01 sshd[4012]: Failed password for root from 10.0.0.8 port 22 ssh2';
    const out = run(syslog);
    expect(out).not.toMatch(/\x1b\[1;35mfor/);
    expect(out).not.toMatch(/\x1b\[1;36mfrom/);
    expect(visible(out)).toBe(syslog);

    // An nginx request line is a request, not a shell string literal.
    const access = '10.0.0.8 - - [23/Oct/2026:04:11:02 +0800] "GET /x HTTP/1.1" 200 12 "-" "curl"';
    expect(run(access)).toMatch(/\x1b\[1;37m"GET/);
    expect(run(access)).not.toMatch(/\x1b\[32m"GET/);
  });

  it('stops painting a domain it is told to stop painting', () => {
    const line = 'Oct 23 04:11:02 web-01 sshd[4012]: Failed password for root';
    // syslog off → the line falls back to the shell rules, which know no
    // severity words, so the red is gone.
    const out = run(line, ['shell']);
    expect(out).not.toMatch(/\x1b\[1;31mFailed/);
  });

  it('can be switched at runtime, including off', () => {
    const highlighter = new OutputHighlighter(['shell']);
    expect(highlighter.enabled).toBe(true);
    highlighter.setSets([]);
    expect(highlighter.enabled).toBe(false);
    const bytes = latin1ToBytes('ls');
    expect(highlighter.process(bytes)).toBe(bytes);
  });
});

describe('OutputHighlighter: data shapes', () => {
  it('colours addresses, ids and numbers on any kind of line', () => {
    const ip = run('client 10.0.0.8 connected');
    expect(ip).toMatch(/\x1b\[1;36m10\.0\.0\.8/);

    expect(run('peer 2001:db8::1 up')).toMatch(/\x1b\[36m2001:db8::1/);
    expect(run('mac aa:bb:cc:dd:ee:ff')).toMatch(/\x1b\[35maa:bb:cc:dd:ee:ff/);
    expect(run('id 550e8400-e29b-41d4-a716-446655440000')).toMatch(
      /\x1b\[35m550e8400-e29b-41d4-a716-446655440000/,
    );
    expect(run('call 13812345678 now')).toMatch(/\x1b\[1;35m13812345678/);
    expect(run('call +1 555 010 9999')).toMatch(/\x1b\[1;35m\+1 555 010 9999/);
    expect(run('mail ops@example.com')).toMatch(/\x1b\[1;34mops@example\.com/);
    expect(run('see https://example.com/a?b=1')).toMatch(/\x1b\[4;36mhttps:\/\/example\.com/);
    expect(run('at 2026-10-23T04:11:02Z')).toMatch(/\x1b\[90m2026-10-23T04:11:02Z/);
    expect(run('container a1b2c3d4e5f6')).toMatch(/\x1b\[35ma1b2c3d4e5f6/);
  });

  it('does not invent an address out of an impossible octet or a version', () => {
    expect(run('999.1.1.1')).not.toMatch(/\x1b\[1;36m999/);
    expect(visible(run('v1.2.3.4-beta'))).toBe('v1.2.3.4-beta');
  });

  it('leaves a long decimal number alone (only hex ids get a colour)', () => {
    const line = 'bytes 123456789012345';
    expect(run(line)).not.toMatch(/\x1b\[35m123456789012345/);
  });

  it('yields to the line vocabulary for a token it would also claim', () => {
    // The nginx rule owns the client address; the data set must not repaint it.
    const line = '10.0.0.8 - - [23/Oct/2026:04:11:02 +0800] "GET / HTTP/1.1" 200 12 "-" "curl"';
    const out = run(line);
    expect(out).toMatch(/\x1b\[36m10\.0\.0\.8/);
    expect(out).not.toMatch(/\x1b\[0m\x1b\[1;36m10\.0\.0\.8/);
  });
});

describe('OutputHighlighter: custom rules', () => {
  const rule = (pattern: string, extra: Partial<CustomRule> = {}): CustomRule => ({
    id: pattern,
    pattern,
    colour: 'magenta',
    regex: false,
    ignoreCase: true,
    enabled: true,
    ...extra,
  });

  it('colours a keyword whole-word and case-insensitively', () => {
    const highlighter = new OutputHighlighter(['shell']);
    expect(highlighter.setCustomRules([rule('panic')])).toEqual([]);
    const out = decoder.decode(highlighter.process(new TextEncoder().encode('a PANIC and panicky')));
    expect(out).toMatch(/\x1b\[35mPANIC/);
    // Inside a longer word it is not the keyword.
    expect(out).not.toMatch(/\x1b\[35mpanicky/);
  });

  it('wins over the built-in set for the same token', () => {
    const highlighter = new OutputHighlighter(['shell']);
    highlighter.setCustomRules([rule('deploy', { colour: 'red' })]);
    const out = decoder.decode(highlighter.process(new TextEncoder().encode('deploy now')));
    expect(out).toMatch(/\x1b\[31mdeploy/);
  });

  it('supports a regular expression, and reports one that will not compile', () => {
    const highlighter = new OutputHighlighter(['shell']);
    const errors = highlighter.setCustomRules([
      rule('job\\s*#?\\d+', { regex: true, colour: 'cyan' }),
      rule('(unclosed', { regex: true }),
    ]);
    expect(errors).toHaveLength(1);
    expect(errors[0]).toContain('(unclosed');

    const out = decoder.decode(highlighter.process(new TextEncoder().encode('job #42 started')));
    expect(out).toMatch(/\x1b\[36mjob #42/);
  });

  it('ignores rules that are disabled', () => {
    const highlighter = new OutputHighlighter([]);
    highlighter.setCustomRules([rule('deploy', { enabled: false })]);
    const out = decoder.decode(highlighter.process(new TextEncoder().encode('deploy')));
    expect(out).toBe('deploy');
  });

  it('keeps colouring even when every built-in set is off', () => {
    const highlighter = new OutputHighlighter([]);
    highlighter.setCustomRules([rule('deploy')]);
    expect(highlighter.enabled).toBe(true);
    const out = decoder.decode(highlighter.process(new TextEncoder().encode('deploy')));
    expect(out).toMatch(/\x1b\[35mdeploy/);
  });
});
