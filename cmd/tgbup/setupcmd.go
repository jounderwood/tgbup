package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram"

	"gitlab.com/joeunderwood/tgbup/internal/config"
	"gitlab.com/joeunderwood/tgbup/internal/selector"
	"gitlab.com/joeunderwood/tgbup/internal/tgclient"
)

type setupDialog struct {
	in      *bufio.Scanner
	out     io.Writer
	groups  []selector.Group
	dialogs []tgclient.Dialog
	sel     selector.Selection
	cfgPath string
	plan    func([]tgclient.Dialog) ([]planRow, error)
}

// ask returns false on EOF (Ctrl+D), which always means "leave without saving".
func (s *setupDialog) ask(prompt string) (string, bool) {
	fmt.Fprint(s.out, prompt)
	if !s.in.Scan() {
		fmt.Fprintln(s.out)
		return "", false
	}
	return strings.TrimSpace(s.in.Text()), true
}

func (s *setupDialog) run() bool {
	for {
		fmt.Fprintf(s.out, "\nВыбрано: %d чатов\n", len(s.sel))
		for i, g := range s.groups {
			fmt.Fprintf(s.out, "  %d) %s (%d, выбрано %d)\n", i+1, g.Title, len(g.Chats), s.sel.Count(g.Chats))
		}
		fmt.Fprintln(s.out, "  s) поиск по названию    d) готово    q) выйти без сохранения")
		line, ok := s.ask("> ")
		if !ok || line == "q" {
			return false
		}
		switch line {
		case "s":
			q, ok := s.ask("поиск: ")
			if !ok || !s.editGroup("Поиск: "+q, selector.Search(s.dialogs, q)) {
				return false
			}
		case "d":
			save, ok := s.confirm()
			if !ok {
				return false
			}
			if save {
				return true
			}
		default:
			n, err := strconv.Atoi(line)
			if err != nil || n < 1 || n > len(s.groups) {
				fmt.Fprintf(s.out, "неизвестная команда %q\n", line)
				continue
			}
			if !s.editGroup(s.groups[n-1].Title, s.groups[n-1].Chats) {
				return false
			}
		}
	}
}

// editGroup returns false on EOF.
func (s *setupDialog) editGroup(title string, chats []tgclient.Dialog) bool {
	for {
		fmt.Fprintf(s.out, "\n%s:\n", title)
		if len(chats) == 0 {
			fmt.Fprintln(s.out, "  (пусто)")
		}
		for i, c := range chats {
			mark := " "
			if s.sel[c.ID] {
				mark = "x"
			}
			fmt.Fprintf(s.out, "  %d) [%s] %s  %s\n", i+1, mark, c.Title, c.Type)
		}
		fmt.Fprintln(s.out, "номера (1,3,5-7) — переключить, a — все, n — ни одного, Enter — назад")
		line, ok := s.ask("> ")
		if !ok {
			return false
		}
		switch line {
		case "":
			return true
		case "a":
			s.sel.SetAll(chats, true)
		case "n":
			s.sel.SetAll(chats, false)
		default:
			idx, err := selector.ParseIndices(line, len(chats))
			if err != nil {
				fmt.Fprintf(s.out, "ошибка: %v\n", err)
				continue
			}
			s.sel.Toggle(chats, idx)
		}
	}
}

// confirm shows the dry-run table for the selection; ok is false on EOF.
func (s *setupDialog) confirm() (save, ok bool) {
	var chosen []tgclient.Dialog
	for _, d := range s.dialogs {
		if s.sel[d.ID] {
			chosen = append(chosen, d)
		}
	}
	if len(chosen) == 0 {
		fmt.Fprintln(s.out, "ничего не выбрано")
		return false, true
	}
	rows, err := s.plan(chosen)
	if err != nil {
		fmt.Fprintf(s.out, "не удалось посчитать сообщения: %v\n", err)
	} else {
		printPlan(s.out, rows)
	}
	ans, ok := s.ask(fmt.Sprintf("Записать в %s (%d чатов)? [y/N]: ", s.cfgPath, len(chosen)))
	if !ok {
		return false, false
	}
	return strings.EqualFold(ans, "y"), true
}

func dialogItems(chosen []tgclient.Dialog, groups []selector.Group) []planItem {
	items := make([]planItem, 0, len(chosen))
	for _, d := range chosen {
		var in []string
		for _, g := range groups {
			for _, c := range g.Chats {
				if c.ID == d.ID {
					in = append(in, g.Title)
					break
				}
			}
		}
		items = append(items, planItem{ID: d.ID, Type: d.Type, Title: d.Title, Source: strings.Join(in, ", "), Peer: d.Peer})
	}
	return items
}

func cmdSetup(ctx context.Context, env Env) int {
	st, err := loadSetup(env, false)
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup setup:", err)
		return 1
	}
	err = tgclient.Run(ctx, st.clientOptions(), func(ctx context.Context, c *telegram.Client) error {
		if err := tgclient.RequireAuth(ctx, c); err != nil {
			return err
		}
		raw := c.API()
		api := tgclient.NewAPI(raw)
		self, err := tgclient.SelfID(ctx, raw)
		if err != nil {
			return err
		}
		dialogs, err := api.Dialogs(ctx)
		if err != nil {
			return err
		}
		folders, err := api.Folders(ctx, self)
		if err != nil {
			return err
		}
		sel, warnings := selector.Initial(dialogs, folders, st.cfg.Targets)
		for _, w := range warnings {
			fmt.Fprintf(env.Stdout, "текущий конфиг: %s\n", w)
		}
		groups := selector.Groups(dialogs, folders)
		d := &setupDialog{
			in: bufio.NewScanner(env.Stdin), out: env.Stdout,
			groups: groups, dialogs: dialogs, sel: sel, cfgPath: st.paths.Config,
			plan: func(chosen []tgclient.Dialog) ([]planRow, error) {
				return planRows(ctx, api, dialogItems(chosen, groups))
			},
		}
		if !d.run() {
			fmt.Fprintln(env.Stdout, "конфиг не изменён")
			return nil
		}
		titles := map[int64]string{}
		for _, dg := range dialogs {
			titles[dg.ID] = dg.Title
		}
		if err := config.WriteTargets(st.paths.Config, d.sel.IDs(), titles); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "записано %d чатов в %s\n", len(d.sel), st.paths.Config)
		return nil
	})
	if err != nil {
		fmt.Fprintln(env.Stderr, "tgbup setup:", err)
		return 1
	}
	return 0
}
