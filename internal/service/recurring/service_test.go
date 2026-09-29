package recurring_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/apperror"
	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/models"
	"aura-saku/internal/service/recurring"
)

// setup is an integration test helper against a real Postgres instance (see
// docker-compose.yml). Skipped automatically if no DB is reachable, so
// `go test ./...` still passes without the local dev stack.
func setup(t *testing.T) (*recurring.Service, *gorm.DB, *models.User) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("skipping integration test, config load failed: %v", err)
	}
	gormDB, err := db.NewPostgres(cfg)
	if err != nil {
		t.Skipf("skipping integration test, postgres not reachable: %v", err)
	}
	if err := db.RunMigrations(gormDB); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	user := models.User{
		TelegramChatID:  time.Now().UnixNano(),
		DefaultCurrency: "IDR",
		Tier:            models.TierFree,
	}
	if err := gormDB.WithContext(context.Background()).Create(&user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		gormDB.Unscoped().Where("user_id = ?", user.ID).Delete(&models.RecurringTransaction{})
		gormDB.Unscoped().Where("id = ?", user.ID).Delete(&models.User{})
	})

	return recurring.NewService(gormDB), gormDB, &user
}

// TestCreate_SchedulesNextOccurrenceNotToday covers the actual DB-backed
// Create path end to end: the row is persisted with the fields from parsed,
// and NextRunAt is in the future, never today's date, regardless of what
// day-of-month was requested relative to today.
func TestCreate_SchedulesNextOccurrenceNotToday(t *testing.T) {
	svc, _, user := setup(t)
	ctx := context.Background()

	parsed := &ai.ParsedTransaction{
		Type: "expense", Amount: 150_000, Currency: "idr", CategoryName: "subscription",
		Description: "netflix", Bucket: "wants", DayOfMonth: 5,
	}

	rt, err := svc.Create(ctx, user.ID, parsed)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rt.ID == 0 {
		t.Fatal("expected a non-zero ID")
	}
	if rt.Currency != "IDR" {
		t.Errorf("Currency = %q, want uppercased \"IDR\"", rt.Currency)
	}
	if rt.Bucket == nil || *rt.Bucket != models.BucketWants {
		t.Errorf("Bucket = %v, want a pointer to wants", rt.Bucket)
	}
	if !rt.IsActive {
		t.Error("expected a freshly created recurring transaction to be active")
	}

	now := time.Now().UTC()
	if !rt.NextRunAt.After(now) {
		t.Errorf("NextRunAt = %v, want strictly after now (%v) — must never fire on creation day", rt.NextRunAt, now)
	}
	if rt.NextRunAt.Day() != 5 {
		t.Errorf("NextRunAt day = %d, want 5", rt.NextRunAt.Day())
	}
}

