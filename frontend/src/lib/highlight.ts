/*
 * Terminal keyword highlighting, the way MobaXterm does it: recognised words in
 * the *output* get a colour, nothing else about the stream changes.
 *
 * Three rules keep this honest, and the rest of the file exists to serve them:
 *
 *  1. **Only bytes between escape sequences are touched.** The scanner walks
 *     CSI/OSC/charset sequences whole and never inserts anything inside one, so
 *     cursor movement, titles and charset switches survive intact.
 *  2. **The stream is never edited, only decorated.** Existing bytes are copied
 *     out unchanged; the only additions are SGR sequences, which are zero-width.
 *     Because the scan runs on latin1 (one char per byte, byte-transparent), a
 *     chunk boundary in the middle of a UTF-8 character cannot corrupt it — the
 *     bytes are never decoded as text in the first place.
 *  3. **The program's colour wins.** When the remote program picks a colour for
 *     a line — `grep --color`, `ls --color`, a compiler diagnostic, `bat` — the
 *     highlighter stands down for that line and says nothing more about it. It
 *     used to paint over the program's colour and then restore it, which left
 *     grep's own match indistinguishable from a dozen local colours on the same
 *     line: the one thing the program was pointing at became invisible.
 *
 * Bold and underline are style, not colour, so they do not silence a highlight;
 * an explicit foreground/background (palette, 256-colour or truecolor) or
 * reverse video does.
 *
 * Full-screen programs (vim, less, any alternate-screen app) are skipped: a
 * cursor-addressed screen is painted from cells, and inserting colours into it
 * fights the program's own redraws.
 */
import { bytesToLatin1, latin1ToBytes } from './bytes';

export interface HighlightRule {
  /** What the rule matches, for the settings list. */
  example: string;
  /** Must carry the `g` flag; the engine resets `lastIndex` itself. */
  re: RegExp;
  /** The SGR sequence emitted before the match, e.g. `\x1b[1;36m`. */
  sgr: string;
}

export interface HighlightSet {
  id: string;
  label: string;
  description: string;
  /**
   * `vocabulary` sets describe a language and are chosen per line (a syslog line
   * is read with the syslog rules, not the shell ones). `always` sets are data
   * shapes — addresses, ids, timestamps — and apply to whichever vocabulary the
   * line speaks, filling in what that vocabulary did not claim.
   */
  scope: 'vocabulary' | 'always';
  rules: HighlightRule[];
}

/** The colours a user-defined rule can ask for. */
export type HighlightColour =
  | 'red'
  | 'green'
  | 'yellow'
  | 'blue'
  | 'magenta'
  | 'cyan'
  | 'white'
  | 'grey'
  | 'bold'
  | 'underline';

export const COLOUR_SGR: Record<HighlightColour, string> = {
  red: '\x1b[31m',
  green: '\x1b[32m',
  yellow: '\x1b[33m',
  blue: '\x1b[1;34m',
  magenta: '\x1b[35m',
  cyan: '\x1b[36m',
  white: '\x1b[1;37m',
  grey: '\x1b[90m',
  bold: '\x1b[1m',
  underline: '\x1b[4m',
};

/** One user-defined keyword or pattern. */
export interface CustomRule {
  id: string;
  pattern: string;
  colour: HighlightColour;
  /** false = a literal keyword (escaped, whole word), true = a regular expression. */
  regex: boolean;
  /** Matched case-insensitively, which is what log keywords want. */
  ignoreCase: boolean;
  enabled: boolean;
}

const RESET = '\x1b[0m';

/** Anything bigger than this is a paste or a `cat` of a big file: skip the scan. */
const MAX_SCAN_BYTES = 512 * 1024;

/** trzsz's own banner: colouring it would only confuse the eye and the parser. */
const TRANSFER_BANNER = '::TRZSZ:TRANSFER:';

// Shared command vocabulary. Kept ASCII-only on purpose: the scan is latin1, and
// every token these sets care about (commands, keywords, IPs, status codes) is
// ASCII. Non-ASCII bytes are passed through untouched.
const COMMANDS = [
  'apt', 'apt-get', 'awk', 'bash', 'cat', 'cd', 'chgrp', 'chmod', 'chown', 'cp',
  'crontab', 'curl', 'cut', 'df', 'diff', 'dmesg', 'dnf', 'docker', 'du', 'echo',
  'env', 'find', 'free', 'gcc', 'gd', 'git', 'go', 'grep', 'gunzip', 'gzip', 'head',
  'history', 'htop', 'ifconfig', 'iptables', 'java', 'journalctl', 'kill', 'killall',
  'less', 'ln', 'lsof', 'ls', 'make', 'man', 'mkdir', 'more', 'mount', 'mv', 'mysql',
  'nano', 'netstat', 'nmap', 'node', 'nohup', 'npm', 'passwd', 'ping', 'pip', 'pip3',
  'ps', 'psql', 'python', 'python3', 'read', 'rm', 'rmdir', 'rsync', 'scp', 'sed',
  'sh', 'source', 'ssh', 'ssh-keygen', 'ss', 'sudo', 'su', 'systemctl', 'tail', 'tar',
  'tee', 'tmux', 'top', 'touch', 'umount', 'uname', 'unzip', 'vi', 'vim', 'wget',
  'which', 'whoami', 'xargs', 'yum', 'zip',
].join('|');

const SEVERITY =
  'error|errors|err|fail|failed|failure|fatal|crit|critical|alert|emerg|panic|segfault|' +
  'denied|refused|reject(?:ed)?|timeout|timed[ -]out|invalid|unable|unauthorized|forbidden|' +
  'too many|exceeded|crash(?:ed)?|broken pipe|no space|out of memory|killed';

