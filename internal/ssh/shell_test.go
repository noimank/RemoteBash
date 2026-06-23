package ssh

import (
	"strings"
	"testing"
)

func TestStripANSI(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"\x1b[32mgreen\x1b[0m", "green"},
		{"\x1b[1;32mbold green\x1b[0m", "bold green"},
		{"\x1b]0;title\x07text", "text"},
		{"\x1b]0;title\x1b\\text", "text"},
		{"no color \x1b[31mred\x1b[0m here", "no color red here"},
		{"\x1b[?25lhidden cursor", "hidden cursor"},
		{"", ""},
	}
	for _, tc := range cases {
		got := string(StripANSI([]byte(tc.in)))
		if got != tc.want {
			t.Errorf("StripANSI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ── normalizeOutput ──────────────────────────────────────────────────────

func TestNormalizeOutput_CRLF(t *testing.T) {
	raw := []byte("line1\r\nline2\r\nline3")
	got := normalizeOutput(raw)
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_StandaloneCR(t *testing.T) {
	raw := []byte("progress: 0%\rprogress: 50%\rprogress: 100%")
	got := normalizeOutput(raw)
	want := "progress: 0%\nprogress: 50%\nprogress: 100%"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Regression: a PTY with ONLCR maps every '\n' to '\r\n'. When the source
// already uses CRLF, ONLCR doubles it to '\r\r\n', which must collapse to a
// single LF — not two (which would surface as a spurious blank line in cat).
func TestNormalizeOutput_OnlcrDoubledCRLF(t *testing.T) {
	raw := []byte("line1\r\r\nline2\r\r\n")
	got := normalizeOutput(raw)
	want := "line1\nline2\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_CRLFMixedWithLoneCR(t *testing.T) {
	// Plain CRLF, ONLCR-doubled CRLF, and a lone CR (progress redraw) combined.
	raw := []byte("a\r\nb\r\r\nc\r\rdone")
	got := normalizeOutput(raw)
	want := "a\nb\nc\n\ndone"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_Tab(t *testing.T) {
	raw := []byte("col1\tcol2\tcol3")
	got := normalizeOutput(raw)
	want := "col1\tcol2\tcol3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_Backspace(t *testing.T) {
	raw := []byte("abc\b\bxy")
	got := normalizeOutput(raw)
	want := "axy"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_ColourCodes(t *testing.T) {
	raw := []byte("before\x1b[32mgreen\x1b[0mafter")
	got := normalizeOutput(raw)
	want := "beforegreenafter"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_ControlChars(t *testing.T) {
	raw := []byte("a\x01b\x02c")
	got := normalizeOutput(raw)
	want := "abc"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_DELChar(t *testing.T) {
	raw := []byte("hello\x7Fworld")
	got := normalizeOutput(raw)
	want := "helloworld"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_Empty(t *testing.T) {
	got := normalizeOutput([]byte{})
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestNormalizeOutput_Nil(t *testing.T) {
	got := normalizeOutput(nil)
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestNormalizeOutput_LsSimulation(t *testing.T) {
	raw := []byte("\x1b[0m\x1b[01;34mdir1\x1b[0m  \x1b[01;32mfile.txt\x1b[0m\r\n")
	got := normalizeOutput(raw)
	want := "dir1  file.txt\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_UTF8(t *testing.T) {
	raw := []byte("=== 运行时间 ===\n 20:26:09 up 6 days")
	got := normalizeOutput(raw)
	want := "=== 运行时间 ===\n 20:26:09 up 6 days"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_UTF8WithANSI(t *testing.T) {
	raw := []byte("\x1b[01;34m目录\x1b[0m  \x1b[01;32m文件.txt\x1b[0m\r\n")
	got := normalizeOutput(raw)
	want := "目录  文件.txt\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ── removeBackspaceOverstrikes ───────────────────────────────────────────

func TestRemoveBackspaceOverstrikes(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"abc\b\bxy", "axy"},
		{"hello\b\b\b\b\bworld", "world"},
		{"no backspaces", "no backspaces"},
		{"\bstart", "start"},
		{"", ""},
	}
	for _, tc := range tests {
		got := removeBackspaceOverstrikes(tc.in)
		if got != tc.want {
			t.Errorf("removeBackspaceOverstrikes(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRemoveBackspaceOverstrikes_MultiByte(t *testing.T) {
	// Single backspace deletes full preceding rune, not partial byte.
	got := removeBackspaceOverstrikes("测试\btest")
	if got != "测test" {
		t.Errorf("got %q, want %q", got, "测test")
	}
}

func TestRemoveBackspaceOverstrikes_MultiByteManyBackspaces(t *testing.T) {
	got := removeBackspaceOverstrikes("测试\b\btest")
	if got != "test" {
		t.Errorf("got %q, want %q", got, "test")
	}
}

func TestRemoveBackspaceOverstrikes_ConsecutiveBackspaces(t *testing.T) {
	got := removeBackspaceOverstrikes("abc\b\b\b")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// ── CSI erase handling ───────────────────────────────────────────────────

func TestNormalizeOutput_CSI_EraseToEnd(t *testing.T) {
	// \x1b[K — erase from cursor to end. Removes only the escape sequence.
	raw := []byte("Downloading... 50%\x1b[K")
	got := normalizeOutput(raw)
	want := "Downloading... 50%"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_CSI_EraseToEndThenMore(t *testing.T) {
	raw := []byte("old text\x1b[Knew text")
	got := normalizeOutput(raw)
	want := "old textnew text"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_CSI_EraseEntireLine(t *testing.T) {
	// \x1b[2K — erase entire line. Everything before is discarded.
	raw := []byte("stale content\x1b[2Kfresh")
	got := normalizeOutput(raw)
	want := "fresh"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_CSI_EraseToStart(t *testing.T) {
	// \x1b[1K — erase from start to cursor.
	raw := []byte("prefix\x1b[1Ksuffix")
	got := normalizeOutput(raw)
	want := "suffix"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_CSI_MixedEraseAndColor(t *testing.T) {
	// Realistic: color + erase sequences interleaved.
	raw := []byte("\x1b[01;32m50%\x1b[0m\x1b[K\x1b[01;32m100%\x1b[0m")
	got := normalizeOutput(raw)
	want := "50%100%"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_CSI_MultiLineErase(t *testing.T) {
	raw := []byte("line1 old\x1b[2Kline1 new\nline2 old\x1b[2Kline2 new")
	got := normalizeOutput(raw)
	want := "line1 new\nline2 new"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_CSI_ProgressBar(t *testing.T) {
	// Simulate a progress bar: print, erase, print, erase, final.
	raw := []byte("[==>      ] 25%\x1b[2K[====>    ] 50%\x1b[2K[========] 100%")
	got := normalizeOutput(raw)
	want := "[========] 100%"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNormalizeOutput_CSI_ComplexEraseAcrossLines(t *testing.T) {
	raw := []byte("Header\nitem1\x1b[2Kupdated1\nitem2\x1b[K more\nFooter")
	got := normalizeOutput(raw)
	want := "Header\nupdated1\nitem2 more\nFooter"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ── Helper functions ─────────────────────────────────────────────────────

func TestBashQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello", "'hello'"},
		{"it's", "'it'\\''s'"},
		{"", "''"},
	}
	for _, tc := range cases {
		got := bashQuote(tc.in)
		if got != tc.want {
			t.Errorf("bashQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRandomToken(t *testing.T) {
	t1 := randomToken()
	t2 := randomToken()
	if len(t1) != 32 {
		t.Errorf("expected 32-char hex token, got %d", len(t1))
	}
	if t1 == t2 {
		t.Errorf("two random tokens should not be equal")
	}
}

// ── donePattern multi-line matching ──────────────────────────────────────

func TestDonePattern_MultiLineOutput(t *testing.T) {
	// Verify donePattern matches output spanning multiple lines with (?s).
	// The raw regex capture includes a trailing newline; cleanOutput strips it.
	shell := &PersistentShell{}
	token := "abc123def456abc123def456abc1"
	pattern := shell.donePattern(token)

	input := "__RBSH_START__:abc123def456abc123def456abc1__\n" +
		"Filesystem      Size  Used Avail Use% Mounted on\n" +
		"/dev/sda1       100G   50G   50G  50% /\n" +
		"tmpfs           3.1G   44K  3.1G   1% /run/user/1000\n" +
		"=== 运行时间 ===\n" +
		" 20:41:53 up 6 days, 23:40,  1 user,  load average: 0.56\n" +
		"__RBSH_DONE__:abc123def456abc123def456abc1:0:CWD:/home/noimank__\n" +
		"noimank@debian-server:~$ "

	m := pattern.FindSubmatch([]byte(input))
	if m == nil {
		t.Fatal("regex did not match multi-line output — missing (?s) flag?")
	}

	// Verify the full cleanOutput pipeline produces correct final output.
	rawOutput := m[1]
	exitCode := string(m[2])
	cwd := string(m[3])

	if exitCode != "0" {
		t.Errorf("exitCode = %q, want 0", exitCode)
	}
	if cwd != "/home/noimank" {
		t.Errorf("cwd = %q, want /home/noimank", cwd)
	}

	clean := shell.cleanOutput(rawOutput, "df -h", token)
	if clean != "Filesystem      Size  Used Avail Use% Mounted on\n"+
		"/dev/sda1       100G   50G   50G  50% /\n"+
		"tmpfs           3.1G   44K  3.1G   1% /run/user/1000\n"+
		"=== 运行时间 ===\n"+
		" 20:41:53 up 6 days, 23:40,  1 user,  load average: 0.56" {
		t.Errorf("cleanOutput mismatch:\ngot: %q", clean)
	}

	// Sentinel and prompt must not leak into final output.
	if strings.Contains(clean, "noimank@debian-server") {
		t.Error("prompt leaked into final output")
	}
	if strings.Contains(clean, "__RBSH_DONE__") {
		t.Error("done sentinel leaked into final output")
	}
}

func TestDonePattern_SingleLineOutput(t *testing.T) {
	shell := &PersistentShell{}
	token := "deadbeef12345678deadbeef12345678"
	pattern := shell.donePattern(token)

	input := "__RBSH_START__:deadbeef12345678deadbeef12345678__\n" +
		"total 42\n" +
		"__RBSH_DONE__:deadbeef12345678deadbeef12345678:0:CWD:/tmp__\n"

	m := pattern.FindSubmatch([]byte(input))
	if m == nil {
		t.Fatal("regex did not match single-line output")
	}

	clean := shell.cleanOutput(m[1], "ls", token)
	if clean != "total 42" {
		t.Errorf("cleanOutput = %q, want %q", clean, "total 42")
	}
}

func TestDonePattern_NegativeExitCode(t *testing.T) {
	shell := &PersistentShell{}
	token := "00000000000000000000000000000000"
	pattern := shell.donePattern(token)

	input := "__RBSH_START__:00000000000000000000000000000000__\n" +
		"command not found\n" +
		"__RBSH_DONE__:00000000000000000000000000000000:-127:CWD:/root__\n"

	m := pattern.FindSubmatch([]byte(input))
	if m == nil {
		t.Fatal("regex did not match negative exit code")
	}
	if string(m[2]) != "-127" {
		t.Errorf("exitCode = %q, want -127", string(m[2]))
	}
	if string(m[3]) != "/root" {
		t.Errorf("cwd = %q, want /root", string(m[3]))
	}

	clean := shell.cleanOutput(m[1], "nonexistent", token)
	if clean != "command not found" {
		t.Errorf("cleanOutput = %q, want %q", clean, "command not found")
	}
}

func TestDonePattern_EmptyOutput(t *testing.T) {
	shell := &PersistentShell{}
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pattern := shell.donePattern(token)

	input := "__RBSH_START__:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa__\n" +
		"__RBSH_DONE__:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:0:CWD:/home__\n"

	m := pattern.FindSubmatch([]byte(input))
	if m == nil {
		t.Fatal("regex did not match empty output")
	}

	clean := shell.cleanOutput(m[1], "true", token)
	if clean != "" {
		t.Errorf("cleanOutput = %q, want empty string", clean)
	}
}

func TestDonePattern_CRLF(t *testing.T) {
	shell := &PersistentShell{}
	token := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	pattern := shell.donePattern(token)

	input := "__RBSH_START__:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb__\r\n" +
		"hello world\r\n" +
		"__RBSH_DONE__:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:0:CWD:/tmp__\r\n"

	m := pattern.FindSubmatch([]byte(input))
	if m == nil {
		t.Fatal("regex did not match CRLF output")
	}

	clean := shell.cleanOutput(m[1], "echo hello", token)
	if clean != "hello world" {
		t.Errorf("cleanOutput = %q, want %q", clean, "hello world")
	}
}

// Regression for the "cat prints blank lines" bug: cat of a Windows (CRLF)
// text file through an ONLCR PTY delivers '\r\r\n' per line. cleanOutput must
// yield one line per source line, with no inserted blank lines.
func TestCleanOutput_CatCRLFFile(t *testing.T) {
	shell := &PersistentShell{}
	token := "cccccccccccccccccccccccccccccccc"
	raw := []byte("header row\r\r\n" +
		"data line 1\r\r\n" +
		"data line 2\r\r\n")
	clean := shell.cleanOutput(raw, "cat win.txt", token)
	want := "header row\ndata line 1\ndata line 2"
	if clean != want {
		t.Errorf("cleanOutput = %q, want %q", clean, want)
	}
}
