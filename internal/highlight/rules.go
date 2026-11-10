package highlight

import (
	"regexp"
	"sort"
	"strings"
)

// SGR sequences. Colour is the only thing a rule changes; everything that was
// already in the stream is replayed afterwards via Highlighter.style.
const (
	reset = "\x1b[0m"

	red     = "\x1b[31m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	blue    = "\x1b[1;34m"
	magenta = "\x1b[35m"
	cyan    = "\x1b[36m"
	white   = "\x1b[1;37m"
	grey    = "\x1b[90m"

	brightRed     = "\x1b[1;31m"
	brightGreen   = "\x1b[1;32m"
	brightCyan    = "\x1b[1;36m"
	brightMagenta = "\x1b[1;35m"
	urlSGR        = "\x1b[4;36m"
)

type rule struct {
	re     *regexp.Regexp
	group  int
	sgr    string
	accept func(line string, start, end int) bool
}

type ruleSet struct {
	id         string
	vocabulary bool
	sniff      func(string) bool
	rules      []rule
}

type edit struct {
	start int
	end   int
	sgr   string
}

func mustRule(pattern string, group int, sgr string, accept func(string, int, int) bool) rule {
	return rule{re: regexp.MustCompile(pattern), group: group, sgr: sgr, accept: accept}
}

func applySet(line string, rules []rule, mask []byte, edits *[]edit) {
	for _, r := range rules {
		for _, m := range r.re.FindAllStringSubmatchIndex(line, -1) {
			start, end := m[0], m[1]
			if r.group > 0 {
				if 2*r.group+1 >= len(m) {
					continue
				}
				start, end = m[2*r.group], m[2*r.group+1]
			}
			if start < 0 || end <= start {
				continue
			}
			if r.accept != nil && !r.accept(line, start, end) {
				continue
			}
			if claimed(mask, start, end) {
				continue
			}
			for i := start; i < end; i++ {
				mask[i] = 1
			}
			*edits = append(*edits, edit{start: start, end: end, sgr: r.sgr})
		}
	}
}

func claimed(mask []byte, start, end int) bool {
	for i := start; i < end; i++ {
		if mask[i] != 0 {
			return true
		}
	}
	return false
}

func sortEdits(edits []edit) {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }
func isHex(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// tokenBoundary is the "whole word" rule: the match may not be glued to a path,
// a dotted word, an address or an option.
func tokenBoundary(s string, start, end int) bool {
	bad := func(c byte) bool {
		return isLetter(c) || isDigit(c) || c == '_' || c == '-' || c == '.' || c == '@' || c == '/'
	}
	if start > 0 && bad(s[start-1]) {
		return false
	}
	if end < len(s) {
		c := s[end]
		if c == '.' {
			// A full stop ends a sentence ("failed.") but not a path
			// ("failed.log"): only allow it when whitespace follows.
			if end+1 < len(s) && s[end+1] != ' ' && s[end+1] != '\t' && s[end+1] != '\r' && s[end+1] != '\n' {
				return false
			}
		} else if bad(c) {
			return false
		}
	}
	return true
}

func wordBoundary(s string, start, end int) bool {
	bad := func(c byte) bool {
		return isLetter(c) || isDigit(c) || c == '_' || c == '-'
	}
	if start > 0 && bad(s[start-1]) {
		return false
	}
	if end < len(s) && bad(s[end]) {
		return false
	}
	return true
}

func ipv4Boundary(s string, start, end int) bool {
	if start > 0 && (isDigit(s[start-1]) || s[start-1] == '.') {
		return false
	}
	if end < len(s) && (isDigit(s[end]) || s[end] == '.') {
		return false
	}
	return true
}

func ipv6Boundary(s string, start, end int) bool {
	if start > 0 && (isHex(s[start-1]) || s[start-1] == ':') {
		return false
	}
	if end < len(s) && (isHex(s[end]) || s[end] == ':') {
		return false
	}
	return true
}

func hexIDBoundary(s string, start, end int) bool {
	if start > 0 && isHex(s[start-1]) {
		return false
	}
	if end < len(s) && isHex(s[end]) {
		return false
	}
	// A long decimal is a number, not an id.
	for i := start; i < end; i++ {
		if isLetter(s[i]) {
			return true
		}
	}
	return false
}

// The command vocabulary shared by the shell and bash sets. Kept ASCII-only on
// purpose: every token these rules care about is ASCII.
const commands = `apt-get|apt|awk|bash|cat|cd|chgrp|chmod|chown|cp|crontab|curl|cut|df|diff|dmesg|dnf|docker|du|echo|env|find|free|gcc|git|go|grep|gunzip|gzip|head|history|htop|ifconfig|iptables|java|journalctl|killall|kill|less|ln|lsof|ls|make|man|mkdir|more|mount|mv|mysql|nano|netstat|nginx|nmap|node|nohup|npm|passwd|ping|pip3|pip|psql|ps|python3|python|read|rmdir|rm|rsync|scp|sed|ssh-keygen|ssh|su|sudo|systemctl|tail|tar|tee|tmux|top|touch|umount|uname|unzip|vi|vim|wget|which|whoami|xargs|yum|zip`

const severity = `error|errors|err|fail|failed|failure|fatal|crit|critical|alert|emerg|panic|segfault|denied|refused|reject(?:ed)?|timeout|timed[ -]out|invalid|unable|unauthorized|forbidden|too many|exceeded|crash(?:ed)?|broken pipe|no space|out of memory|killed`

const success = `accepted|success|successful|started|opened|enabled|active|listening|ready|done|ok`

// nginxBlock and nginxDirectives cover the configuration language, which is not
// its log format. The list is the common surface rather than the whole manual:
// colouring every symbol would make a misread log line unreadable.
const nginxBlock = `http|server|location|upstream|events|stream|mail|map|geo|types|if|split_clients|limit_except|match`

const nginxDirectives = `user|worker_processes|worker_rlimit_nofile|daemon|pid|error_log|include|default_type|log_format|access_log|sendfile|tcp_nopush|tcp_nodelay|keepalive_timeout|keepalive_requests|gzip|gzip_types|gzip_min_length|gzip_comp_level|client_max_body_size|client_body_buffer_size|client_header_timeout|send_timeout|root|alias|index|try_files|autoindex|expires|add_header|return|rewrite|set|break|listen|server_name|ssl_certificate|ssl_certificate_key|ssl_protocols|ssl_ciphers|ssl_session_cache|ssl_session_timeout|proxy_pass|proxy_set_header|proxy_http_version|proxy_connect_timeout|proxy_read_timeout|proxy_send_timeout|proxy_buffering|proxy_buffer_size|proxy_buffers|proxy_redirect|proxy_cache|proxy_cache_path|proxy_cache_valid|proxy_next_upstream|fastcgi_pass|fastcgi_param|fastcgi_index|fastcgi_read_timeout|uwsgi_pass|scgi_pass|grpc_pass|resolver|resolver_timeout|limit_req|limit_req_zone|limit_conn|limit_conn_zone|allow|deny|auth_basic|auth_basic_user_file|satisfy|real_ip_header|set_real_ip_from|charset|source_charset|error_page|internal|etag|add_trailer`

var (
	nginxAccessSniff = regexp.MustCompile(`(?:\d{1,3}\.){3}\d{1,3}\s+-\s+\S+\s+\[[^\]]+\]\s+"(?:GET|POST|PUT|DELETE|HEAD|OPTIONS|PATCH|CONNECT|TRACE)\s`)
	nginxErrorSniff  = regexp.MustCompile(`\d{4}[-/]\d{2}[-/]\d{2} \d{2}:\d{2}:\d{2} \[(?:emerg|alert|crit|error|warn|notice|info)\]`)
	syslogSniff      = regexp.MustCompile(`(?:^|\s)(?:[A-Z][a-z]{2} {1,2}\d{1,2} \d{2}:\d{2}:\d{2}|\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z?)\s+\S+\s+\S+(?:\[\d+\])?:`)
	bashSniff        = regexp.MustCompile(`(?:^|\s)#!/(?:usr/)?bin/(?:env\s+)?\w*(?:ba|z|k|da)?sh\b|(?:^|\s)(?:then|fi|done|esac|elif)\b|(?:^|\s)function\s+\w+|\w+\s*\(\)\s*\{`)
	nginxConfSniff   = regexp.MustCompile(`^[ \t]*(?:[^\s:]+:\d+[-:][ \t]*)?(?:\d+[-:\t][ \t]*)?(?:` + nginxBlock + `|` + nginxDirectives + `)(?:[ \t]|$)[^;{}]*[;{][ \t]*(?:#[^\r\n]*)?\r?\n?$`)
)

func allSets() []*ruleSet {
	return []*ruleSet{
		shellSet(),
		bashSet(),
		nginxSet(),
		nginxConfSet(),
		syslogSet(),
		dataSet(),
		wordsSet(),
	}
}

func shellSet() *ruleSet {
	return &ruleSet{
		id:         "shell",
		vocabulary: true,
		rules: []rule{
			mustRule(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`, 0, green, nil),
			mustRule(`(?:^|[\s;|&])(#[^\r\n]*)`, 1, grey, nil),
			mustRule(`\$(?:\{[^}]{1,64}\}|[A-Za-z_][A-Za-z0-9_]{0,63}|[?!$#*@-])`, 0, magenta, nil),
			mustRule(`(?:`+commands+`)`, 0, brightCyan, tokenBoundary),
			mustRule(`(?:^|\s)(--?[A-Za-z][\w-]{0,63})`, 1, yellow, nil),
			mustRule(`\|\||&&|2>&1|>>?|<<?|&`, 0, grey, func(s string, start, end int) bool {
				return whitespaceAt(s, start-1) && whitespaceAt(s, end)
			}),
		},
	}
}

func bashSet() *ruleSet {
	return &ruleSet{
		id:         "bash",
		vocabulary: true,
		sniff:      bashSniff.MatchString,
		rules: []rule{
			mustRule(`^#![^\r\n]{1,120}`, 0, brightGreen, nil),
			mustRule(`(?:^|[^\w.$-])(if|then|elif|else|fi|for|while|until|do|done|case|esac|select|in|function|return|break|continue|local|readonly|declare|shift|trap|exit|set|unset)(?:[^\w-]|$)`, 1, brightMagenta, wordBoundary),
			mustRule(`(?:^|[^\w.$-])([A-Za-z_][\w-]{0,63})(\s*\(\s*\)\s*\{?)`, 1, blue, nil),
			mustRule(`\$(?:\{[^}]{1,64}\}|[A-Za-z_][A-Za-z0-9_]{0,63}|\([^)]{1,80}\)|[0-9?!$#@*+-])`, 0, magenta, nil),
			mustRule(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`, 0, green, nil),
			mustRule(`(?:^|[\s;])(#[^\r\n]*)`, 1, grey, nil),
		},
	}
}

func syslogSet() *ruleSet {
	return &ruleSet{
		id:         "syslog",
		vocabulary: true,
		sniff:      syslogSniff.MatchString,
		rules: []rule{
			mustRule(`[A-Z][a-z]{2} +\d{1,2} \d{2}:\d{2}:\d{2}`, 0, grey, nil),
			mustRule(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z?`, 0, grey, nil),
			mustRule(`[A-Za-z][\w.-]{0,31}\[\d{1,7}\]:`, 0, blue, nil),
			mustRule(`(?i)(?:`+severity+`)`, 0, brightRed, wordBoundary),
			mustRule(`(?i)(?:`+success+`)`, 0, green, wordBoundary),
			mustRule(`(?:^|[\s,=])((?:\d{1,3}\.){3}\d{1,3})(?:[\s,]|\[|$)`, 1, cyan, nil),
			mustRule(`(?i)\b(?:user|uid|gid|pid|session|serial|port|from|for|via|ip)[=\s]"?[A-Za-z0-9_@./:\\-]{1,64}"?`, 0, magenta, nil),
		},
	}
}

func nginxSet() *ruleSet {
	return &ruleSet{
		id:         "nginx",
		vocabulary: true,
		sniff: func(line string) bool {
			return nginxAccessSniff.MatchString(line) || nginxErrorSniff.MatchString(line)
		},
		rules: []rule{
			mustRule(`\d{4}[-/]\d{2}[-/]\d{2} \d{2}:\d{2}:\d{2}`, 0, grey, nil),
			mustRule(`\[[^\]\r\n]{4,80}\]`, 0, grey, nil),
			mustRule(`(?i)(?:error|warn(?:ing)?|crit(?:ical)?|alert|emerg|notice|failed|failure|refused|denied|timed[ -]out|no such file|upstream|downstream)`, 0, red, wordBoundary),
			mustRule(`"(?:GET|POST|PUT|DELETE|HEAD|OPTIONS|PATCH|CONNECT|TRACE)[^"]{1,300}?HTTP/[\d.]+"`, 0, white, nil),
			mustRule(`"\s+(2\d{2})(?:[\s"]|$)`, 1, green, nil),
			mustRule(`"\s+(3\d{2})(?:[\s"]|$)`, 1, cyan, nil),
			mustRule(`"\s+(4\d{2})(?:[\s"]|$)`, 1, yellow, nil),
			mustRule(`"\s+(5\d{2})(?:[\s"]|$)`, 1, brightRed, nil),
			mustRule(`(?:^|[\s,])((?:\d{1,3}\.){3}\d{1,3})(?:[\s,]|\[|$)`, 1, cyan, nil),
			mustRule(`"\s+\d{3}\s+(\d{2,})\b`, 1, yellow, nil),
			mustRule(`\b(?:nginx|php-fpm|upstream)[^\r\n]{0,80}`, 0, yellow, nil),
		},
	}
}

func nginxConfSet() *ruleSet {
	return &ruleSet{
		id:         "nginx-conf",
		vocabulary: true,
		sniff: func(line string) bool {
			if strings.Contains(line, " -") {
				return false
			}
			return nginxConfSniff.MatchString(line)
		},
		rules: []rule{
			mustRule(`(?:^|[\s;])(#[^\r\n]*)`, 1, grey, nil),
			mustRule(`"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'`, 0, green, nil),
			mustRule(`https?://[^\s<>"'`+"\x60"+`;]+`, 0, urlSGR, nil),
			mustRule(`\$(?:\{[^}]{1,64}\}|[A-Za-z_][A-Za-z0-9_]{0,63})`, 0, magenta, nil),
			mustRule(`(?:^|[^\w$.-])(`+nginxBlock+`)(?:[^\w-]|$)`, 1, brightMagenta, wordBoundary),
			mustRule(`(?:^|[^\w$./-])(`+nginxDirectives+`)(?:[^\w-]|$)`, 1, brightCyan, wordBoundary),
			mustRule(`(?:^|\s)(\^~|~\*?|=)(?:\s|$)`, 1, grey, nil),
			mustRule(`(?:^|[^\w$.-])(on|off|yes|no|true|false|auto|always|never|last|break|stop|permanent|redirect|master|best)(?:[^\w-]|$)`, 1, yellow, wordBoundary),
			mustRule(`(?:^|[^\w$.:])((?:\d+(?:\.\d+)?[kKmMgGtT][bB]?|\d+[smhd]|\d{1,5}))(?:[^\w.]|$)`, 1, yellow, nil),
			mustRule(`(?:^|[^\w$~.-])((?:/[\w.~+-]+)+/?)(?:[^\w-]|$)`, 1, blue, nil),
			mustRule(`[;{}]`, 0, grey, nil),
		},
	}
}

func dataSet() *ruleSet {
	return &ruleSet{
		id: "data",
		rules: []rule{
			mustRule(`https?://[^\s<>"'`+"\x60"+`]+`, 0, urlSGR, nil),
			mustRule(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`, 0, magenta, nil),
			mustRule(`(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(?:\.(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}(?:/\d{1,2})?`, 0, brightCyan, ipv4Boundary),
			mustRule(`(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}|(?:[0-9A-Fa-f]{1,4}:){1,7}:|(?:[0-9A-Fa-f]{1,4}:){1,6}:[0-9A-Fa-f]{1,4}|(?:[0-9A-Fa-f]{1,4}:){1,5}(?::[0-9A-Fa-f]{1,4}){1,2}|(?:[0-9A-Fa-f]{1,4}:){1,4}(?::[0-9A-Fa-f]{1,4}){1,3}|(?:[0-9A-Fa-f]{1,4}:){1,3}(?::[0-9A-Fa-f]{1,4}){1,4}|(?:[0-9A-Fa-f]{1,4}:){1,2}(?::[0-9A-Fa-f]{1,4}){1,5}|[0-9A-Fa-f]{1,4}:(?::[0-9A-Fa-f]{1,4}){1,6}|:(?:(?::[0-9A-Fa-f]{1,4}){1,7}|:)`, 0, cyan, ipv6Boundary),
			mustRule(`(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}`, 0, magenta, nil),
			mustRule(`[\w.%+-]+@[\w-]+(?:\.[\w-]+)+`, 0, blue, nil),
			mustRule(`\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?`, 0, grey, nil),
			mustRule(`[0-9a-f]{12,64}`, 0, magenta, hexIDBoundary),
		},
	}
}

// wordsSet is the keyword vocabulary that applies to any plain line. It fills
// what the line's own vocabulary did not claim, which is what makes `tail`,
// `cat` and `ps` output readable without teaching the highlighter every log
// format in existence.
func wordsSet() *ruleSet {
	return &ruleSet{
		id: "words",
		rules: []rule{
			mustRule(`(?i)(?:`+severity+`)`, 0, brightRed, tokenBoundary),
			mustRule(`(?i)\b(?:warn|warning)\b`, 0, yellow, tokenBoundary),
			mustRule(`(?i)\b(?:info|notice)\b`, 0, cyan, tokenBoundary),
			mustRule(`(?i)\b(?:debug|trace)\b`, 0, grey, tokenBoundary),
			mustRule(`(?i)(?:`+success+`)`, 0, green, tokenBoundary),
		},
	}
}

func whitespaceAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return true
	}
	return s[i] == ' ' || s[i] == '\t'
}
