package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// readSecretStdin reads one secret (share password, key passphrase) from stdin.
// The host is treated as hostile to secrets in argv (§3 of CLAUDE.md: argv,
// environment and shell history are all readable by other users on a shared
// box), so every high-value secret has a stdin path and the private-key
// passphrase has ONLY that path.
func readSecretStdin(cmd *cobra.Command) (string, error) {
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err != nil && line == "" {
		return "", errors.New("no secret on stdin")
	}
	if line == "" {
		return "", errors.New("empty secret on stdin")
	}
	return line, nil
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
