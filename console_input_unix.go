//go:build darwin || linux

package main

import (
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

type consoleLineReader struct {
	in     *os.File
	out    *os.File
	fd     int
	state  *unix.Termios
	active bool
}

func newConsoleLineReader(in *os.File, out *os.File) (*consoleLineReader, error) {
	if in == nil || out == nil {
		return nil, fmt.Errorf("console input/output is required")
	}
	fd := int(in.Fd())
	state, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return nil, fmt.Errorf("get terminal state: %w", err)
	}
	raw := *state
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, &raw); err != nil {
		return nil, fmt.Errorf("set terminal raw mode: %w", err)
	}
	return &consoleLineReader{
		in:     in,
		out:    out,
		fd:     fd,
		state:  state,
		active: true,
	}, nil
}

func (r *consoleLineReader) Close() error {
	if r == nil || !r.active {
		return nil
	}
	r.active = false
	return unix.IoctlSetTermios(r.fd, unix.TIOCSETA, r.state)
}

func (r *consoleLineReader) ReadLine(prompt string) (string, error) {
	if _, err := fmt.Fprint(r.out, prompt); err != nil {
		return "", err
	}

	var runes []rune
	var utf8buf []byte
	var esc []byte
	scratch := make([]byte, 1)

	for {
		if _, err := r.in.Read(scratch); err != nil {
			return "", err
		}
		b := scratch[0]

		if len(esc) > 0 || b == 0x1b {
			esc = append(esc, b)
			if len(esc) >= 3 || (len(esc) == 2 && esc[1] != '[') {
				esc = esc[:0]
			}
			continue
		}

		switch b {
		case '\r', '\n':
			if _, err := fmt.Fprint(r.out, "\r\n"); err != nil {
				return "", err
			}
			return string(runes), nil
		case 0x03:
			if _, err := fmt.Fprint(r.out, "^C\r\n"); err != nil {
				return "", err
			}
			return "", errConsoleInterrupted
		case 0x04:
			if len(runes) == 0 {
				return "", io.EOF
			}
			continue
		case 0x7f, 0x08:
			if len(utf8buf) > 0 {
				utf8buf = utf8buf[:0]
				continue
			}
			if len(runes) > 0 {
				runes = runes[:len(runes)-1]
				if err := redrawConsolePrompt(r.out, prompt, string(runes)); err != nil {
					return "", err
				}
			}
			continue
		}

		if b < 0x20 {
			continue
		}
		if b < utf8.RuneSelf {
			runes = append(runes, rune(b))
			if err := redrawConsolePrompt(r.out, prompt, string(runes)); err != nil {
				return "", err
			}
			continue
		}

		utf8buf = append(utf8buf, b)
		if !utf8.FullRune(utf8buf) {
			continue
		}
		ru, size := utf8.DecodeRune(utf8buf)
		if ru != utf8.RuneError || size > 1 {
			runes = append(runes, ru)
			if err := redrawConsolePrompt(r.out, prompt, string(runes)); err != nil {
				return "", err
			}
		}
		utf8buf = utf8buf[:0]
	}
}

func redrawConsolePrompt(out io.Writer, prompt, line string) error {
	_, err := fmt.Fprintf(out, "\r\033[2K%s%s", prompt, line)
	return err
}
