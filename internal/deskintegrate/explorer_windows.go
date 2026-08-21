//go:build windows

package deskintegrate

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// The shell context menu.
//
// Windows offers two ways to add one: a COM handler (IExplorerCommand, or the
// packaged IExplorerCommand a Windows 11 app uses to appear in the short menu)
// or registry verbs. Verbs are used here because they need no COM server, no
// in-process DLL loaded into Explorer, and no elevation — and because a crash
// in a shell extension takes Explorer with it, while a verb only ever launches
// a separate process. The cost is that our entries live in the "Show more
// options" menu on Windows 11 rather than the short one.
const (
	// classesKey is the per-user class registration root. HKCU\Software\Classes
	// is merged over HKLM for this user, so this needs no administrator.
	classesKey = `Software\Classes`

	// menuName is the key name under each class's shell key.
	menuName = "Ledgerline"

	// fileCommands and dirCommands hold the sub-menu entries. They are separate
	// stores because the two menus offer different verbs: you do not encrypt a
	// folder the way you encrypt a file, and only a folder can be synced.
	fileCommands = "Ledgerline.FileCommands"
	dirCommands  = "Ledgerline.DirCommands"
)

// Verb is one entry in the context menu.
type Verb struct {
	// Order fixes the position; the shell sorts sub-commands by key name.
	Order int
	// Name is the verb passed to the tray as --context <name>.
	Name string
	// Label is what the user reads, in their language.
	Label string
	// Separator draws a divider above this entry.
	Separator bool
}

// Menu is the labelled set of verbs for one target type.
type Menu struct {
	// Title is the sub-menu's own label.
	Title string
	Verbs []Verb
}

// RegisterExplorerMenu writes the file and folder menus for the current user,
// replacing whatever was there. Registering again after a language change is
// the supported way to relabel: the shell reads these strings, so they are
// written in the user's language rather than resolved at display time.
func RegisterExplorerMenu(files, dirs Menu) error {
	exe, err := trayExecutable()
	if err != nil {
		return err
	}

	// "*" is every file; Directory is a folder in the listing, and
	// Directory\Background is the empty space inside an open folder — the place
	// people right-click to act on the folder they are already in.
	if err := writeMenu(`*`, fileCommands, files, exe, "%1"); err != nil {
		return err
	}
	if err := writeMenu(`Directory`, dirCommands, dirs, exe, "%1"); err != nil {
		return err
	}
	return writeMenu(`Directory\Background`, dirCommands, dirs, exe, "%V")
}

// UnregisterExplorerMenu removes every key this package wrote. It ignores
// "not found": the point is to end up with the menu absent, and a partially
// registered state must still clean up completely.
func UnregisterExplorerMenu() error {
	var firstErr error
	note := func(err error) {
		if err != nil && !isNotFound(err) && firstErr == nil {
			firstErr = err
		}
	}
	for _, class := range []string{`*`, `Directory`, `Directory\Background`} {
		note(deleteTree(classesKey + `\` + class + `\shell\` + menuName))
	}
	for _, store := range []string{fileCommands, dirCommands} {
		note(deleteTree(classesKey + `\` + store))
	}
	return firstErr
}

// ExplorerMenuRegistered reports whether the file menu is present.
func ExplorerMenuRegistered() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		classesKey+`\*\shell\`+menuName, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	return true
}

// writeMenu registers one class's sub-menu and the command store it points at.
//
// argument is the shell placeholder for the target: %1 for the item that was
// clicked, %V for the folder whose background was clicked.
func writeMenu(class, store string, menu Menu, exe, argument string) error {
	root := classesKey + `\` + class + `\shell\` + menuName
	k, _, err := registry.CreateKey(registry.CURRENT_USER, root, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("create %s: %w", root, err)
	}
	defer k.Close()

	if err := k.SetStringValue("MUIVerb", menu.Title); err != nil {
		return err
	}
	// The program's own icon, so the entry is recognisable rather than blank.
	if err := k.SetStringValue("Icon", exe+",0"); err != nil {
		return err
	}
	// ExtendedSubCommandsKey is what makes this a sub-menu whose entries live
	// in their own store, which is the only form that supports separators and
	// per-entry icons without a COM handler.
	if err := k.SetStringValue("ExtendedSubCommandsKey", store); err != nil {
		return err
	}

	// Rewrite the store from scratch: leaving a verb behind after it was
	// renamed or dropped would show the user a command that no longer exists.
	if err := deleteTree(classesKey + `\` + store); err != nil && !isNotFound(err) {
		return err
	}
	for _, v := range menu.Verbs {
		if err := writeVerb(store, v, exe, argument); err != nil {
			return err
		}
	}
	return nil
}

// writeVerb registers one command. The key name carries the order because the
// shell sorts sub-commands alphabetically by key, not by insertion.
func writeVerb(store string, v Verb, exe, argument string) error {
	name := fmt.Sprintf("%02d%s", v.Order, v.Name)
	path := classesKey + `\` + store + `\shell\` + name

	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer k.Close()

	if err := k.SetStringValue("MUIVerb", v.Label); err != nil {
		return err
	}
	if v.Separator {
		if err := k.SetStringValue("CommandFlags", "0x20"); err != nil { // ECF_SEPARATORBEFORE
			return err
		}
	}

	cmd, _, err := registry.CreateKey(registry.CURRENT_USER, path+`\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer cmd.Close()

	// The tray executable, not the CLI: it is linked as a GUI program, so the
	// verb runs without a console window flashing over the desktop. Both the
	// path and the argument are quoted — a file called "my report.pdf" is the
	// common case, not the exotic one.
	line := fmt.Sprintf(`"%s" --context %s "%s"`, exe, v.Name, argument)
	return cmd.SetStringValue("", line)
}

// deleteTree removes a key and everything under it. The registry API deletes
// only empty keys, so the children go first.
func deleteTree(path string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.READ|registry.WRITE)
	if err != nil {
		return err
	}
	names, err := k.ReadSubKeyNames(-1)
	k.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := deleteTree(path + `\` + name); err != nil && !isNotFound(err) {
			return err
		}
	}
	return registry.DeleteKey(registry.CURRENT_USER, path)
}

// isNotFound reports the registry's "no such key or value", which every removal
// path treats as success.
//
// Matched on the error code, not the message. Windows returns its errors in the
// user's language, so comparing text worked on an English install and silently
// failed on every other one: on a German system the first registration aborted
// because deleting a store that was not there yet did not read as "not there".
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, registry.ErrNotExist) || errors.Is(err, fs.ErrNotExist) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.ERROR_FILE_NOT_FOUND || errno == syscall.ERROR_PATH_NOT_FOUND
	}
	return false
}
