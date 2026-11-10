// Package encode turns the remote shell's bytes into UTF-8 for the terminal.
//
// The 1.x frontend decoded GB18030 and friends in JavaScript; that meant every
// byte crossed an IPC boundary and an encoding change rebuilt a decoder in the
// browser. Here the decoder sits on the Go byte stream, one transform per
// session, so a multi-byte character split across two SSH reads still decodes.
package encode

import (
	"io"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

// Name is one of the encodings the settings offer.
type Name string

const (
	UTF8     Name = "utf-8"
	GB18030  Name = "gb18030"
	Big5     Name = "big5"
	ShiftJIS Name = "shift_jis"
)

// Names lists the choices in the order the settings dialog shows them.
func Names() []Name { return []Name{UTF8, GB18030, Big5, ShiftJIS} }

// Label is what a person reads in the settings.
func (n Name) Label() string {
	switch n {
	case GB18030:
		return "GB18030 / GBK / GB2312"
	case Big5:
		return "Big5"
	case ShiftJIS:
		return "Shift_JIS"
	default:
		return "UTF-8"
	}
}

// Parse accepts the setting's stored value, its label, or a common alias.
func Parse(s string) Name {
	lower := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(lower, "gb18030") || strings.Contains(lower, "gbk") || strings.Contains(lower, "gb2312"):
		return GB18030
	case strings.Contains(lower, "big5") || strings.Contains(lower, "big-5"):
		return Big5
	case strings.Contains(lower, "shift") || lower == "sjis" || lower == "cp932":
		return ShiftJIS
	default:
		return UTF8
	}
}

// Reader wraps r so its bytes read as UTF-8. Invalid sequences become U+FFFD,
// as a browser's TextDecoder does, and never swallow the following bytes.
func Reader(r io.Reader, n Name) io.Reader {
	enc := encodingOf(n)
	if enc == nil {
		return r
	}
	return transform.NewReader(r, enc.NewDecoder())
}

func encodingOf(n Name) encoding.Encoding {
	switch n {
	case GB18030:
		return simplifiedchinese.GB18030
	case Big5:
		return traditionalchinese.Big5
	case ShiftJIS:
		return japanese.ShiftJIS
	default:
		return nil
	}
}
