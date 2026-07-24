package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/MalteKiefer/ledgerline-cli/internal/files"
)

// newFilesLsCommand lists a remote folder's contents.
func newFilesLsCommand() *cobra.Command {
	var recursive bool
	var color, icons string

	cmd := &cobra.Command{
		Use:   "ls [path]",
		Short: "List folders and files in the encrypted store",
		Long: "List the contents of a remote folder (subfolders and files), colour-\n" +
			"coded with a monochrome per-type icon.\n\n" +
			"  ledgerline-cli files ls              # the root\n" +
			"  ledgerline-cli files ls Photos/2024  # a subfolder\n" +
			"  ledgerline-cli files ls -R Photos    # recurse into subfolders\n\n" +
			"Icons use Nerd Font glyphs (--icons nerd); pass --icons none if your\n" +
			"terminal font lacks them.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			return runFilesLs(cmd, path, recursive, colorEnabled(color), icons != "none")
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "R", false, "list subfolders recursively")
	cmd.Flags().StringVar(&color, "color", "auto", "colourise output: auto | always | never")
	cmd.Flags().StringVar(&icons, "icons", "nerd", "type icons: nerd | none")
	return cmd
}

// runFilesLs loads the manifest and prints a folder's contents.
func runFilesLs(cmd *cobra.Command, path string, recursive, color, icons bool) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	client, err := authedClient(ctx)
	if err != nil {
		return err
	}
	vk, err := unlockVault(cmd, client)
	if err != nil {
		return err
	}

	store := files.NewStore(client, vk)
	store.SetShardCache(shardCache("files-shards"))
	if err := store.Load(ctx); err != nil {
		return err
	}
	warnIfDegraded(w, "files", store)

	if recursive {
		entries := files.List(store, path)
		if len(entries) == 0 {
			fmt.Fprintln(w, "(empty)")
			return nil
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		for _, e := range entries {
			icon, col := styleForFile(e.Path)
			fmt.Fprintf(w, "%10s  %s%s\n", humanBytes(e.View.Size), glyph(icons, icon), paint(color, col, e.Path))
		}
		return nil
	}

	folders, filesList, err := files.Children(store, path)
	if err != nil {
		return err
	}
	if len(folders) == 0 && len(filesList) == 0 {
		fmt.Fprintln(w, "(empty)")
		return nil
	}
	for _, f := range folders {
		fmt.Fprintf(w, "%10s  %s%s/\n", "-", glyph(icons, folderIcon), paint(color, colorFolder, f.Name))
	}
	for _, f := range filesList {
		icon, col := styleForFile(f.Name)
		fmt.Fprintf(w, "%10s  %s%s\n", humanBytes(f.Size), glyph(icons, icon), paint(color, col, f.Name))
	}
	return nil
}

// glyph renders an icon plus a trailing space, or nothing when icons are off.
func glyph(enabled bool, icon string) string {
	if !enabled {
		return ""
	}
	return icon + "  "
}

// ANSI colour codes used for the listing.
const (
	colorFolder  = "1;34" // bold blue
	colorImage   = "35"   // magenta
	colorVideo   = "36"   // cyan
	colorAudio   = "32"   // green
	colorArchive = "31"   // red
	colorDoc     = "33"   // yellow
	colorCode    = "92"   // bright green
	colorReset   = "0"
)

// folderIcon is the monochrome Nerd Font glyph shown for directories (nf-fa-folder).
const folderIcon = "\uf07b"

// colorEnabled resolves the --color mode against the terminal and NO_COLOR.
func colorEnabled(mode string) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	default:
		if os.Getenv("NO_COLOR") != "" {
			return false
		}
		return term.IsTerminal(int(os.Stdout.Fd()))
	}
}

// paint wraps s in an ANSI colour when enabled.
func paint(enabled bool, code, s string) string {
	if !enabled || code == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[" + colorReset + "m"
}

// styleForFile returns an icon and colour code for a filename by extension.
func styleForFile(name string) (icon, color string) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".heic", ".heif", ".avif", ".svg":
		return "\uf1c5", colorImage
	case ".mov", ".mp4", ".m4v", ".avi", ".mkv", ".webm", ".3gp", ".mpg", ".mpeg", ".wmv", ".flv":
		return "\uf1c8", colorVideo
	case ".mp3", ".wav", ".flac", ".aac", ".ogg", ".m4a", ".opus":
		return "\uf1c7", colorAudio
	case ".pdf":
		return "\uf1c1", colorDoc
	case ".doc", ".docx", ".odt", ".rtf", ".txt", ".md":
		return "\uf1c2", colorDoc
	case ".xls", ".xlsx", ".ods", ".csv":
		return "\uf1c3", colorDoc
	case ".ppt", ".pptx", ".odp":
		return "\uf1c4", colorDoc
	case ".zip", ".tar", ".gz", ".bz2", ".xz", ".7z", ".rar", ".zst":
		return "\uf1c6", colorArchive
	case ".go", ".js", ".ts", ".py", ".rb", ".php", ".c", ".h", ".cpp", ".rs", ".java", ".sh", ".json", ".yaml", ".yml", ".toml", ".html", ".css":
		return "\uf1c9", colorCode
	case ".app", ".exe", ".dmg", ".deb", ".rpm", ".pkg":
		return "\uf013", colorCode
	default:
		return "\uf15b", ""
	}
}
