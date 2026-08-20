package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

// readSecretStdin reads one secret (share password, key passphrase) from stdin.
// The host is treated as hostile to secrets in argv (§3 of CLAUDE.md: argv,
// environment and shell history are all readable by other users on a shared
// box), so every high-value secret has a stdin path and the private-key
// passphrase has ONLY that path.
func readSecretStdin(cmd *cobra.Command) (string, error) {
	// Read a byte at a time rather than through a buffered reader: stdin may
	// carry more than this one secret (a piped password followed by a prompted
	// two-factor code), and a buffer would swallow the rest of it.
	line, err := readLine(cmd.InOrStdin())
	if err != nil && line == "" {
		return "", errors.New("no secret on stdin")
	}
	if line == "" {
		return "", errors.New("empty secret on stdin")
	}
	return line, nil
}

// readLine consumes exactly one line from r, without reading ahead.
func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return strings.TrimRight(b.String(), "\r"), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			return strings.TrimRight(b.String(), "\r"), err
		}
	}
}

// secretFlag resolves a secret that can arrive either as --<name> (convenient,
// but visible in argv) or --<name>-stdin (piped). It returns nil when neither
// was passed, so callers can leave the field out of the request body.
func secretFlag(cmd *cobra.Command, name string, value string, fromStdin bool) (*string, error) {
	viaFlag := cmd.Flags().Changed(name)
	if viaFlag && fromStdin {
		return nil, fmt.Errorf("--%s and --%s-stdin are mutually exclusive", name, name)
	}
	if fromStdin {
		secret, err := readSecretStdin(cmd)
		if err != nil {
			return nil, err
		}
		return &secret, nil
	}
	if viaFlag {
		return &value, nil
	}
	return nil, nil
}
