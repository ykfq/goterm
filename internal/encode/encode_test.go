package encode

import (
	"bytes"
	"io"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestUTF8PassesThrough(t *testing.T) {
	in := []byte("中文终端")
	got, err := io.ReadAll(Reader(bytes.NewReader(in), UTF8))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, in) {
		t.Fatalf("utf-8 changed: %q", got)
	}
}

func TestGB18030DecodesAcrossReadBoundaries(t *testing.T) {
	// 中 in GB18030 is D6 D0; feeding one byte at a time must still decode it.
	want := "中文终端测试"
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(want))
	if err != nil {
		t.Fatal(err)
	}
	br := &oneByteReader{r: bytes.NewReader(encoded)}
	got, err := io.ReadAll(Reader(br, GB18030))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("decoded %q, want %q", got, want)
	}
}

func TestInvalidSequenceBecomesReplacementWithoutEatingTheRest(t *testing.T) {
	// 0xFF is invalid in GB18030; "abc" after it must survive.
	got, err := io.ReadAll(Reader(bytes.NewReader([]byte{0xff, 'a', 'b', 'c'}), GB18030))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("abc")) {
		t.Fatalf("bytes after the invalid one were lost: %q", got)
	}
}

func TestParseAcceptsAliases(t *testing.T) {
	cases := map[string]Name{
		"":                       UTF8,
		"utf-8":                  UTF8,
		"GBK":                    GB18030,
		"gb2312":                 GB18030,
		"Big5":                   Big5,
		"shift-jis":              ShiftJIS,
		"cp932":                  ShiftJIS,
		"GB18030 / GBK / GB2312": GB18030,
	}
	for in, want := range cases {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
}

type oneByteReader struct{ r io.Reader }

func (o *oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
}