const SUCCESS = 'accepted|success|successful|started|started?|opened|enabled|active|listening|ok\\b';

/**
 * nginx's configuration language, which is not its log format.
 *
 * An access line is read by position (address, then time, then request, then
 * status); a config line is `directive  argument;`. They share almost no
 * vocabulary, so the config gets its own set and its own line sniffer.
 *
 * The two lists below are split for a reason. `NGINX_BLOCK` is the words that
 * open a section; `NGINX_DIRECTIVES` is everything else worth colouring, kept to
 * what a real server block actually contains rather than every symbol in the
 * manual — a list of the whole language would colour half of any log line that
 * was ever misread as config. `server` appears in both: inside an `upstream` it
 * is a member line ending in `;`, not a section opening in `{`.
 */
const NGINX_BLOCK = [
  'http', 'server', 'location', 'upstream', 'events', 'stream', 'mail', 'map', 'geo', 'geoip2',
  'types', 'if', 'split_clients', 'limit_except', 'match', 'cache_use_stale',
].join('|');

const NGINX_DIRECTIVES = [
  // core, process, events
  'user', 'worker_processes', 'worker_cpu_affinity', 'worker_rlimit_nofile', 'worker_rlimit_core',
  'worker_shutdown_timeout', 'worker_priority', 'env', 'daemon', 'master_process', 'pid', 'error_log',
  'lock_file', 'pcre_jit', 'ssl_engine', 'use', 'multi_accept', 'accept_mutex', 'accept_mutex_delay',
  'reuse_port', 'aio', 'aio_write', 'eventport', 'poll', 'thread_pool',
  // files, locations, static
  'root', 'alias', 'internal', 'etag', 'try_files', 'include', 'if_modified_since', 'expires',
  'sendfile', 'tcp_nopush', 'tcp_nodelay', 'sendfile_max_chunk', 'postpone_output', 'output_buffers',
  'directio', 'open_file_cache', 'open_file_cache_valid', 'open_file_cache_min_uses',
  'open_file_cache_errors', 'open_file_cache_not_ok', 'log_not_found', 'log_subrequest',
  'recursive_error_pages', 'server_tokens', 'msie_padding', 'port_in_redirect',
  'server_name_in_redirect', 'absolute_redirect', 'merge_slashes', 'ignore_invalid_headers',
  // request limits and buffers
  'client_max_body_size', 'client_body_buffer_size', 'client_body_timeout', 'client_body_in_file_only',
  'client_body_in_single_buffer', 'client_body_postpone_size', 'client_header_timeout',
  'client_header_buffer_size', 'large_client_header_buffers', 'reset_timedout_connection',
  'send_timeout', 'keepalive_timeout', 'keepalive_requests', 'keepalive_disable', 'keepalive_pool_size',
  'lingering_close', 'lingering_time', 'lingering_timeout', 'limit_rate', 'limit_rate_after',
  'request_pool_size', 'client_body_temp_path', 'proxy_temp_path', 'fastcgi_temp_path',
  'scgi_temp_path', 'uwsgi_temp_path',
  // hashing / sizes of internal tables
  'types_hash_max_size', 'types_hash_bucket_size', 'server_names_hash_max_size',
  'server_names_hash_bucket_size', 'map_hash_max_size', 'map_hash_bucket_size',
  'variables_hash_max_size', 'variables_hash_bucket_size',
  // logging, headers, errors
  'access_log', 'log_format', 'open_log_file_cache', 'add_header', 'add_trailer', 'charset',
  'charset_map', 'charset_types', 'override_charset', 'source_charset', 'error_page', 'types',
  'default_type', 'server_name', 'server_name_include', 'listen', 'protocol', 'server',
  // rewriting and flow
  'rewrite', 'return', 'break', 'set', 'uninitialized_variable_warn', 'rewrite_log', 'limit_req',
  'limit_req_zone', 'limit_req_status', 'limit_req_log_level', 'limit_conn', 'limit_conn_zone',
  'limit_conn_status', 'limit_conn_log_level', 'allow', 'deny', 'satisfy', 'auth_basic',
  'auth_basic_user_file', 'auth_request', 'auth_request_set', 'auth_delay', 'real_ip_header',
  'real_ip_recursive', 'set_real_ip_from', 'geoip_country', 'geoip_city', 'mirror',
  'mirror_request_body',
  // proxy
  'proxy_pass', 'proxy_method', 'proxy_bind', 'proxy_http_version', 'proxy_set_header',
  'proxy_set_body', 'proxy_pass_request_body', 'proxy_pass_request_headers', 'proxy_redirect',
  'proxy_cookie_domain', 'proxy_cookie_path', 'proxy_cookie_flags', 'proxy_hide_header',
  'proxy_pass_header', 'proxy_ignore_headers', 'proxy_intercept_errors', 'proxy_next_upstream',
  'proxy_next_upstream_tries', 'proxy_next_upstream_timeout', 'proxy_buffering', 'proxy_buffer_size',
  'proxy_buffers', 'proxy_busy_buffers_size', 'proxy_max_temp_file_size', 'proxy_temp_file_write_size',
  'proxy_cache', 'proxy_cache_path', 'proxy_cache_valid', 'proxy_cache_key', 'proxy_cache_lock',
  'proxy_cache_use_stale', 'proxy_cache_revalidate', 'proxy_cache_bypass', 'proxy_no_cache',
  'proxy_cache_min_uses', 'proxy_cache_methods', 'proxy_cache_background_update',
  'proxy_cache_convert_head', 'proxy_connect_timeout', 'proxy_send_timeout', 'proxy_read_timeout',
  'proxy_ssl_name', 'proxy_ssl_server_name', 'proxy_ssl_verify', 'proxy_ssl_verify_depth',
  'proxy_ssl_certificate', 'proxy_ssl_certificate_key', 'proxy_ssl_trusted_certificate',
  'proxy_ssl_ciphers', 'proxy_ssl_protocols', 'proxy_ssl_session_reuse', 'proxy_ssl_dhparam',
  'proxy_ssl_password_file', 'proxy_protocol_timeout', 'proxy_protocol',
  // upstreams
  'least_conn', 'ip_hash', 'hash', 'random', 'keepalive', 'zone', 'resolve', 'resolver',
  'resolver_timeout', 'health_check',
  // fastcgi / uwsgi / scgi / grpc
  'fastcgi_pass', 'fastcgi_index', 'fastcgi_split_path_info', 'fastcgi_param', 'fastcgi_hide_header',
  'fastcgi_pass_header', 'fastcgi_ignore_headers', 'fastcgi_intercept_errors', 'fastcgi_buffering',
  'fastcgi_buffer_size', 'fastcgi_buffers', 'fastcgi_busy_buffers_size', 'fastcgi_cache',
  'fastcgi_cache_valid', 'fastcgi_cache_path', 'fastcgi_cache_key', 'fastcgi_cache_use_stale',
  'fastcgi_connect_timeout', 'fastcgi_send_timeout', 'fastcgi_read_timeout', 'fastcgi_next_upstream',
  'fastcgi_keep_conn', 'uwsgi_pass', 'uwsgi_param', 'uwsgi_index', 'uwsgi_read_timeout',
  'uwsgi_buffering', 'scgi_pass', 'scgi_param', 'scgi_read_timeout', 'grpc_pass', 'grpc_set_header',
  'grpc_read_timeout', 'grpc_send_timeout',
  // compression, content filters
  'gzip', 'gzip_comp_level', 'gzip_disable', 'gzip_min_length', 'gzip_buffers', 'gzip_http_version',
  'gzip_proxied', 'gzip_types', 'gzip_vary', 'gzip_window', 'gzip_static', 'sub_filter',
  'sub_filter_once', 'sub_filter_types', 'mp4', 'flv', 'secure_link', 'stub_status', 'status', 'perl',
  // tls and HTTP/2-3
  'ssl', 'ssl_certificate', 'ssl_certificate_key', 'ssl_password_file', 'ssl_protocols', 'ssl_ciphers',
  'ssl_prefer_server_ciphers', 'ssl_session_cache', 'ssl_session_timeout', 'ssl_session_tickets',
  'ssl_session_ticket_key', 'ssl_dhparam', 'ssl_ecdh_curve', 'ssl_crl', 'ssl_stapling',
  'ssl_stapling_verify', 'ssl_trusted_certificate', 'ssl_buffer_size', 'ssl_early_data', 'http2',
  'http3', 'early_hints', 'quic_retry', 'quic_gso', 'quic_host_key', 'quic_stateless_reset',
].join('|');

