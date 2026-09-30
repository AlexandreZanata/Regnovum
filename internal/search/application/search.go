// Package application coordinates public full-text search without exposing
// persistence details or moderation-private data.
package application

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultLimit  = 20
	MaxLimit      = 100
	cursorVersion = "v1"
)

var (
	ErrInvalidQuery     = errors.New("search: invalid query")
	ErrInvalidCursor    = errors.New("search: invalid cursor")
	ErrWeakCursorSecret = errors.New("search: cursor secret must contain at least 256 bits")
)

// ArenaResult is the public Arena search projection.
type ArenaResult struct {
	ID          string
	Slug        string
	Statement   string
	Category    string
	Language    string
	PublishedAt time.Time
	Score       float64
}

// ArgumentResult is the public published-argument search projection. It has
// no author identity, email, moderation evidence or provider data.
type ArgumentResult struct {
	ID        string
	ArenaID   string
	Relation  string
	Content   string
	Language  string
	CreatedAt time.Time
	Score     float64
}

type ArenaPage struct {
	Items      []ArenaResult
	NextCursor string
}
type ArgumentPage struct {
	Items      []ArgumentResult
	NextCursor string
}

type ArenaSearchRepository interface {
	SearchArenas(ctx context.Context, query, language string, after *Cursor, limit int) ([]ArenaResult, error)
}
type ArgumentSearchRepository interface {
	SearchArguments(ctx context.Context, query, language string, after *Cursor, limit int) ([]ArgumentResult, error)
}

type Cursor struct {
	Score float64
	At    time.Time
	ID    string
	Kind  string
}

type CursorCodec struct{ secret []byte }

func NewCursorCodec(secret []byte) (*CursorCodec, error) {
	if len(secret) < 32 {
		return nil, ErrWeakCursorSecret
	}
	return &CursorCodec{secret: append([]byte(nil), secret...)}, nil
}
func (c *CursorCodec) Encode(cursor Cursor) string {
	payload := strings.Join([]string{cursorVersion, cursor.Kind, strconv.FormatFloat(cursor.Score, 'g', 17, 64), cursor.At.UTC().Format(time.RFC3339Nano), cursor.ID}, "|")
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (c *CursorCodec) Decode(raw string, kind string) (*Cursor, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return nil, ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidCursor
	}
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, ErrInvalidCursor
	}
	fields := strings.Split(string(payload), "|")
	if len(fields) != 5 || fields[0] != cursorVersion || fields[1] != kind {
		return nil, ErrInvalidCursor
	}
	score, err := strconv.ParseFloat(fields[2], 64)
	if err != nil || math.IsNaN(score) || math.IsInf(score, 0) || score < 0 {
		return nil, ErrInvalidCursor
	}
	at, err := time.Parse(time.RFC3339Nano, fields[3])
	if err != nil || at.IsZero() || fields[4] == "" {
		return nil, ErrInvalidCursor
	}
	return &Cursor{Score: score, At: at.UTC(), ID: fields[4], Kind: kind}, nil
}

type ArenaSearchUseCase struct {
	repository ArenaSearchRepository
	cursors    *CursorCodec
}

func NewArenaSearchUseCase(repository ArenaSearchRepository, cursors *CursorCodec) (*ArenaSearchUseCase, error) {
	if repository == nil || cursors == nil {
		return nil, ErrInvalidQuery
	}
	return &ArenaSearchUseCase{repository: repository, cursors: cursors}, nil
}
func (uc *ArenaSearchUseCase) Execute(ctx context.Context, query, language, rawCursor string, limit int) (*ArenaPage, error) {
	query, language, limit, err := validate(query, language, limit)
	if err != nil {
		return nil, err
	}
	after, err := uc.cursors.Decode(rawCursor, "arena")
	if err != nil {
		return nil, err
	}
	items, err := uc.repository.SearchArenas(ctx, query, language, after, limit+1)
	if err != nil {
		return nil, err
	}
	page := &ArenaPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = uc.cursors.Encode(Cursor{Score: last.Score, At: last.PublishedAt, ID: last.ID, Kind: "arena"})
	}
	return page, nil
}

type ArgumentSearchUseCase struct {
	repository ArgumentSearchRepository
	cursors    *CursorCodec
}

func NewArgumentSearchUseCase(repository ArgumentSearchRepository, cursors *CursorCodec) (*ArgumentSearchUseCase, error) {
	if repository == nil || cursors == nil {
		return nil, ErrInvalidQuery
	}
	return &ArgumentSearchUseCase{repository: repository, cursors: cursors}, nil
}
func (uc *ArgumentSearchUseCase) Execute(ctx context.Context, query, language, rawCursor string, limit int) (*ArgumentPage, error) {
	query, language, limit, err := validate(query, language, limit)
	if err != nil {
		return nil, err
	}
	after, err := uc.cursors.Decode(rawCursor, "argument")
	if err != nil {
		return nil, err
	}
	items, err := uc.repository.SearchArguments(ctx, query, language, after, limit+1)
	if err != nil {
		return nil, err
	}
	page := &ArgumentPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = uc.cursors.Encode(Cursor{Score: last.Score, At: last.CreatedAt, ID: last.ID, Kind: "argument"})
	}
	return page, nil
}

func validate(query, language string, limit int) (string, string, int, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 200 {
		return "", "", 0, fmt.Errorf("%w: query must contain 1..200 characters", ErrInvalidQuery)
	}
	// NUL nunca chega ao banco: o PostgreSQL recusa o byte e o erro do
	// driver viraria 500 não classificado. A busca é texto de interface,
	// e NUL não é texto.
	if strings.ContainsRune(query, '\x00') {
		return "", "", 0, fmt.Errorf("%w: query must not contain NUL", ErrInvalidQuery)
	}
	if language != "" && language != "pt-BR" && language != "en-US" {
		return "", "", 0, fmt.Errorf("%w: unsupported language", ErrInvalidQuery)
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	return query, language, limit, nil
}
