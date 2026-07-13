package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MalteKiefer/ledgerline-cli/internal/api"
	"github.com/MalteKiefer/ledgerline-cli/internal/todo"
)

// newTodoCommand builds the `todo` group covering all todo actions.
func newTodoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "todo",
		Short: "Manage encrypted todos and lists",
	}
	cmd.AddCommand(
		newTodoLsCommand(),
		newTodoAddCommand(),
		newTodoDoneCommand("done", true),
		newTodoDoneCommand("undone", false),
		newTodoMarkCommand("mark", true),
		newTodoMarkCommand("unmark", false),
		newTodoEditCommand(),
		newTodoRmCommand(),
		newTodoRestoreCommand(),
		newTodoListsCommand(),
	)
	return cmd
}

// todoSession authenticates, unlocks the vault and loads the todo store.
func todoSession(cmd *cobra.Command) (*api.Client, *todo.Store, error) {
	ctx := cmd.Context()
	client, err := authedClient(ctx)
	if err != nil {
		return nil, nil, err
	}
	vk, err := unlockVault(cmd, client)
	if err != nil {
		return nil, nil, err
	}
	store := todo.NewStore(client, vk)
	if err := store.Load(ctx); err != nil {
		return nil, nil, err
	}
	return client, store, nil
}

// saveTodos persists staged changes (interrupt-safe).
func saveTodos(cmd *cobra.Command, store *todo.Store) error {
	if !store.Dirty() {
		return nil
	}
	return store.Save(context.WithoutCancel(cmd.Context()))
}

// --- ls ---

func newTodoLsCommand() *cobra.Command {
	var list, tag string
	var all, done, marked, trash bool

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List todos",
		Long: "List todos. By default only open (not done, not trashed) items show.\n\n" +
			"  ledgerline-cli todo ls --list Work --tag urgent\n" +
			"  ledgerline-cli todo ls --all         # include done\n" +
			"  ledgerline-cli todo ls --trash       # the trash",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, store, err := todoSession(cmd)
			if err != nil {
				return err
			}
			return listTodos(cmd.OutOrStdout(), store, listFilter{list: list, tag: tag, all: all, done: done, marked: marked, trash: trash})
		},
	}
	f := cmd.Flags()
	f.StringVar(&list, "list", "", "only todos in this list")
	f.StringVar(&tag, "tag", "", "only todos with this tag")
	f.BoolVar(&all, "all", false, "include completed todos")
	f.BoolVar(&done, "done", false, "only completed todos")
	f.BoolVar(&marked, "marked", false, "only starred todos")
	f.BoolVar(&trash, "trash", false, "show the trash")
	return cmd
}

type listFilter struct {
	list, tag                string
	all, done, marked, trash bool
}

func listTodos(w io.Writer, store *todo.Store, f listFilter) error {
	names := listNames(store)
	var listID string
	if f.list != "" {
		id, ok := store.ResolveListByName(f.list)
		if !ok {
			return fmt.Errorf("no such list: %s", f.list)
		}
		listID = id
	}

	items := store.TodoViews()
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Done != items[j].Done {
			return !items[i].Done
		}
		if items[i].Marked != items[j].Marked {
			return items[i].Marked
		}
		return prioRank(items[i].Priority) < prioRank(items[j].Priority)
	})

	var shown int
	for _, t := range items {
		switch {
		case f.trash && !t.Trashed:
			continue
		case !f.trash && t.Trashed:
			continue
		case f.done && !t.Done:
			continue
		case !f.all && !f.done && t.Done:
			continue
		case f.marked && !t.Marked:
			continue
		case f.tag != "" && !hasTag(t.Tags, f.tag):
			continue
		case listID != "" && (t.ListID == nil || *t.ListID != listID):
			continue
		}
		fmt.Fprintln(w, formatTodo(t, names))
		shown++
	}
	if shown == 0 {
		fmt.Fprintln(w, "(no todos)")
	}
	return nil
}

// --- add ---