/** Anything that may stand at the start of a config statement. */
const NGINX_STATEMENT = `${NGINX_BLOCK}|${NGINX_DIRECTIVES}`;

/**
 * Data shapes rather than a language: addresses, ids, numbers, timestamps.
 *
 * These apply to every line, whatever vocabulary it speaks, and only after that
 * vocabulary has had its pick — so the client address at the start of an nginx
 * line keeps the nginx colour instead of being repainted here.
 */
const DATA_SHAPES: HighlightSet = {
  id: 'data',
  scope: 'always',
  label: 'Addresses, ids and numbers',
  description:
    'IPv4/IPv6, MAC addresses, UUIDs, phone numbers, e-mail addresses, URLs and ISO timestamps, on any kind of line.',
  rules: [
    // URL first: everything inside it would otherwise be fair game for the rest.
    { example: 'https://example.com/a?b=1', re: /\bhttps?:\/\/[^\s<>"'`]+/g, sgr: '\x1b[4;36m' },
    {
      example: '550e8400-e29b-41d4-a716-446655440000',
      re: /\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b/g,
      sgr: '\x1b[35m',
    },
    {
      example: '10.0.0.8',
      // Octets are range-checked so 1.2.3.999 is not dressed up as an address.
      re: /(?<![\d.])(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(?:\.(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}(?:\/\d{1,2})?(?![\d.])/g,
      sgr: '\x1b[1;36m',
    },
    {
      example: '2001:db8::1',
      re: /(?<![0-9A-Fa-f:])(?:(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}|(?:[0-9A-Fa-f]{1,4}:){1,7}:|(?:[0-9A-Fa-f]{1,4}:){1,6}:[0-9A-Fa-f]{1,4}|(?:[0-9A-Fa-f]{1,4}:){1,5}(?::[0-9A-Fa-f]{1,4}){1,2}|(?:[0-9A-Fa-f]{1,4}:){1,4}(?::[0-9A-Fa-f]{1,4}){1,3}|(?:[0-9A-Fa-f]{1,4}:){1,3}(?::[0-9A-Fa-f]{1,4}){1,4}|(?:[0-9A-Fa-f]{1,4}:){1,2}(?::[0-9A-Fa-f]{1,4}){1,5}|[0-9A-Fa-f]{1,4}:(?::[0-9A-Fa-f]{1,4}){1,6}|:(?:(?::[0-9A-Fa-f]{1,4}){1,7}|:))(?![0-9A-Fa-f:])/g,
      sgr: '\x1b[36m',
    },
    { example: 'aa:bb:cc:dd:ee:ff', re: /\b(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}\b/g, sgr: '\x1b[35m' },
    // China mobile first, so +86 138… is not read as a generic international
    // number with the +86 as its country code.
    { example: '13812345678', re: /(?<![\d+])(?:\+?86[-\s]?)?1[3-9]\d{9}(?!\d)/g, sgr: '\x1b[1;35m' },
    { example: '+1 555 010 9999', re: /\+\d{1,3}[\s-]?(?:\(?\d{1,4}\)?[\s-]?){2,5}\d{2,4}\b/g, sgr: '\x1b[1;35m' },
    { example: '400-800-1234', re: /\b\d{3,4}-\d{3,4}-\d{4}\b/g, sgr: '\x1b[1;35m' },
    { example: 'ops@example.com', re: /\b[\w.%+-]+@[\w-]+(?:\.[\w-]+)+\b/g, sgr: '\x1b[1;34m' },
    {
      example: '2026-10-23T04:11:02Z',
      re: /\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?\b/g,
      sgr: '\x1b[90m',
    },
    // A long hex run that contains at least one letter: container ids, git
    // hashes, session tokens — but not a long decimal number.
    { example: 'a1b2c3d4e5f6', re: /\b(?=[0-9a-f]*[a-f])[0-9a-f]{12,64}\b/g, sgr: '\x1b[35m' },
  ],
};

export const HIGHLIGHT_SETS: HighlightSet[] = [
  {
    id: 'shell',
    scope: 'vocabulary',
    label: 'Shell commands',
    description: 'Commands, options, variables, quoted strings, pipes and comments in an interactive session.',
    rules: [
      { example: '"file.tar.gz"', re: /"(?:[^"\\\x1b]|\\.)*"|'(?:[^'\\\x1b]|\\.)*'/g, sgr: '\x1b[32m' },
      // A shebang is a comment to a shell and a directive to bash; leaving it
      // to the bash set is what makes `cat script.sh` show it as one.
      { example: '# note', re: /(?:^|(?<=[\s;|&]))#(?!!)[^\x1b\r\n]*/g, sgr: '\x1b[90m' },
      { example: '$HOME', re: /\$(?:\{[^}\x1b]{1,64}\}|[A-Za-z_][A-Za-z0-9_]{0,63}|[?!$#*@-])/g, sgr: '\x1b[35m' },
      {
        example: 'systemctl',
        re: new RegExp(`(?<![\\w./@-])(?:${COMMANDS})(?![\\w-])`, 'g'),
        sgr: '\x1b[1;36m',
      },
      { example: '--verbose', re: /(?<=\s)--?[A-Za-z][\w-]{0,63}/g, sgr: '\x1b[33m' },
      { example: '| && >', re: /(?<=\s)(?:\|\||&&|>>?|<<?|2>&1|&)(?=\s|$)/g, sgr: '\x1b[1;30m' },
    ],
  },
  {
    id: 'bash',
    scope: 'vocabulary',
    label: 'Bash scripts',
    description: 'Control keywords, function bodies, shebangs and expansions in `cat script.sh`.',
    rules: [
      { example: '#!/bin/bash', re: /#!\/[^\x1b\r\n]{1,120}/g, sgr: '\x1b[1;32m' },
      { example: 'if / then / done', re: /(?<![\w-])(?:if|then|elif|else|fi|for|while|until|do|done|case|esac|select|in|function|return|break|continue|local|readonly|declare|shift|trap|exit|set|unset)(?![\w-])/g, sgr: '\x1b[1;35m' },
      { example: 'run() {', re: /(?<![\w.$-])[A-Za-z_][\w-]{0,63}(?=\s*\(\s*\)\s*\{?)/g, sgr: '\x1b[1;34m' },
      { example: '$1', re: /\$(?:\{[^}\x1b]{1,64}\}|[A-Za-z_][A-Za-z0-9_]{0,63}|\([^)]{1,80}\)|[0-9?!\$#@*+-])/g, sgr: '\x1b[35m' },
      { example: '"text"', re: /"(?:[^"\\\x1b]|\\.)*"|'(?:[^'\\\x1b]|\\.)*'/g, sgr: '\x1b[32m' },
      { example: '# comment', re: /(?:^|(?<=[\s;]))#[^\x1b\r\n]*/g, sgr: '\x1b[90m' },
    ],
  },
  {
    id: 'nginx',
    scope: 'vocabulary',
    label: 'nginx logs',
    description: 'Client address, time, request, status class and byte counts in access/error logs.',
    rules: [
      { example: '2026/10/23 04:11:05 [error]', re: /(?:^|(?<=\s))\d{4}[-\/]\d{2}[-\/]\d{2} \d{2}:\d{2}:\d{2}(?=\s\[)/g, sgr: '\x1b[90m' },
      { example: '[23/Oct/2026:04:11:02 +0800]', re: /\[(?!\x1b)[^\]\x1b\r\n]{4,80}\]/g, sgr: '\x1b[90m' },
      { example: 'failed / refused / upstream', re: /(?<![\w-])(?:error|warn(?:ing)?|crit(?:ical)?|alert|emerg|notice|failed|failure|refused|denied|timed[ -]out|no such file|upstream|downstream)(?![\w-])/gi, sgr: '\x1b[31m' },
      { example: 'GET /api/x HTTP/1.1', re: /"(?:GET|POST|PUT|DELETE|HEAD|OPTIONS|PATCH|CONNECT|TRACE)\s[^"\x1b]{1,300}?HTTP\/[\d.]+"/g, sgr: '\x1b[1;37m' },
      { example: '200', re: /(?<="\s)2\d{2}(?=\s)/g, sgr: '\x1b[32m' },
      { example: '301', re: /(?<="\s)3\d{2}(?=\s)/g, sgr: '\x1b[36m' },
      { example: '404', re: /(?<="\s)4\d{2}(?=\s)/g, sgr: '\x1b[33m' },
      { example: '502', re: /(?<="\s)5\d{2}(?=\s)/g, sgr: '\x1b[1;31m' },
      { example: '10.0.0.8', re: /(?:^|(?<=[\s,]))(?:\d{1,3}\.){3}\d{1,3}(?=[\s,]|\[)/g, sgr: '\x1b[36m' },
      { example: '1024', re: /(?<=\s\d{3}\s)\d{2,}\b/g, sgr: '\x1b[33m' },
      { example: 'nginx: [error] …', re: /\b(?:nginx|php-fpm|upstream)[^\x1b\r\n]{0,80}/g, sgr: '\x1b[33m' },
    ],
  },
  {
    id: 'nginx-conf',
    scope: 'vocabulary',
    label: 'nginx config',
    description:
      'Directives, sections, variables, paths and values in nginx.conf and conf.d — what `nginx -T` or `cat` prints, not the access log.',
    rules: [
      { example: '# note', re: /(?:^|(?<=\s))#[^\x1b\r\n]*/g, sgr: '\x1b[90m' },
      { example: '"max-age=31536000"', re: /"(?:[^"\\\x1b]|\\.)*"|'(?:[^'\\\x1b]|\\.)*'/g, sgr: '\x1b[32m' },
      // URL before the path rule, so `http://backend` is not eaten from the middle.
      // `;` is excluded here though the data set allows it: in a config file the
      // semicolon is the end of the statement, and swallowing it would colour the
      // one character that tells a reader where the directive stops.
      { example: 'http://backend', re: /\bhttps?:\/\/[^\s<>"'`;]+/g, sgr: '\x1b[4;36m' },
      { example: '$document_root', re: /\$(?:\{[^}\x1b]{1,64}\}|[A-Za-z_][A-Za-z0-9_]{0,63})/g, sgr: '\x1b[35m' },
      {
        example: 'server { / location',
        re: new RegExp(`(?<![\\w$.-])(?:${NGINX_BLOCK})(?![\\w-])`, 'g'),
        sgr: '\x1b[1;35m',
      },
      {
        example: 'proxy_pass',
        // `/` and `.` in the lookbehind: a directive name inside a path or a
        // dotted word (`mime.types`) is not a directive.
        re: new RegExp(`(?<![\\w$./-])(?:${NGINX_DIRECTIVES})(?![\\w-])`, 'g'),
        sgr: '\x1b[1;36m',
      },
      // location modifiers: they are operators, not values.
      { example: '= / ^~ / ~ / ~*', re: /(?<=\s)(?:\^~|~\*?|=)(?=\s)/g, sgr: '\x1b[1;30m' },
      {
        example: 'on / off / permanent',
        re: /(?<![\w$.-])(?:on|off|yes|no|true|false|auto|always|never|last|break|stop|permanent|redirect|master|best)(?![\w-])/g,
        sgr: '\x1b[1;33m',
      },
      {
        example: '10m / 1g / 30s / 80',
        // The lookbehind and the lookahead keep an IP, a version and `http2` out;
        // only a whole number standing on its own is a size, a port or a timeout.
        re: /(?<![\w$.:])(?:\d+(?:\.\d+)?[kKmMgGtT][bB]?|\d+[smhd]|\d{1,5})(?![\w.])/g,
        sgr: '\x1b[33m',
      },
      { example: '/var/log/nginx/error.log', re: /(?<![\w$~.-])(?:\/[\w.~+-]+)+\/?(?![\w-])/g, sgr: '\x1b[1;34m' },
      { example: '; { }', re: /[;{}]/g, sgr: '\x1b[1;30m' },
    ],
  },
  {
    id: 'syslog',
    scope: 'vocabulary',
    label: 'Linux messages',
    description: 'Timestamps, service tags and severity words in /var/log/messages, syslog and journald output.',
    rules: [
      { example: 'Oct 23 04:11:02', re: /\b[A-Z][a-z]{2} +\d{1,2} \d{2}:\d{2}:\d{2}\b/g, sgr: '\x1b[90m' },
      { example: 'sshd[4012]:', re: /\b[A-Za-z][\w.-]{0,31}\[\d{1,7}\]:/g, sgr: '\x1b[1;34m' },
      { example: 'Failed password', re: new RegExp(`(?<![\\w-])(?:${SEVERITY})(?![\\w-])`, 'gi'), sgr: '\x1b[1;31m' },
      { example: 'Accepted publickey', re: new RegExp(`(?<![\\w-])(?:${SUCCESS})`, 'gi'), sgr: '\x1b[32m' },
      { example: '10.0.0.8', re: /(?:^|(?<=[\s,=]))(?:\d{1,3}\.){3}\d{1,3}(?=[\s,]|\b)/g, sgr: '\x1b[36m' },
      { example: 'user="root"', re: /\b(?:user|uid|gid|pid|session|serial|port|from|for|via|ip)[=\s]"?[A-Za-z0-9_@./:\\-]{1,64}"?/gi, sgr: '\x1b[35m' },
    ],
  },
  DATA_SHAPES,
];

export const DEFAULT_HIGHLIGHT_SETS = HIGHLIGHT_SETS.map((set) => set.id);

/**
 * Line sniffers.
 *
 * Each set is applied only to lines that look like its domain. Without this, a
 * syslog line was being read by the shell rules (every `for` went magenta) and
 * an nginx request line by the shell string rule (the whole quoted request went
 * green instead of the method rule's white). One line, one vocabulary.
 */
const NGINX_ACCESS =
  /(?:\d{1,3}\.){3}\d{1,3}\s+-\s+\S+\s+\[(?!\x1b)[^\]]+\]\s+"(?:GET|POST|PUT|DELETE|HEAD|OPTIONS|PATCH|CONNECT|TRACE)\s/;
const NGINX_ERROR = /\d{4}[-\/]\d{2}[-\/]\d{2} \d{2}:\d{2}:\d{2} \[(?:emerg|alert|crit|error|warn|notice|info)\]/;
/**
 * A config statement: a keyword nginx owns at the start of the line — after the
 * indent, and after whatever the command that printed the file put in front of
 * it — then arguments, then `;` or `{`.
 *
 * The prefix matters more than it looks. Nobody `cat`s a config file plain very
 * often; the shapes that actually reach a terminal are `cat -n` (a number and a
 * tab), `grep -n` (`24:`) and `grep -rn /etc/nginx` (`nginx.conf:24:`), so all
 * three are allowed for, and a statement still has to follow them.
 *
 * Two details are the whole craft beyond that. Quoted spans are matched as units
 * so a `;` inside `"max-age=31536000; includeSubDomains"` is not mistaken for the
 * end of the statement. And a flag-looking token disqualifies the line: `set -e;`
 * is a shell line whose first word nginx also owns, and without that guard a
 * loose pattern reads every `set`/`return`/`break` in a script as server config.
 */
const NGINX_CONF_LINE = new RegExp(
  '^[ \\t]*(?:[^\\s:]+:\\d+[-:][ \\t]*)?(?:\\d+[-:\\t][ \\t]*)?(?:' +
    `(?:${NGINX_STATEMENT})(?![\\w-])(?![^#\\x1b{}]*(?:[ \\t])-)` +
    '(?:[^"\'\\x1b{};]|"[^"\\x1b]*"|\'[^\'\\x1b]*\')*' +
    '(?:;|\\{)[ \\t]*(?:#[^\\n]*)?\\r?\\n?)$',
);

const SYSLOG_LINE = /(?:^|\s)(?:[A-Z][a-z]{2} {1,2}\d{1,2} \d{2}:\d{2}:\d{2}|\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z?)\s+\S+\s+\S+(?:\[\d+\])?:/;
const BASH_SCRIPT =
  /(?:^|\s)#!\/(?:usr\/)?bin\/(?:env\s+)?\w*(?:ba|z|k|da)?sh\b|(?:^|\s)(?:then|fi|done|esac|elif)\b|(?:^|\s)function\s+\w+|\w+\s*\(\)\s*\{/;

export function highlightSetLabel(id: string): string {
  return HIGHLIGHT_SETS.find((set) => set.id === id)?.label ?? id;
}

/**
 * Decorates terminal output with colour.
 *
 * One instance per terminal: it carries that terminal's style state (the SGR
 * sequences seen so far) and whether the alternate screen is showing, both of
 * which are properties of the stream, not of the settings.
 */
export class OutputHighlighter {
  private sets: HighlightSet[] = [];
  /** Vocabulary sets, per line; always-sets apply after them. */
  private vocabulary: HighlightSet[] = [];
  private always: HighlightSet[] = [];
  /** Compiled user rules: they win, because they are what the user asked for. */
  private custom: HighlightRule[] = [];
  /** SGR sequences seen so far, replayed after each highlight. */
  private style = '';
  /**
   * Set once the program puts a colour on the line being painted, and cleared at
   * the line break. Highlighting on top of that competes with the one thing the
   * remote program was pointing at, so for the rest of that line it keeps quiet.
   *
   * Per line, not per style state: `grep` colours a match and does not always
   * reset afterwards, and a suppressor that outlived the line would silence the
   * highlighter for the rest of the session.
   */
  private lineTinted = false;
  private altScreen = false;
  /**
   * An escape sequence that the chunk boundary cut in half. It is carried to the
   * next chunk rather than guessed at, so a colour can never be inserted inside
   * a cursor-movement sequence that happened to arrive in two pieces.
   */
  private carry = '';

  constructor(setIds: string[] = []) {
    this.setSets(setIds);
  }

  setSets(setIds: string[]): void {
    const seen = new Set<string>();
    const sets: HighlightSet[] = [];
    for (const id of setIds) {
      if (seen.has(id)) continue;
      seen.add(id);
      const set = HIGHLIGHT_SETS.find((candidate) => candidate.id === id);
      // Order is the caller's: where two sets claim the same characters, the
      // one listed first wins.
      if (set) sets.push(set);
    }
    this.sets = sets;
    this.vocabulary = sets.filter((set) => set.scope === 'vocabulary');
    this.always = sets.filter((set) => set.scope === 'always');
  }

  /**
   * Install the user's own keywords and patterns.
   *
   * A rule that will not compile is dropped rather than thrown: one bad pattern
   * must not take the terminal's output down with it. The caller gets the
   * reasons back and shows them.
   */
  setCustomRules(rules: CustomRule[]): string[] {
    const { compiled, errors } = compileCustomRules(rules);
    this.custom = compiled;
    return errors;
  }

  get enabled(): boolean {
    return this.sets.length > 0 || this.custom.length > 0;
  }

  /** True while a full-screen program (vim, less, htop) owns the screen. */
  get altScreenActive(): boolean {
    return this.altScreen;
  }

  /** Forget the style state — after a clear, a reconnect, or a tab swap. */
  reset(): void {
    this.style = '';
    this.lineTinted = false;
    this.altScreen = false;
    this.carry = '';
  }

  /** Returns bytes to hand to xterm. Never loses or reorders an input byte. */
  process(bytes: Uint8Array): Uint8Array {
    if (!this.enabled) {
      // Colouring is off, but the alternate-screen flag is not only a colouring
      // concern: the anti-idle must not type an Enter into vim. Track the two
      // sequences that matter, cheaply, and hand the bytes back untouched.
      if (bytes.indexOf(0x1b) >= 0) this.trackAltScreen(bytesToLatin1(bytes));
      // Still hand back anything carried, or a chunk would go missing.
      if (!this.carry) return bytes;
      const tail = latin1ToBytes(this.carry);
      this.carry = '';
      return bytes.length ? concatBytes(tail, bytes) : tail;
    }
    if (bytes.length > MAX_SCAN_BYTES) {
      const tail = latin1ToBytes(this.carry);
      this.carry = '';
      return bytes.length ? concatBytes(tail, bytes) : tail;
    }

    const text = this.carry + bytesToLatin1(bytes);
    this.carry = '';
    if (!text) return bytes;

    let out = '';
    let runStart = 0;
    let i = 0;
    while (i < text.length) {
      if (text.charCodeAt(i) !== 0x1b) {
        i += 1;
        continue;
      }
      if (i > runStart) out += this.paint(text.slice(runStart, i));
      const found = escapeEnd(text, i);
      if (!found.complete) {
        // Wait for the rest of it — unless it never finishes, in which case the
        // carry cap keeps a truncated stream from swallowing bytes forever.
        if (text.length - i > 32) out += text.slice(i);
        else this.carry = text.slice(i);
        return latin1ToBytes(out);
      }
      const sequence = text.slice(i, found.end);
      this.observe(sequence);
      out += sequence;
      i = found.end;
      runStart = found.end;
    }
    if (runStart < text.length) out += this.paint(text.slice(runStart));
    return latin1ToBytes(out);
  }

  /** Colour one stretch of ordinary text. The active style is constant here. */
  private paint(run: string): string {
    if (this.altScreen) return run;

    let out = '';
    for (const line of run.split(/(?<=\n)/)) {
      // One colour decision per line: a log line and a shell prompt do not share
      // a vocabulary, and pretending they do is how the noise starts.
      out += this.lineTinted || this.silent(line) ? line : this.paintLine(line);
      // The latch lives exactly as long as the line that earned it, so a coloured
      // prompt cannot silence the output underneath it.
      if (line.endsWith('\n')) this.lineTinted = false;
    }
    return out;
  }

  /** Stretches no rule can say anything useful about. */
  private silent(line: string): boolean {
    return line.includes(TRANSFER_BANNER) || !/[A-Za-z0-9]/.test(line);
  }

  private paintLine(line: string): string {
    // A single 64 KiB "line" is a minified JSON blob or a base64 dump, not a log
    // entry: scanning every rule over it costs real time and cannot look good.
    if (line.length > 8192) return line;

    const claimed = new Uint8Array(line.length);
    const edits: Edit[] = [];

    // Three tiers, in the order a reader would expect them to win: what the user
    // asked for, then the vocabulary the line speaks, then data shapes that fit
    // any line and only fill what is left.
    applyRules(line, this.custom, claimed, edits);
    for (const set of this.setsForLine(line)) applyRules(line, set.rules, claimed, edits);
    for (const set of this.always) applyRules(line, set.rules, claimed, edits);

    if (!edits.length) return line;

    edits.sort((a, b) => a.start - b.start);
    let out = '';
    let at = 0;
    for (const edit of edits) {
      out += line.slice(at, edit.start);
      // Drop the caller's colour, add ours, then put the caller's back.
      out += RESET + edit.sgr + line.slice(edit.start, edit.end) + RESET + this.style;
      at = edit.end;
    }
    return out + line.slice(at);
  }

  /** Which vocabulary a line speaks. Empty when only the data sets are enabled. */
  private setsForLine(line: string): HighlightSet[] {
    const find = (id: string) => this.vocabulary.find((set) => set.id === id);
    const nginx = find('nginx');
    const nginxConf = find('nginx-conf');
    const syslog = find('syslog');
    const bash = find('bash');
    const shell = find('shell');

    if (nginx && (NGINX_ACCESS.test(line) || NGINX_ERROR.test(line))) return [nginx];
    // Config before the shell fallback and before bash: `set`, `return` and
    // `break` are words both languages use, and the line's shape decides.
    if (nginxConf && NGINX_CONF_LINE.test(line)) return [nginxConf];
    if (syslog && SYSLOG_LINE.test(line)) return [syslog];
    if (bash && BASH_SCRIPT.test(line)) return shell ? [bash, shell] : [bash];
    return shell ? [shell] : [];
  }

  /** Track what the program's own sequences mean. */
  /** Track what the program's own sequences mean. */
  private trackAltScreen(text: string): void {
    // The alternate screen is entered and left with these three codes; matching
    // them directly is cheaper than the full tokenizer and good enough for a
    // flag that only decides whether an idle Enter is safe.
    const re = /\x1b\[\?(?:1049|1047|47)([hl])/g;
    let match: RegExpExecArray | null = re.exec(text);
    while (match) {
      this.altScreen = match[1] === 'h';
      if (this.altScreen) this.style = '';
      match = re.exec(text);
    }
  }

  private observe(sequence: string): void {
    if (!sequence.startsWith('\x1b[') || sequence.length < 3) return;
    const body = sequence.slice(2, -1);
    const final = sequence[sequence.length - 1] ?? '';

    if (final === 'h' || final === 'l') {
      const code = body.startsWith('?') ? body.slice(1) : '';
      if (code === '1049' || code === '1047' || code === '47') {
        this.altScreen = final === 'h';
        if (this.altScreen) {
          this.style = '';
          this.lineTinted = false;
        }
      }
      return;
    }
    if (final !== 'm') return;

    if (body === '' || body === '0') {
      this.style = '';
      return;
    }
    // Only the latch matters here: whether the program's colour is still in force
    // when the line ends is the program's business, and `this.style` already
    // tracks it for the replay after each highlight.
    if (sgrPicksColour(body)) this.lineTinted = true;
    // A sequence that starts with a reset replaces the accumulated state;
    // anything else adds to it, because SGR attributes persist independently
    // (`\x1b[1m` then `\x1b[31m` is bold red, not red).
    this.style = (body.startsWith('0;') ? sequence : this.style + sequence).slice(-96);
  }
}

/**
 * Does this SGR sequence pick a colour?
 *
 * True for an explicit foreground or background in any of the three forms, and
 * for reverse video: the program has chosen a colour, and a local highlight on
 * top of it would compete with what the program is pointing at.
 *
 * False for bold, dim, italic, underline, blink and friends — style, not colour,
 * and no reason to silence a highlight — and for the sequences that put the
 * terminal back to defaults.
 */
function sgrPicksColour(body: string): boolean {
  const parts = body.split(';');
  let tint = false;

  for (let i = 0; i < parts.length; i += 1) {
    const raw = parts[i] as string;
    const code = raw === '' ? 0 : Number(raw);
    if (!Number.isInteger(code)) continue;

    if (code === 38 || code === 48) {
      // Extended colour: `38;5;n` (palette) or `38;2;r;g;b` (truecolor). Both the
      // mode and the value belong to this one parameter, so step over them.
      const mode = Number(parts[i + 1]);
      if (mode === 5) {
        tint = true;
        i += 2;
      } else if (mode === 2) {
        tint = true;
        i += 4;
      }
      continue;
    }
    if (code === 7) tint = true;
    else if ((code >= 30 && code <= 37) || (code >= 90 && code <= 97) || code === 109
      || (code >= 40 && code <= 47) || (code >= 100 && code <= 107)) tint = true;
  }

  // A sequence that resets and colours in one breath (`\x1b[01;31m`, grep's own
  // match style) is a colour choice.
  return tint;
}

/**
 * Where an escape sequence starting at `i` ends, and whether it ended at all:
 * a sequence cut off by the end of the chunk reports `complete: false` so the
 * caller can wait for the rest instead of painting inside it.
 */
function escapeEnd(text: string, i: number): { end: number; complete: boolean } {
  if (i + 1 >= text.length) return { end: text.length, complete: false };
  const next = text[i + 1] as string;

  if (next === '[') {
    let j = i + 2;
    while (j < text.length && text.charCodeAt(j) >= 0x30 && text.charCodeAt(j) <= 0x3f) j += 1;
    while (j < text.length && text.charCodeAt(j) >= 0x20 && text.charCodeAt(j) <= 0x2f) j += 1;
    return j < text.length ? { end: j + 1, complete: true } : { end: text.length, complete: false };
  }

  // OSC / DCS / SOS / PM / APC: terminated by BEL or ST.
  if (next === ']' || next === 'P' || next === 'X' || next === '^' || next === '_') {
    let j = i + 2;
    while (j < text.length) {
      const code = text.charCodeAt(j);
      if (code === 0x07) return { end: j + 1, complete: true };
      if (code === 0x1b && text[j + 1] === '\\') return { end: j + 2, complete: true };
      j += 1;
    }
    return { end: text.length, complete: false };
  }

  // Designate charset: ESC ( A and friends.
  if (next === '(' || next === ')' || next === '*' || next === '+') {
    return text.length >= i + 3 ? { end: i + 3, complete: true } : { end: text.length, complete: false };
  }

  return { end: i + 2, complete: true };
}

function concatBytes(first: Uint8Array, second: Uint8Array): Uint8Array {
  if (!first.length) return second;
  if (!second.length) return first;
  const out = new Uint8Array(first.length + second.length);
  out.set(first, 0);
  out.set(second, first.length);
  return out;
}

interface Edit {
  start: number;
  end: number;
  sgr: string;
}

/** Run one rule list over a line, claiming characters as it goes. */
function applyRules(line: string, rules: HighlightRule[], claimed: Uint8Array, edits: Edit[]): void {
  for (const rule of rules) {
    rule.re.lastIndex = 0;
    let match: RegExpExecArray | null = rule.re.exec(line);
    while (match) {
      const start = match.index;
      const end = start + match[0].length;
      if (end === start) {
        // A rule that can match nothing would loop forever on this line.
        rule.re.lastIndex += 1;
        match = rule.re.exec(line);
        continue;
      }
      if (!isClaimed(claimed, start, end)) {
        claim(claimed, start, end);
        edits.push({ start, end, sgr: rule.sgr });
      }
      match = rule.re.exec(line);
    }
  }
}

function escapeRegex(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/**
 * Turn the user's rules into regexes.
 *
 * A keyword is escaped and wrapped in word boundaries so `ls` does not light up
 * inside `false`; a pattern is used as written. Anything that does not compile
 * is reported and skipped — one typo must not cost the terminal its output.
 */
export function compileCustomRules(rules: CustomRule[]): { compiled: HighlightRule[]; errors: string[] } {
  const compiled: HighlightRule[] = [];
  const errors: string[] = [];
  for (const rule of rules) {
    const pattern = rule.pattern.trim();
    if (!rule.enabled || !pattern) continue;
    const source = rule.regex ? pattern : `(?<![\\w-])${escapeRegex(pattern)}(?![\\w-])`;
    try {
      compiled.push({
        example: pattern,
        re: new RegExp(source, rule.ignoreCase ? 'gi' : 'g'),
        sgr: COLOUR_SGR[rule.colour] ?? COLOUR_SGR.yellow,
      });
    } catch (error) {
      errors.push(`${pattern}: ${error instanceof Error ? error.message : String(error)}`);
    }
  }
  return { compiled, errors };
}

function isClaimed(mask: Uint8Array, start: number, end: number): boolean {
  for (let i = start; i < end; i += 1) if (mask[i]) return true;
  return false;
}

function claim(mask: Uint8Array, start: number, end: number): void {
  for (let i = start; i < end; i += 1) mask[i] = 1;
}