// TestListActive_OnlyReturnsThisUsersActiveOnes covers both halves of the
// filter: another user's row must never show up, and a canceled
// (deactivated) one must be excluded too.
func TestListActive_OnlyReturnsThisUsersActiveOnes(t *testing.T) {
	svc, gormDB, user := setup(t)
	ctx := context.Background()

	otherUser := models.User{TelegramChatID: time.Now().UnixNano() + 1, DefaultCurrency: "IDR", Tier: models.TierFree}
	if err := gormDB.Create(&otherUser).Error; err != nil {
		t.Fatalf("create other test user: %v", err)
	}
	t.Cleanup(func() {
		gormDB.Unscoped().Where("user_id = ?", otherUser.ID).Delete(&models.RecurringTransaction{})
		gormDB.Unscoped().Where("id = ?", otherUser.ID).Delete(&models.User{})
	})

	active, err := svc.Create(ctx, user.ID, &ai.ParsedTransaction{Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "rent", DayOfMonth: 1})
	if err != nil {
		t.Fatalf("Create active: %v", err)
	}
	canceled, err := svc.Create(ctx, user.ID, &ai.ParsedTransaction{Type: "expense", Amount: 50_000, Currency: "IDR", CategoryName: "gym", DayOfMonth: 10})
	if err != nil {
		t.Fatalf("Create to-be-canceled: %v", err)
	}
	if _, err := svc.Cancel(ctx, user.ID, canceled.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, err := svc.Create(ctx, otherUser.ID, &ai.ParsedTransaction{Type: "expense", Amount: 999_000, Currency: "IDR", CategoryName: "other-user", DayOfMonth: 15}); err != nil {
		t.Fatalf("Create for other user: %v", err)
	}

	list, err := svc.ListActive(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListActive returned %d rows, want exactly 1 (the still-active one)", len(list))
	}
	if list[0].ID != active.ID {
		t.Errorf("ListActive returned id %d, want the active one (%d)", list[0].ID, active.ID)
	}
}

// TestCancel_CrossUserOwnershipIsEnforced guards the same ownership
// guarantee transaction.Service's correction/deletion targeting relies on:
// a user can never cancel another user's recurring transaction by guessing
// its id.
func TestCancel_CrossUserOwnershipIsEnforced(t *testing.T) {
	svc, gormDB, user := setup(t)
	ctx := context.Background()

	otherUser := models.User{TelegramChatID: time.Now().UnixNano() + 1, DefaultCurrency: "IDR", Tier: models.TierFree}
	if err := gormDB.Create(&otherUser).Error; err != nil {
		t.Fatalf("create other test user: %v", err)
	}
	t.Cleanup(func() {
		gormDB.Unscoped().Where("user_id = ?", otherUser.ID).Delete(&models.RecurringTransaction{})
		gormDB.Unscoped().Where("id = ?", otherUser.ID).Delete(&models.User{})
	})

	rt, err := svc.Create(ctx, user.ID, &ai.ParsedTransaction{Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "rent", DayOfMonth: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.Cancel(ctx, otherUser.ID, rt.ID); !errors.Is(err, apperror.ErrRecurringNotFound) {
		t.Errorf("Cancel across users err = %v, want ErrRecurringNotFound", err)
	}

	list, err := svc.ListActive(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("recurring transaction should still be active after a cross-user cancel attempt, ListActive returned %d rows", len(list))
	}

	if _, err := svc.Cancel(ctx, user.ID, 999_999_999); !errors.Is(err, apperror.ErrRecurringNotFound) {
		t.Errorf("Cancel(nonexistent id) err = %v, want ErrRecurringNotFound", err)
	}
}

// TestDueAndAdvance_OnlyFiresWhenActuallyDueAndNeverDrifts covers the
// scheduler job's own query (Due) plus the drift-free advance guarantee:
// a row due now shows up, a row scheduled for the future doesn't, and after
// Advance the schedule moves forward from its own NextRunAt (staying fixed
// on the same day-of-month) rather than from whatever "now" happened to be.
func TestDueAndAdvance_OnlyFiresWhenActuallyDueAndNeverDrifts(t *testing.T) {
	svc, gormDB, user := setup(t)
	ctx := context.Background()

	dueSoon, err := svc.Create(ctx, user.ID, &ai.ParsedTransaction{Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "rent", DayOfMonth: 1})
	if err != nil {
		t.Fatalf("Create dueSoon: %v", err)
	}
	// Force it into the past so it's actually due right now, without
	// waiting for a real month boundary in the test.
	past := time.Now().UTC().Add(-1 * time.Hour)
	if err := gormDB.Model(&models.RecurringTransaction{}).Where("id = ?", dueSoon.ID).Update("next_run_at", past).Error; err != nil {
		t.Fatalf("backdate dueSoon.NextRunAt: %v", err)
	}

	notDue, err := svc.Create(ctx, user.ID, &ai.ParsedTransaction{Type: "expense", Amount: 50_000, Currency: "IDR", CategoryName: "gym", DayOfMonth: 28})
	if err != nil {
		t.Fatalf("Create notDue: %v", err)
	}

	now := time.Now().UTC()
	due, err := svc.Due(ctx, now)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}

	var foundDue, foundNotDue bool
	var dueSoonRow *models.RecurringTransaction
	for _, rt := range due {
		if rt.ID == dueSoon.ID {
			foundDue = true
			dueSoonRow = rt
			if rt.User.ID != user.ID {
				t.Errorf("Due() row's preloaded User.ID = %d, want %d", rt.User.ID, user.ID)
			}
		}
		if rt.ID == notDue.ID {
			foundNotDue = true
		}
	}
	if !foundDue {
		t.Fatal("Due() did not return the row backdated into the past")
	}
	if foundNotDue {
		t.Fatal("Due() returned a row scheduled for the future — must not be due yet")
	}

	// Capture the value as it actually came back from Postgres (whose
	// timestamp precision may truncate sub-microsecond digits from the
	// original Go time.Time) rather than reusing the pre-round-trip `past`
	// variable, so the Equal check below isn't comparing against a value
	// with different precision than what Advance itself started from.
	beforeAdvance := dueSoonRow.NextRunAt
	if err := svc.Advance(ctx, dueSoonRow); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	wantNext := beforeAdvance.AddDate(0, 1, 0)
	if !dueSoonRow.NextRunAt.Equal(wantNext) {
		t.Errorf("NextRunAt after Advance = %v, want exactly one month after the PREVIOUS NextRunAt (%v), not from \"now\"", dueSoonRow.NextRunAt, wantNext)
	}

	// After advancing, it must no longer show up as due (its new NextRunAt
	// is back in the future relative to the backdated "past" origin, and
	// definitely after `now` since original past + 1 month > now).
	dueAfter, err := svc.Due(ctx, now)
	if err != nil {
		t.Fatalf("Due (after advance): %v", err)
	}
	for _, rt := range dueAfter {
		if rt.ID == dueSoon.ID {
			t.Error("advanced recurring transaction still shows up as due — schedule did not actually move forward")
		}
	}
}
