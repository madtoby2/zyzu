package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestScheduledMessageLifecycle(t *testing.T) {
	st, err := New(filepath.Join(t.TempDir(), "scheduled.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	message := &ScheduledMessage{
		ChannelCategory: "电影",
		ChannelID:       -100123,
		Content:         "今晚八点见",
		ScheduleType:    "interval",
		IntervalMinutes: 30,
		Enabled:         true,
	}
	if err := st.CreateScheduledMessage(message); err != nil {
		t.Fatal(err)
	}
	if message.ID == 0 || !message.NextRunAt.After(time.Now()) {
		t.Fatalf("created message = %#v", message)
	}

	messages, err := st.ListScheduledMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Content != message.Content {
		t.Fatalf("messages = %#v", messages)
	}

	if _, err := st.db.Exec("UPDATE scheduled_messages SET next_run_at=? WHERE id=?", time.Now().Add(-time.Minute), message.ID); err != nil {
		t.Fatal(err)
	}
	due, err := st.GetDueScheduledMessages(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != message.ID {
		t.Fatalf("due messages = %#v", due)
	}

	next := time.Now().Add(time.Hour)
	if err := st.MarkScheduledMessageResult(message.ID, true, next, "sent", ""); err != nil {
		t.Fatal(err)
	}
	updated, err := st.GetScheduledMessage(message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastSentAt == nil || updated.LastStatus != "sent" {
		t.Fatalf("updated message = %#v", updated)
	}

	if err := st.DeleteScheduledMessage(message.ID); err != nil {
		t.Fatal(err)
	}
}

func TestNextScheduledRunUsesShanghaiDailyTime(t *testing.T) {
	after := time.Date(2026, 7, 30, 0, 30, 0, 0, time.UTC) // 08:30 in Shanghai
	next := NextScheduledRun("daily", 0, "09:00", after)
	want := time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}

	after = time.Date(2026, 7, 30, 2, 0, 0, 0, time.UTC) // 10:00 in Shanghai
	next = NextScheduledRun("daily", 0, "09:00", after)
	want = time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next day = %s, want %s", next, want)
	}
}
