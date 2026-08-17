package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/audit"
	"github.com/MalteKiefer/ledgerline-cli/internal/files"
)

// ndjsonLineWriter wraps an io.Writer, re-emitting every line written to it as
// a `{"type":"log","text":"..."}` NDJSON record. It lets `files sync --json`
// reuse internal/files.Sync/RunService exactly as they are (they only ever
// take an io.Writer for progress lines) while giving a non-interactive caller
// (e.g. a GUI front-end) a structured stream instead of free text to scrape.
type ndjsonLineWriter struct {
	enc *json.Encoder
	buf strings.Builder
}

func newNDJSONLineWriter(w io.Writer) *ndjsonLineWriter {
	return &ndjsonLineWriter{enc: json.NewEncoder(w)}
}

func (w *ndjsonLineWriter) Write(p []byte) (int, error) {
	n := len(p)
	sc := bufio.NewScanner(strings.NewReader(w.buf.String() + string(p)))
	w.buf.Reset()
	// bufio.Scanner drops a trailing partial line silently, so track whether p
	// ended in a newline to know if the last scanned token is complete.
	complete := len(p) > 0 && p[len(p)-1] == '\n'
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if !complete && len(lines) > 0 {
		w.buf.WriteString(lines[len(lines)-1])
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		if line == "" {
			continue
		}
		_ = w.enc.Encode(syncJSONEvent{Type: "log", Text: line})
	}
	return n, nil
}

// syncJSONEvent is one NDJSON line of `files sync --json` output: either a
// free-text progress line ({"type":"log",...}) forwarded from the existing
// human-oriented sync engine, or a structured summary computed here from the
// SyncResult a single pass already returns.
type syncJSONEvent struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Pushed    int    `json:"pushed,omitempty"`
	Pulled    int    `json:"pulled,omitempty"`
	Conflicts int    `json:"conflicts,omitempty"`
	Skipped   int    `json:"skipped,omitempty"`
	Failed    int    `json:"failed,omitempty"`
	Error     string `json:"error,omitempty"`
}

// newFilesSyncCommand runs a two-way sync between a local directory and the
// remote file tree, optionally as a continuous watch service.
func newFilesSyncCommand() *cobra.Command {
	var direction, conflict string
	var interval time.Duration
	var service, jsonFlag, hidden, keepVersions bool
	var remoteFolder int64
	var maxVersions int

	cmd := &cobra.Command{
		Use:   "sync <local-dir>",
		Short: "Two-way sync a local directory with the remote files",
		Long: "Reconcile a local directory against the remote file tree: new/changed\n" +
			"local files are uploaded, new/changed remote files are downloaded, and a\n" +
			"file changed on both sides is resolved by --conflict. Deletions are NOT\n" +
			"propagated (a missing file is never treated as a delete). With --service it\n" +
			"keeps running, re-syncing on local changes and on --interval.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				return fmt.Errorf("%q is not a directory", dir)
			}
			switch direction {
			case files.DirectionBoth, files.DirectionPush, files.DirectionPull:
			default:
				return fmt.Errorf("invalid --direction %q (both|push|pull)", direction)
			}
			switch conflict {
			case files.ConflictNewest, files.ConflictKeepBoth, files.ConflictSkip:
			default:
				return fmt.Errorf("invalid --conflict %q (newest|keep-both|skip)", conflict)
			}

			client, err := authedClient(cmd.Context())
			if err != nil {
				return err
			}
			opts := files.SyncOptions{
				Direction: direction, Conflict: conflict, IncludeHidden: hidden,
				KeepLocalVersions: keepVersions, MaxLocalVersions: maxVersions,
			}
			if cmd.Flags().Changed("remote-folder") {
				opts.RemoteFolder = &remoteFolder
			}
			out := cmd.OutOrStdout()

			// In --json mode every progress line internal/files writes is
			// re-emitted as {"type":"log",...} NDJSON (see ndjsonLineWriter);
			// internal/files.Sync/RunService are otherwise used unchanged.
			progressOut := out
			if jsonFlag {
				progressOut = newNDJSONLineWriter(out)
			}

			if service {
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				if !jsonFlag {
					fmt.Fprintf(out, "Watching %s (interval %s). Ctrl-C to stop.\n", dir, interval)
				}
				serr := files.RunService(ctx, client, dir, opts, interval, progressOut)
				auditLog().Log(audit.Event{Event: "files.sync.service", Outcome: outcome(serr)})
				if ctx.Err() != nil {
					if jsonFlag {
						_ = json.NewEncoder(out).Encode(syncJSONEvent{Type: "stopped"})
					} else {
						fmt.Fprintln(out, "Stopped.")
					}
					return nil
				}
				if jsonFlag && serr != nil {
					_ = json.NewEncoder(out).Encode(syncJSONEvent{Type: "error", Error: serr.Error()})
				}
				return serr
			}

			res, err := files.Sync(cmd.Context(), client, dir, opts, progressOut, !jsonFlag)
			if err != nil {
				if jsonFlag {
					_ = json.NewEncoder(out).Encode(syncJSONEvent{Type: "error", Error: err.Error()})
				}
				return err
			}
			auditLog().Log(audit.Event{
				Event: "files.sync", Outcome: audit.OutcomeOK,
				Count: res.Pushed + res.Pulled,
				Detail: fmt.Sprintf("%d pushed, %d pulled, %d conflicts, %d failed",
					res.Pushed, res.Pulled, res.Conflicts, res.Failed),
			})
			if jsonFlag {
				return json.NewEncoder(out).Encode(syncJSONEvent{
					Type: "summary", Pushed: res.Pushed, Pulled: res.Pulled,
					Conflicts: res.Conflicts, Skipped: res.Skipped, Failed: res.Failed,
				})
			}
			fmt.Fprintf(out, "\nDone: %d pushed, %d pulled, %d conflicts, %d failed.\n",
				res.Pushed, res.Pulled, res.Conflicts, res.Failed)
			return nil
		},
	}
	cmd.Flags().StringVar(&direction, "direction", "both", "sync direction: both, push, or pull")
	cmd.Flags().StringVar(&conflict, "conflict", "newest", "both-sides change policy: newest, keep-both, or skip")
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Minute, "re-sync interval in --service mode")
	cmd.Flags().BoolVar(&service, "service", false, "keep running and watch for changes")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "print machine-readable NDJSON instead of human progress text")
	cmd.Flags().BoolVar(&hidden, "hidden", false, "include hidden files/directories (dotfiles), normally skipped")
	cmd.Flags().Int64Var(&remoteFolder, "remote-folder", 0,
		"scope the sync to this remote folder id instead of the whole remote root "+
			"(lets several sync pairs each mirror a different remote folder)")
	cmd.Flags().BoolVar(&keepVersions, "keep-versions", false,
		"before a pull overwrites a local file, snapshot it into .ledgerline-versions/ first "+
			"(a local safety net, separate from the server's own file version history)")
	cmd.Flags().IntVar(&maxVersions, "max-versions", 5, "snapshots to keep per file with --keep-versions")
	return cmd
}

// outcome maps an error to an audit outcome string.
func outcome(err error) string {
	if err != nil {
		return audit.OutcomeError
	}
	return audit.OutcomeOK
}
