package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"unicode"

	"golang.org/x/sys/unix"
	"golang.org/x/text/width"
)

var errConsoleInterrupted = errors.New("console interrupted")

func readConsoleInputLine(reader *bufio.Reader, stdin, stdout *os.File) (string, error) {
	if stdin == nil || stdout == nil || !isTerminalFile(stdin) || !isTerminalFile(stdout) {
		return reader.ReadString('\n')
	}
	return readConsoleLineRaw(stdin, stdout)
}

func readConsoleLineRaw(stdin *os.File, stdout io.Writer) (string, error) {
	oldState, err := makeRawTerminal(int(stdin.Fd()))
	if err != nil {
		return "", err
	}
	defer func() {
		_ = restoreTerminal(int(stdin.Fd()), oldState)
	}()

	reader := bufio.NewReader(stdin)
	editor := newConsoleLineEditor()
	for {
		r, _, err := reader.ReadRune()
		if err != nil {
			return "", err
		}

		if r == 0x1b {
			discardEscapeSequence(reader)
			continue
		}

		rendered, done, interrupted := editor.Apply(r)
		if rendered != "" {
			if _, writeErr := io.WriteString(stdout, rendered); writeErr != nil {
				return "", writeErr
			}
		}
		if interrupted {
			return "", errConsoleInterrupted
		}
		if done {
			return editor.String(), nil
		}
	}
}

func discardEscapeSequence(reader *bufio.Reader) {
	for reader.Buffered() > 0 {
		r, _, err := reader.ReadRune()
		if err != nil {
			return
		}
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' {
			return
		}
	}
}

type consoleLineEditor struct {
	buffer []rune
}

func newConsoleLineEditor() *consoleLineEditor {
	return &consoleLineEditor{}
}

func (e *consoleLineEditor) Apply(r rune) (rendered string, done bool, interrupted bool) {
	switch r {
	case '\r', '\n':
		return "\n", true, false
	case 3:
		return "^C\n", false, true
	case 8, 127:
		if len(e.buffer) == 0 {
			return "", false, false
		}
		last := e.buffer[len(e.buffer)-1]
		e.buffer = e.buffer[:len(e.buffer)-1]
		return eraseSequenceForWidth(runeCellWidth(last)), false, false
	default:
		if unicode.IsControl(r) {
			return "", false, false
		}
		e.buffer = append(e.buffer, r)
		return string(r), false, false
	}
}

func (e *consoleLineEditor) String() string {
	return strings.TrimSpace(string(e.buffer))
}

func eraseSequenceForWidth(cellWidth int) string {
	if cellWidth <= 0 {
		return ""
	}
	back := strings.Repeat("\b", cellWidth)
	blank := strings.Repeat(" ", cellWidth)
	return back + blank + back
}

func runeCellWidth(r rune) int {
	if r == 0 || unicode.IsControl(r) {
		return 0
	}
	if unicode.Is(unicode.Mn, r) {
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	default:
		return 1
	}
}

func isTerminalFile(file *os.File) bool {
	if file == nil {
		return false
	}
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TIOCGETA)
	return err == nil
}

func makeRawTerminal(fd int) (*unix.Termios, error) {
	state, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, err
	}

	raw := *state
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, &raw); err != nil {
		return nil, err
	}
	return state, nil
}

func restoreTerminal(fd int, state *unix.Termios) error {
	if state == nil {
		return nil
	}
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, state)
}
