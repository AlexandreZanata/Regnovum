package domain

import (
	"errors"
	"testing"
	"time"
)

func archiveSeason(t *testing.T) Season {
	t.Helper()
	return mustSeason(t, seasonReq())
}

func TestArchiveReceiptShape(t *testing.T) {
	season := archiveSeason(t)
	good := ArchiveReceipt{
		Season: season.Manifest.ID, ManifestHash: season.Manifest.Hash,
		CutoffAt: season.EndsAt, SealedAt: season.EndsAt.Add(time.Minute),
		Milli: 700, Legs: 10, Intentions: 5,
	}
	if err := good.Valid(); err != nil {
		t.Fatalf("Valid: %v", err)
	}
	for _, bad := range []ArchiveReceipt{
		{Season: "", ManifestHash: season.Manifest.Hash, CutoffAt: season.EndsAt, SealedAt: season.EndsAt, Milli: 1},
		{Season: season.Manifest.ID, ManifestHash: "selo", CutoffAt: season.EndsAt, SealedAt: season.EndsAt, Milli: 1},
		{Season: season.Manifest.ID, ManifestHash: season.Manifest.Hash, Milli: 1},
		{Season: season.Manifest.ID, ManifestHash: season.Manifest.Hash, CutoffAt: season.EndsAt, SealedAt: season.EndsAt, Milli: -1},
	} {
		if err := bad.Valid(); err == nil {
			t.Fatalf("shapeless receipt %+v passed", bad)
		}
	}
}

func TestArchiveManifestTamperRefuses(t *testing.T) {
	manifest := mustManifest(t, seasonReq())
	if err := VerifyArchiveManifest(manifest); err != nil {
		t.Fatalf("VerifyArchiveManifest: %v", err)
	}
	tampered := manifest
	tampered.PolicyRef = "politica-nova"
	if err := VerifyArchiveManifest(tampered); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("tampered manifest = %v, want ErrInvalidSeason: checksum adulterado impede abertura", err)
	}
}

func TestConservationDivergenceRefusesOpening(t *testing.T) {
	book := SealSnapshot{Milli: 700, Legs: 10, Intentions: 5}
	if err := VerifyConservationForOpening(book, book); err != nil {
		t.Fatalf("conserved: %v", err)
	}
	minted := SealSnapshot{Milli: 699, Legs: 10, Intentions: 5}
	if err := VerifyConservationForOpening(book, minted); err == nil {
		t.Fatal("diverged S opened: Σ≠S impede abertura")
	}
	rewound := SealSnapshot{Milli: 700, Legs: 9, Intentions: 5}
	if err := VerifyConservationForOpening(book, rewound); err == nil {
		t.Fatal("rewound history opened: o diário nunca regride")
	}
	grown := SealSnapshot{Milli: 700, Legs: 12, Intentions: 6}
	if err := VerifyConservationForOpening(book, grown); err != nil {
		t.Fatalf("grown snapshot: %v", err)
	}
}

func TestSuccessorContinuityAndFreshBook(t *testing.T) {
	previous := archiveSeason(t)
	nextReq := seasonReq()
	nextReq.ID = "temporada-2"
	nextReq.Ordinal = 2
	nextReq.StartsAt = previous.EndsAt
	if _, err := VerifySuccessorContinuity(previous, nextReq); err != nil {
		t.Fatalf("continuity: %v", err)
	}
	gapped := nextReq
	gapped.StartsAt = previous.EndsAt.Add(time.Second)
	if _, err := VerifySuccessorContinuity(previous, gapped); !errors.Is(err, ErrInvalidSeason) {
		t.Fatalf("gapped successor = %v, want ErrInvalidSeason: downtime preserva o calendário", err)
	}
	if err := VerifyFreshBook(700, 0, 700); err != nil {
		t.Fatalf("fresh book: %v", err)
	}
	if err := VerifyFreshBook(699, 0, 700); err == nil {
		t.Fatal("short treasury opened: o Tesouro do livro novo guarda exatamente S")
	}
	if err := VerifyFreshBook(700, 1, 700); err == nil {
		t.Fatal("carry-over opened: zero saldo novo nasce de vencedor anterior")
	}
}
