package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AlexandreZanata/Regnovum/internal/arguments/application"
	"github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
)

type fakeArgumentQueryRepo struct {
	arenaArguments []application.PublicArgument
	replies        []application.PublicArgument
	single         *application.PublicArgument
	err            error
	lastLimit      int
	lastAfter      *application.ArgumentPosition
	lastRelation   domain.Relation
	lastParent     domain.ArgumentID
	lastArena      domain.ArenaID
}

func (r *fakeArgumentQueryRepo) ListArenaArguments(_ context.Context, arenaID domain.ArenaID, relation domain.Relation, after *application.ArgumentPosition, limit int) ([]application.PublicArgument, error) {
	r.lastArena = arenaID
	r.lastRelation = relation
	r.lastAfter = after
	r.lastLimit = limit
	if r.err != nil {
		return nil, r.err
	}
	return r.arenaArguments, nil
}

func (r *fakeArgumentQueryRepo) ListReplies(_ context.Context, parentID domain.ArgumentID, after *application.ArgumentPosition, limit int) ([]application.PublicArgument, error) {
	r.lastParent = parentID
	r.lastAfter = after
	r.lastLimit = limit
	if r.err != nil {
		return nil, r.err
	}
	return r.replies, nil
}

func (r *fakeArgumentQueryRepo) GetPublicArgument(_ context.Context, argumentID domain.ArgumentID) (*application.PublicArgument, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.single == nil {
		return nil, application.ErrArgumentNotFound
	}
	return r.single, nil
}

func newArgumentCursorCodec(t *testing.T) *application.ArgumentCursorCodec {
	t.Helper()
	codec, err := application.NewArgumentCursorCodec([]byte("arguments-cursor-secret-0123456789"))
	if err != nil {
		t.Fatalf("NewArgumentCursorCodec: %v", err)
	}
	return codec
}

func publicArgumentAt(t *testing.T, id string, createdAt time.Time) application.PublicArgument {
	t.Helper()
	content, err := domain.ParseContent("Conteúdo público do argumento", runeCounter)
	if err != nil {
		t.Fatalf("ParseContent: %v", err)
	}
	return application.PublicArgument{
		ID:        mustArgumentID(t, id),
		ArenaID:   mustArena(t, testArenaRaw),
		Relation:  mustRelation(t, domain.RelationSupport),
		Content:   &content,
		Status:    "published",
		CreatedAt: createdAt,
	}
}

func TestArgumentCursorRoundTripAndRejections(t *testing.T) {
	codec := newArgumentCursorCodec(t)
	position := publicArgumentAt(t, "018f6b2a-0000-7000-8000-0000000000ff", testInstant)

	encoded := codec.Encode(position)
	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded == nil || !decoded.CreatedAt.Equal(testInstant) || decoded.ArgumentID != position.ID.String() {
		t.Fatalf("decoded = %+v, want the encoded position", decoded)
	}

	if first, err := codec.Decode("   "); err != nil || first != nil {
		t.Fatalf("empty cursor = %v/%v, want a nil first page", first, err)
	}

	invalid := []string{
		"not-a-cursor",
		encoded + "tampered",
		"djF8MjAyNi0wOS0xN1QxMjowMDowMFp8YWJj.assinatura",
	}
	for _, raw := range invalid {
		if _, err := codec.Decode(raw); !errors.Is(err, application.ErrInvalidCursor) {
			t.Fatalf("Decode(%q) error = %v, want ErrInvalidCursor", raw, err)
		}
	}

	if _, err := application.NewArgumentCursorCodec([]byte("short")); !errors.Is(err, application.ErrWeakCursorSecret) {
		t.Fatalf("weak secret error = %v, want ErrWeakCursorSecret", err)
	}

	// Thirty-two bytes are exactly 256 bits and must be accepted; thirty-one refuse.
	if _, err := application.NewArgumentCursorCodec(make([]byte, 32)); err != nil {
		t.Fatalf("32-byte secret error = %v, want accepted", err)
	}
	if _, err := application.NewArgumentCursorCodec(make([]byte, 31)); !errors.Is(err, application.ErrWeakCursorSecret) {
		t.Fatalf("31-byte secret error = %v, want ErrWeakCursorSecret", err)
	}
}

