package tfcli

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// isTerminalFile reports whether f is connected to a terminal.
func isTerminalFile(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// readPassword prompts on stderr and reads a password from stdin without
// echoing it. The caller must have already checked that stdin is an *os.File
// connected to a terminal (isTerminalReader); this only asserts the type.
func readPassword(stdin io.Reader, stderr io.Writer) (string, error) {
	f, ok := stdin.(*os.File)
	if !ok {
		return "", fmt.Errorf("stdin is not a terminal")
	}
	fmt.Fprint(stderr, "Password: ")
	b, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
