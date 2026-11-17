package chat

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPlannerRenamePersistsAndPublishesTheOwnedThread(t *testing.T) {
	planner, db, _ := newTestPlanner(t, plannerGeneratorFunc(nil))
	id := createOwnedPlannerThread(t, planner, "kimicode-plus.planner")
	if _, err := planner.PlannerAction(context.Background(), "planner.rename", []byte(mustPlannerJSON(map[string]any{
		"id": id, "pluginOwner": "kimicode-plus.other", "title": "越权修改",
	}))); err == nil {
		t.Fatal("another plugin renamed the thread")
	}
	value, err := planner.PlannerAction(context.Background(), "planner.rename", []byte(mustPlannerJSON(map[string]any{
		"id": id, "pluginOwner": "kimicode-plus.planner", "title": "  发布计划  ",
	})))
	if err != nil {
		t.Fatal(err)
	}
	thread := value.(PlannerThread)
	if thread.Title != "发布计划" || thread.Revision != 2 {
		t.Fatalf("rename did not update the thread: %+v", thread)
	}
	var raw string
	if err := db.QueryRow(`SELECT data FROM planner_threads WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored PlannerThread
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Title != "发布计划" || stored.Revision != 2 {
		t.Fatalf("rename was not persisted: %+v", stored)
	}
	for _, title := range []string{"", string(make([]byte, 201))} {
		if _, err := planner.PlannerAction(context.Background(), "planner.rename", []byte(mustPlannerJSON(map[string]any{
			"id": id, "pluginOwner": "kimicode-plus.planner", "title": title,
		}))); err == nil {
			t.Fatalf("invalid title %q was accepted", title)
		}
	}
}
