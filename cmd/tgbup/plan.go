package main

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/gotd/td/tg"
)

type planItem struct {
	ID     int64
	Type   string
	Title  string
	Source string
	Peer   tg.InputPeerClass
}

type planRow struct {
	ID     int64
	Type   string
	Title  string
	Source string
	Count  int
}

type historyCounter interface {
	HistoryCount(ctx context.Context, peer tg.InputPeerClass) (int, error)
}

func planRows(ctx context.Context, c historyCounter, items []planItem) ([]planRow, error) {
	rows := make([]planRow, 0, len(items))
	for _, it := range items {
		n, err := c.HistoryCount(ctx, it.Peer)
		if err != nil {
			return nil, fmt.Errorf("count messages in %d %q: %w", it.ID, it.Title, err)
		}
		rows = append(rows, planRow{ID: it.ID, Type: it.Type, Title: it.Title, Source: it.Source, Count: n})
	}
	return rows, nil
}

func printPlan(w io.Writer, rows []planRow) {
	total := 0
	for _, r := range rows {
		total += r.Count
	}
	fmt.Fprintf(w, "would archive %d chats, ~%s messages:\n", len(rows), groupDigits(total))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t~%s\n", r.ID, r.Type, r.Title, r.Source, groupDigits(r.Count))
	}
	tw.Flush()
}
