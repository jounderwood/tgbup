package convert

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// todoAction is what a checklist event (messageActionTodoCompletions,
// messageActionTodoAppendTasks) stores in service_action_json.
type todoAction struct {
	Type string `json:"type"`
	Data struct {
		Completed   []int `json:"Completed"`
		Incompleted []int `json:"Incompleted"`
		List        []struct {
			ID    int `json:"ID"`
			Title struct {
				Text string `json:"Text"`
			} `json:"Title"`
		} `json:"List"`
	} `json:"data"`
}

// ApplyTodoEvent brings a checklist's meta up to an event that came later:
// Telegram does not resend the checklist when someone ticks an item or adds
// one. from and date are the event's sender and time. It returns the new meta
// and whether it changed; applying the same events again changes nothing.
func ApplyTodoEvent(metaJSON, actionJSON string, from int64, date int) (string, bool) {
	var meta Meta
	var a todoAction
	if json.Unmarshal([]byte(metaJSON), &meta) != nil || meta.Todo == nil || json.Unmarshal([]byte(actionJSON), &a) != nil {
		return metaJSON, false
	}
	items := meta.Todo.Items
	switch a.Type {
	case "messageActionTodoCompletions":
		for i := range items {
			switch {
			case slices.Contains(a.Data.Completed, items[i].ID):
				items[i].DoneBy, items[i].DoneAt = from, date
			case slices.Contains(a.Data.Incompleted, items[i].ID):
				items[i].DoneBy, items[i].DoneAt = 0, 0
			}
		}
	case "messageActionTodoAppendTasks":
		for _, it := range a.Data.List {
			if !slices.ContainsFunc(items, func(x TodoItem) bool { return x.ID == it.ID }) {
				items = append(items, TodoItem{ID: it.ID, Title: it.Title.Text})
			}
		}
	default:
		return metaJSON, false
	}
	meta.Todo.Items = items
	b, err := json.Marshal(meta)
	if err != nil || string(b) == metaJSON {
		return metaJSON, false
	}
	return string(b), true
}

// TodoEventText says what a checklist event did, naming the items by their
// titles in the checklist (checklistMeta); an item not found shows as #id.
func TodoEventText(actionJSON, checklistMeta string) string {
	var a todoAction
	if json.Unmarshal([]byte(actionJSON), &a) != nil {
		return ""
	}
	var meta Meta
	_ = json.Unmarshal([]byte(checklistMeta), &meta)
	title := func(id int) string {
		if meta.Todo != nil {
			for _, it := range meta.Todo.Items {
				if it.ID == id {
					return "«" + it.Title + "»"
				}
			}
		}
		return "#" + strconv.Itoa(id)
	}
	list := func(ids []int) string {
		names := make([]string, len(ids))
		for i, id := range ids {
			names[i] = title(id)
		}
		return strings.Join(names, ", ")
	}
	switch a.Type {
	case "messageActionTodoCompletions":
		var parts []string
		if len(a.Data.Completed) > 0 {
			parts = append(parts, "marked done "+list(a.Data.Completed))
		}
		if len(a.Data.Incompleted) > 0 {
			parts = append(parts, "marked not done "+list(a.Data.Incompleted))
		}
		if len(parts) == 0 {
			return "changed the checklist"
		}
		return strings.Join(parts, "; ")
	case "messageActionTodoAppendTasks":
		names := make([]string, len(a.Data.List))
		for i, it := range a.Data.List {
			names[i] = "«" + it.Title.Text + "»"
		}
		return "added to the checklist " + strings.Join(names, ", ")
	}
	return ""
}
