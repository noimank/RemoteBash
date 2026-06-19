package ssh

// SafeRmShim is the bash function that shadows rm(1) with a trash-to-/tmp
// safe delete. Copied from the Python implementation.
//
// Three-tier per-file strategy:
//  1. mv -- $_a $_t/        — fast, atomic, handles most cases
//  2. cp -r && command rm -r — cross-device safe
//  3. command rm $_o -- $_a  — last resort
//
// Use "command rm" or "/bin/rm" to bypass the shim.
const SafeRmShim = (
	"rm(){" +
		" _t=\"/tmp/.rbsh_trash/$(date +%s)_$$\"&&mkdir -p \"$_t\" 2>/dev/null;" +
		" _o=\"\";_n=0;_e=false;_f=0;" +
		" for _a;do" +
		"  $_e && {" +
		"   _n=1;mv -- \"$_a\" \"$_t/\" 2>/dev/null||" +
		"   { cp -r -- \"$_a\" \"$_t/\" 2>/dev/null&&command rm -r -- \"$_a\" 2>/dev/null;}||" +
		"   { command rm $_o -- \"$_a\" 2>/dev/null||_f=1;};" +
		"   continue;" +
		"  };" +
		"  case \"$_a\" in" +
		"   --) _e=true;_o=\"$_o --\";;" +
		"   -*) _o=\"$_o $_a\";;" +
		"   *) _n=1;mv -- \"$_a\" \"$_t/\" 2>/dev/null||" +
		"       { cp -r -- \"$_a\" \"$_t/\" 2>/dev/null&&command rm -r -- \"$_a\" 2>/dev/null;}||" +
		"       { command rm $_o -- \"$_a\" 2>/dev/null||_f=1;};" +
		"  esac;" +
		" done;" +
		" [ \"$_n\" -eq 0 ]&&command rm $_o 2>/dev/null;" +
		" return $_f;" +
		"}; ")

// TerminalPS1 is the human-readable PS1 for the browser terminal.
// Layout: user@host /path\n➜
const TerminalPS1 = (
	"\\[\\e[1;36m\\]\\u\\[\\e[0m\\]@\\[\\e[1;33m\\]\\h\\[\\e[0m\\]" +
	" \\[\\e[1;37m\\]\\w\\[\\e[0m\\]\\n\\[\\e[1;32m\\]➜\\[\\e[0m\\] ")