func newTodoAddCommand() *cobra.Command {
	var desc, url, due, priority, list, tags string
	var mark bool

	cmd := &cobra.Command{
		Use:   "add <title...>",
		Short: "Add a todo",
		Long: "Add a todo. The title is taken from the arguments.\n\n" +
			"  ledgerline-cli todo add Buy milk --due 2026-07-20 --priority high --list Home",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, store, err := todoSession(cmd)
			if err != nil {
				return err
			}
			prio, err := normalizePriority(priority)
			if err != nil {
				return err
			}
			listID, err := resolveOptionalList(store, list)
			if err != nil {
				return err
			}
			if _, err := store.Add(todo.NewTodo{
				Title: strings.Join(args, " "), Description: desc, URL: url, Tags: splitTags(tags),
				Priority: prio, Marked: mark, Due: due, ListID: listID,
			}); err != nil {
				return err
			}
			if err := saveTodos(cmd, store); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Added.")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&desc, "desc", "", "description")
	f.StringVar(&url, "url", "", "an http(s) link")
	f.StringVar(&due, "due", "", "due date (e.g. 2026-07-20)")
	f.StringVar(&priority, "priority", "normal", "priority: high | normal | low")
	f.StringVar(&list, "list", "", "add to this list (created if new)")
	f.StringVar(&tags, "tags", "", "comma-separated tags")
	f.BoolVar(&mark, "mark", false, "star the todo")
	return cmd
}

// --- done / undone, mark / unmark ---

func newTodoDoneCommand(use string, done bool) *cobra.Command {
	short := "Mark a todo done"
	if !done {
		short = "Mark a todo not done"
	}
	return &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateTodo(cmd, args[0], map[string]any{"done": done})
		},
	}
}

func newTodoMarkCommand(use string, mark bool) *cobra.Command {
	short := "Star a todo"
	if !mark {
		short = "Unstar a todo"
	}
	return &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateTodo(cmd, args[0], map[string]any{"marked": mark})
		},
	}
}

// --- edit ---

func newTodoEditCommand() *cobra.Command {
	var title, desc, url, due, priority, list, tags string
	var mark, unmark bool

	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit a todo's fields",
		Long:  "Edit a todo. Only the flags you pass are changed.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, store, err := todoSession(cmd)
			if err != nil {
				return err
			}
			id, err := store.ResolveTodo(args[0])
			if err != nil {
				return err
			}
			patch := map[string]any{}
			fl := cmd.Flags()
			if fl.Changed("title") {
				patch["title"] = title
			}
			if fl.Changed("desc") {
				patch["description"] = desc
			}
			if fl.Changed("url") {
				patch["url"] = url
			}
			if fl.Changed("due") {
				patch["due"] = due
			}
			if fl.Changed("priority") {
				p, perr := normalizePriority(priority)
				if perr != nil {
					return perr
				}
				patch["priority"] = p
			}
			if fl.Changed("tags") {
				patch["tags"] = splitTags(tags)
			}
			if fl.Changed("list") {
				listID, lerr := resolveOptionalList(store, list)
				if lerr != nil {
					return lerr
				}
				patch["listId"] = listID
			}
			if mark {
				patch["marked"] = true
			}
			if unmark {
				patch["marked"] = false
			}
			if len(patch) == 0 {
				return errors.New("nothing to change (pass a field flag)")
			}
			store.Update(id, patch)
			if err := saveTodos(cmd, store); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Updated.")
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "new title")
	f.StringVar(&desc, "desc", "", "new description")
	f.StringVar(&url, "url", "", "new http(s) link")
	f.StringVar(&due, "due", "", "new due date (empty clears)")
	f.StringVar(&priority, "priority", "normal", "priority: high | normal | low")
	f.StringVar(&list, "list", "", "move to this list (empty clears)")
	f.StringVar(&tags, "tags", "", "replace tags (comma-separated)")
	f.BoolVar(&mark, "mark", false, "star it")
	f.BoolVar(&unmark, "unmark", false, "unstar it")
	return cmd
}

// --- rm / restore ---

func newTodoRmCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rm <id>",
		Short: "Move a todo to the trash (or delete it permanently with --force)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, store, err := todoSession(cmd)
			if err != nil {
				return err
			}
			id, err := store.ResolveTodo(args[0])
			if err != nil {
				return err
			}
			if force {
				store.Delete(id)
			} else {
				store.Update(id, map[string]any{"trashed": true})
			}
			if err := saveTodos(cmd, store); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), map[bool]string{true: "Deleted.", false: "Moved to trash."}[force])
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "delete permanently instead of trashing")
	return cmd
}

func newTodoRestoreCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <id>",
		Short: "Restore a todo from the trash",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mutateTodo(cmd, args[0], map[string]any{"trashed": false})
		},
	}
}

// --- lists ---

func newTodoListsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lists",
		Short: "Manage todo lists",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, store, err := todoSession(cmd)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			lists := store.ListViews()
			if len(lists) == 0 {
				fmt.Fprintln(w, "(no lists)")
				return nil
			}
			counts := map[string]int{}
			for _, t := range store.TodoViews() {
				if t.ListID != nil && !t.Trashed {
					counts[*t.ListID]++
				}
			}
			sort.Slice(lists, func(i, j int) bool { return strings.ToLower(lists[i].Name) < strings.ToLower(lists[j].Name) })
			for _, l := range lists {
				fmt.Fprintf(w, "%-30s %d open\n", l.Name, counts[l.ID])
			}
			return nil
		},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "add <name>",
			Short: "Create a list",
			Args:  cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				_, store, err := todoSession(cmd)
				if err != nil {
					return err
				}
				if _, err := store.AddList(strings.Join(args, " ")); err != nil {
					return err
				}
				return finishTodo(cmd, store, "List created.")
			},
		},
		&cobra.Command{
			Use:   "rm <name>",
			Short: "Delete a list (its todos move to no list)",
			Args:  cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				_, store, err := todoSession(cmd)
				if err != nil {
					return err
				}
				id, ok := store.ResolveListByName(strings.Join(args, " "))
				if !ok {
					return errors.New("no such list")
				}
				store.DeleteList(id)
				return finishTodo(cmd, store, "List deleted.")
			},
		},
		&cobra.Command{
			Use:   "rename <old> <new>",
			Short: "Rename a list",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				_, store, err := todoSession(cmd)
				if err != nil {
					return err
				}
				id, ok := store.ResolveListByName(args[0])
				if !ok {
					return errors.New("no such list")
				}
				store.RenameList(id, args[1])
				return finishTodo(cmd, store, "List renamed.")
			},
		},
	)
	return cmd
}

// --- shared helpers ---

// mutateTodo resolves an id and applies a single patch, then saves.
func mutateTodo(cmd *cobra.Command, ref string, patch map[string]any) error {
	_, store, err := todoSession(cmd)
	if err != nil {
		return err
	}
	id, err := store.ResolveTodo(ref)
	if err != nil {
		return err
	}
	store.Update(id, patch)
	return finishTodo(cmd, store, "Done.")
}

// finishTodo saves and prints a confirmation.
func finishTodo(cmd *cobra.Command, store *todo.Store, msg string) error {
	if err := saveTodos(cmd, store); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), msg)
	return nil
}

func listNames(store *todo.Store) map[string]string {
	m := map[string]string{}
	for _, l := range store.ListViews() {
		m[l.ID] = l.Name
	}
	return m
}

// formatTodo renders one todo line: short id, checkbox, priority, star, title,
// then due/list/tags.
func formatTodo(t todo.TodoView, names map[string]string) string {
	box := "[ ]"
	if t.Done {
		box = "[x]"
	}
	prio := " "
	switch t.Priority {
	case todo.PriorityHigh:
		prio = "↑"
	case todo.PriorityLow:
		prio = "↓"
	}
	star := " "
	if t.Marked {
		star = "★"
	}
	line := fmt.Sprintf("%s  %s %s%s %s", shortID(t.ID), box, prio, star, t.Title)

	var extra []string
	if t.Due != "" {
		extra = append(extra, "due:"+t.Due)
	}
	if t.ListID != nil {
		if n, ok := names[*t.ListID]; ok {
			extra = append(extra, "list:"+n)
		}
	}
	for _, g := range t.Tags {
		extra = append(extra, "#"+g)
	}
	if len(extra) > 0 {
		line += "   " + strings.Join(extra, " ")
	}
	return line
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func prioRank(p string) int {
	switch p {
	case todo.PriorityHigh:
		return 0
	case todo.PriorityLow:
		return 2
	default:
		return 1
	}
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, want) {
			return true
		}
	}
	return false
}

func splitTags(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func normalizePriority(p string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "normal":
		return todo.PriorityNormal, nil
	case "high", "h":
		return todo.PriorityHigh, nil
	case "low", "l":
		return todo.PriorityLow, nil
	default:
		return "", fmt.Errorf("invalid priority %q (use high, normal or low)", p)
	}
}

// resolveOptionalList maps a list name to an id, creating the list if it is new.
// An empty name clears the list (nil).
func resolveOptionalList(store *todo.Store, name string) (*string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}
	if id, ok := store.ResolveListByName(name); ok {
		return &id, nil
	}
	id, err := store.AddList(name)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