func TestListArenaArgumentsPaginatesWithLookahead(t *testing.T) {
	repo := &fakeArgumentQueryRepo{arenaArguments: []application.PublicArgument{
		publicArgumentAt(t, "018f6b2a-0000-7000-8000-000000000001", testInstant),
		publicArgumentAt(t, "018f6b2a-0000-7000-8000-000000000002", testInstant.Add(-time.Minute)),
		publicArgumentAt(t, "018f6b2a-0000-7000-8000-000000000003", testInstant.Add(-2*time.Minute)),
	}}
	codec := newArgumentCursorCodec(t)
	useCase := application.NewListArenaArgumentsUseCase(repo, codec)

	page, err := useCase.Execute(context.Background(), application.ListArenaArgumentsQuery{
		ArenaID:  testArenaRaw,
		Relation: domain.RelationSupport,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(page.Arguments) != 2 {
		t.Fatalf("arguments = %d, want the limit of 2", len(page.Arguments))
	}
	if repo.lastLimit != 3 {
		t.Fatalf("repository limit = %d, want lookahead limit+1", repo.lastLimit)
	}
	if page.NextCursor == "" {
		t.Fatal("a full page must carry the next cursor")
	}
	position, err := codec.Decode(page.NextCursor)
	if err != nil {
		t.Fatalf("Decode(next) error = %v", err)
	}
	if position.ArgumentID != page.Arguments[1].ID.String() {
		t.Fatalf("next cursor = %q, want the last delivered argument", position.ArgumentID)
	}

	// The last page has no cursor.
	repo.arenaArguments = repo.arenaArguments[:1]
	page, err = useCase.Execute(context.Background(), application.ListArenaArgumentsQuery{
		ArenaID:  testArenaRaw,
		Relation: domain.RelationSupport,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("last page Execute() error = %v", err)
	}
	if page.NextCursor != "" {
		t.Fatalf("last page cursor = %q, want none", page.NextCursor)
	}

	// A page with exactly the requested rows is complete: trimming to the
	// same length must not mint a cursor to a page that does not exist
	// (mutation gate: public_arguments.go:108).
	repo.arenaArguments = repo.arenaArguments[:2]
	page, err = useCase.Execute(context.Background(), application.ListArenaArgumentsQuery{
		ArenaID:  testArenaRaw,
		Relation: domain.RelationSupport,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("exact page Execute() error = %v", err)
	}
	if len(page.Arguments) != 2 || page.NextCursor != "" {
		t.Fatalf("exact page = %d arguments/cursor %q, want two with no cursor", len(page.Arguments), page.NextCursor)
	}
}

func TestListArenaArgumentsClampsLimitsAndValidatesInputs(t *testing.T) {
	repo := &fakeArgumentQueryRepo{}
	codec := newArgumentCursorCodec(t)
	useCase := application.NewListArenaArgumentsUseCase(repo, codec)

	if _, err := useCase.Execute(context.Background(), application.ListArenaArgumentsQuery{
		ArenaID:  testArenaRaw,
		Relation: domain.RelationSupport,
	}); err != nil {
		t.Fatalf("default limit error = %v", err)
	}
	if repo.lastLimit != application.DefaultArgumentPageLimit+1 {
		t.Fatalf("default limit = %d, want %d plus lookahead", repo.lastLimit, application.DefaultArgumentPageLimit)
	}

	if _, err := useCase.Execute(context.Background(), application.ListArenaArgumentsQuery{
		ArenaID:  testArenaRaw,
		Relation: domain.RelationSupport,
		Limit:    5000,
	}); err != nil {
		t.Fatalf("large limit error = %v", err)
	}
	if repo.lastLimit != application.MaxArgumentPageLimit+1 {
		t.Fatalf("clamped limit = %d, want %d plus lookahead", repo.lastLimit, application.MaxArgumentPageLimit)
	}

	invalid := []struct {
		name  string
		query application.ListArenaArgumentsQuery
		want  error
	}{
		{name: "empty arena", query: application.ListArenaArgumentsQuery{Relation: domain.RelationSupport}, want: domain.ErrEmptyArenaID},
		{name: "empty relation", query: application.ListArenaArgumentsQuery{ArenaID: testArenaRaw}, want: domain.ErrEmptyRelation},
		{name: "unknown relation", query: application.ListArenaArgumentsQuery{ArenaID: testArenaRaw, Relation: "maybe"}, want: domain.ErrInvalidRelation},
		{name: "malformed cursor", query: application.ListArenaArgumentsQuery{ArenaID: testArenaRaw, Relation: domain.RelationSupport, Cursor: "not-a-cursor"}, want: application.ErrInvalidCursor},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := useCase.Execute(context.Background(), test.query); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestListRepliesPaginatesAndValidates(t *testing.T) {
	repo := &fakeArgumentQueryRepo{replies: []application.PublicArgument{
		publicArgumentAt(t, "018f6b2a-0000-7000-8000-000000000001", testInstant),
		publicArgumentAt(t, "018f6b2a-0000-7000-8000-000000000002", testInstant.Add(-time.Minute)),
	}}
	codec := newArgumentCursorCodec(t)
	useCase := application.NewListRepliesUseCase(repo, codec)

	page, err := useCase.Execute(context.Background(), application.ListRepliesQuery{ParentID: testParentRaw, Limit: 1})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(page.Arguments) != 1 || page.NextCursor == "" {
		t.Fatalf("page = %d arguments/cursor %q, want one with a cursor", len(page.Arguments), page.NextCursor)
	}
	if !repo.lastParent.Equals(mustArgumentID(t, testParentRaw)) || repo.lastLimit != 2 {
		t.Fatal("the repository must receive the parsed parent and the lookahead limit")
	}

	// Exactly the requested reply completes the page: no cursor may point
	// past it (mutation gate: public_arguments.go:156).
	solo := &fakeArgumentQueryRepo{replies: []application.PublicArgument{
		publicArgumentAt(t, "018f6b2a-0000-7000-8000-000000000001", testInstant),
	}}
	soloUseCase := application.NewListRepliesUseCase(solo, codec)
	soloPage, err := soloUseCase.Execute(context.Background(), application.ListRepliesQuery{ParentID: testParentRaw, Limit: 1})
	if err != nil {
		t.Fatalf("exact page Execute() error = %v", err)
	}
	if len(soloPage.Arguments) != 1 || soloPage.NextCursor != "" {
		t.Fatalf("exact page = %d arguments/cursor %q, want one with no cursor", len(soloPage.Arguments), soloPage.NextCursor)
	}

	if _, err := useCase.Execute(context.Background(), application.ListRepliesQuery{ParentID: ""}); !errors.Is(err, domain.ErrEmptyArgumentID) {
		t.Fatalf("empty parent error = %v, want ErrEmptyArgumentID", err)
	}
	if _, err := useCase.Execute(context.Background(), application.ListRepliesQuery{ParentID: testParentRaw, Cursor: "bad"}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("malformed cursor error = %v, want ErrInvalidCursor", err)
	}
}

func TestGetPublicArgumentAppliesVisibilityPolicy(t *testing.T) {
	retracted := publicArgumentAt(t, "018f6b2a-0000-7000-8000-0000000000ff", testInstant)
	retracted.Status = "withdrawn"
	retracted.Content = nil

	repo := &fakeArgumentQueryRepo{single: &retracted}
	useCase := application.NewGetPublicArgumentUseCase(repo)

	result, err := useCase.Execute(context.Background(), application.GetPublicArgumentQuery{ArgumentID: retracted.ID.String()})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Content != nil || result.Status != "withdrawn" {
		t.Fatalf("result = %+v, want the retracted placeholder", result)
	}

	if _, err := useCase.Execute(context.Background(), application.GetPublicArgumentQuery{ArgumentID: ""}); !errors.Is(err, domain.ErrEmptyArgumentID) {
		t.Fatalf("empty id error = %v, want ErrEmptyArgumentID", err)
	}

	missing := &fakeArgumentQueryRepo{}
	if _, err := application.NewGetPublicArgumentUseCase(missing).Execute(context.Background(), application.GetPublicArgumentQuery{ArgumentID: testParentRaw}); !errors.Is(err, application.ErrArgumentNotFound) {
		t.Fatalf("missing error = %v, want ErrArgumentNotFound", err)
	}
}

func TestPublicArgumentQueriesPropagateStorageFailures(t *testing.T) {
	storageErr := errors.New("storage down")
	repo := &fakeArgumentQueryRepo{err: storageErr}
	codec := newArgumentCursorCodec(t)

	if _, err := application.NewListArenaArgumentsUseCase(repo, codec).Execute(context.Background(), application.ListArenaArgumentsQuery{
		ArenaID:  testArenaRaw,
		Relation: domain.RelationSupport,
	}); !errors.Is(err, storageErr) {
		t.Fatalf("arena list error = %v, want the storage failure", err)
	}
	if _, err := application.NewListRepliesUseCase(repo, codec).Execute(context.Background(), application.ListRepliesQuery{ParentID: testParentRaw}); !errors.Is(err, storageErr) {
		t.Fatalf("replies error = %v, want the storage failure", err)
	}
	if _, err := application.NewGetPublicArgumentUseCase(repo).Execute(context.Background(), application.GetPublicArgumentQuery{ArgumentID: testParentRaw}); !errors.Is(err, storageErr) {
		t.Fatalf("get error = %v, want the storage failure", err)
	}
}
