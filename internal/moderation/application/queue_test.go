package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/moderation/application"
	"github.com/AlexandreZanata/Regnovum/internal/moderation/domain"
)

type fakeQueue struct {
	items []application.QueueItem
}

func (f *fakeQueue) ListQueuePage(_ context.Context, status string, after *application.QueuePosition, limit int) ([]application.QueueItem, error) {
	filtered := make([]application.QueueItem, 0, len(f.items))
	for _, item := range f.items {
		if status != "" && string(item.Status) != status {
			continue
		}
		if after != nil && !(item.CreatedAt.Before(after.CreatedAt) || (item.CreatedAt.Equal(after.CreatedAt) && item.CaseID < after.CaseID)) {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) > limit {
		return filtered[:limit], nil
	}
	return filtered, nil
}

var queueSecret = []byte("queue-cursor-secret-0123456789abcdef")

func queueViewer() (domain.AccountID, *fakeRoles) {
	viewer := domain.AccountID("018f6b2a-0000-7000-8000-000000000091")
	roles := &fakeRoles{assignments: map[domain.AccountID]*application.RoleAssignment{
		viewer: {AccountID: viewer, Role: domain.RoleModerator},
	}}
	return viewer, roles
}

func TestQueueListsFilteredPagesWithSignedCursors(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	queue := &fakeQueue{items: []application.QueueItem{
		{CaseID: "case-003", Target: domain.TargetArena, TargetID: "target-3", Status: application.CaseOpen, Priority: "urgent", CreatedAt: base},
		{CaseID: "case-002", Target: domain.TargetArgument, TargetID: "target-2", Status: application.CaseUnderReview, Priority: "normal", CreatedAt: base.Add(-time.Hour)},
		{CaseID: "case-001", Target: domain.TargetProfile, TargetID: "target-1", Status: application.CaseOpen, Priority: "low", CreatedAt: base.Add(-2 * time.Hour)},
	}}
	viewer, roles := queueViewer()
	uc, err := application.NewGetCaseQueueUseCase(queue, roles)
	if err != nil {
		t.Fatalf("NewGetCaseQueueUseCase: %v", err)
	}
	codec, err := application.NewQueueCursorCodec(queueSecret)
	if err != nil {
		t.Fatalf("NewQueueCursorCodec: %v", err)
	}

	first, err := uc.Execute(context.Background(), viewer, "", "", 2, codec)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want 2 items with cursor", first)
	}
	if first.Items[0].CaseID != "case-003" || first.Items[1].CaseID != "case-002" {
		t.Fatalf("first page order = %v, want newest first", first.Items)
	}

	second, err := uc.Execute(context.Background(), viewer, "", first.NextCursor, 2, codec)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].CaseID != "case-001" || second.NextCursor != "" {
		t.Fatalf("second page = %+v, want final case-001 without cursor", second)
	}

	filtered, err := uc.Execute(context.Background(), viewer, "open", "", 10, codec)
	if err != nil {
		t.Fatalf("filtered: %v", err)
	}
	if len(filtered.Items) != 2 {
		t.Fatalf("open filter items = %d, want 2", len(filtered.Items))
	}
}

func TestQueueDeniesUnknownViewerAndForgedCursor(t *testing.T) {
	t.Parallel()

	queue := &fakeQueue{}
	viewer, roles := queueViewer()
	uc, err := application.NewGetCaseQueueUseCase(queue, roles)
	if err != nil {
		t.Fatalf("NewGetCaseQueueUseCase: %v", err)
	}
	codec, err := application.NewQueueCursorCodec(queueSecret)
	if err != nil {
		t.Fatalf("NewQueueCursorCodec: %v", err)
	}

	_, err = uc.Execute(context.Background(), domain.AccountID("018f6b2a-0000-7000-8000-000000000099"), "", "", 10, codec)
	if !errors.Is(err, application.ErrNotAuthorized) {
		t.Fatalf("stranger error = %v, want ErrNotAuthorized", err)
	}

	_, err = uc.Execute(context.Background(), viewer, "", "forged.cursor", 10, codec)
	if !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("forged cursor error = %v, want ErrInvalidCursor", err)
	}

	_, err = uc.Execute(context.Background(), viewer, "bogus", "", 10, codec)
	if !errors.Is(err, application.ErrInvalidQueueFilter) {
		t.Fatalf("bogus filter error = %v, want ErrInvalidQueueFilter", err)
	}
}

func TestQueueCursorSecretBoundaryAndIdentifierEdges(t *testing.T) {
	t.Parallel()

	// Thirty-two bytes are exactly 256 bits and must be accepted;
	// thirty-one refuse (mutation gate: queue.go:44).
	if _, err := application.NewQueueCursorCodec(make([]byte, 32)); err != nil {
		t.Fatalf("32-byte secret error = %v, want accepted", err)
	}
	if _, err := application.NewQueueCursorCodec(make([]byte, 31)); !errors.Is(err, application.ErrWeakQueueCursorSecret) {
		t.Fatalf("31-byte secret error = %v, want ErrWeakQueueCursorSecret", err)
	}

	codec, err := application.NewQueueCursorCodec(queueSecret)
	if err != nil {
		t.Fatalf("NewQueueCursorCodec: %v", err)
	}
	base := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	// The printable range edges are valid identifier bytes, while the
	// space and DEL beside them refuse (mutation gate: queue.go:118-119).
	edged := codec.Encode(base, "case!~9")
	position, err := codec.Decode(edged)
	if err != nil || position.CaseID != "case!~9" {
		t.Fatalf("edge cursor = (%+v, %v), want case!~9 decoded", position, err)
	}
	for _, id := range []string{"case 9", "case\x7f9"} {
		if _, err := codec.Decode(codec.Encode(base, id)); !errors.Is(err, application.ErrInvalidCursor) {
			t.Fatalf("identifier %q error = %v, want ErrInvalidCursor", id, err)
		}
	}
}

func TestQueueZeroLimitSelectsDefault(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	queue := &fakeQueue{items: []application.QueueItem{
		{CaseID: "case-003", Status: application.CaseOpen, CreatedAt: base},
		{CaseID: "case-002", Status: application.CaseOpen, CreatedAt: base.Add(-time.Hour)},
		{CaseID: "case-001", Status: application.CaseOpen, CreatedAt: base.Add(-2 * time.Hour)},
	}}
	viewer, roles := queueViewer()
	uc, err := application.NewGetCaseQueueUseCase(queue, roles)
	if err != nil {
		t.Fatalf("NewGetCaseQueueUseCase: %v", err)
	}
	codec, err := application.NewQueueCursorCodec(queueSecret)
	if err != nil {
		t.Fatalf("NewQueueCursorCodec: %v", err)
	}

	// A non-positive limit selects the default: all three rows arrive
	// with no cursor (mutation gate: queue.go:200).
	page, err := uc.Execute(context.Background(), viewer, "", "", 0, codec)
	if err != nil {
		t.Fatalf("zero limit: %v", err)
	}
	if len(page.Items) != 3 || page.NextCursor != "" {
		t.Fatalf("zero-limit page = %d items/cursor %q, want 3 with no cursor", len(page.Items), page.NextCursor)
	}
}
