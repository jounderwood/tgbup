package convert

import (
	"encoding/json"
	"testing"

	"github.com/gotd/td/tg"
)

// actionJSON stores an action the way Message does.
func actionJSON(t *testing.T, a tg.MessageActionClass) string {
	t.Helper()
	b, err := json.Marshal(struct {
		Type string                `json:"type"`
		Data tg.MessageActionClass `json:"data"`
	}{a.TypeName(), a})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTodoEvents(t *testing.T) {
	checklist := `{"todo":{"title":"💳","items":[{"id":1,"title":"Оплатить счета"},{"id":2,"title":"Вынести мусор"}]}}`
	done := actionJSON(t, &tg.MessageActionTodoCompletions{Completed: []int{1, 2}})
	undo := actionJSON(t, &tg.MessageActionTodoCompletions{Incompleted: []int{2}})
	add := actionJSON(t, &tg.MessageActionTodoAppendTasks{List: []tg.TodoItem{{ID: 3, Title: tg.TextWithEntities{Text: "Налоги"}}}})

	if got := TodoEventText(done, checklist); got != "marked done «Оплатить счета», «Вынести мусор»" {
		t.Errorf("done text = %q", got)
	}
	if got := TodoEventText(undo, checklist); got != "marked not done «Вынести мусор»" {
		t.Errorf("undo text = %q", got)
	}
	if got := TodoEventText(add, ""); got != "added to the checklist «Налоги»" {
		t.Errorf("add text = %q", got)
	}
	if got := TodoEventText(done, ""); got != "marked done #1, #2" {
		t.Errorf("without the checklist = %q", got)
	}

	meta := checklist
	for _, e := range []string{done, undo, add} {
		meta, _ = ApplyTodoEvent(meta, e, 42, 1700)
	}
	var m Meta
	if err := json.Unmarshal([]byte(meta), &m); err != nil {
		t.Fatal(err)
	}
	it := m.Todo.Items
	if len(it) != 3 || it[0].DoneBy != 42 || it[0].DoneAt != 1700 || it[1].DoneAt != 0 || it[2].Title != "Налоги" {
		t.Fatalf("items = %+v", it)
	}
	// Every run applies all events again: the result must be the same.
	again := meta
	for _, e := range []string{done, undo, add} {
		again, _ = ApplyTodoEvent(again, e, 42, 1700)
	}
	if again != meta {
		t.Errorf("applying the events again changed the checklist:\n%s\n%s", meta, again)
	}
	if _, changed := ApplyTodoEvent(`{"poll":{}}`, done, 1, 1); changed {
		t.Error("an event changed a message that is not a checklist")
	}
}
