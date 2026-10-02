package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func reopenAnchor() time.Time {
	return time.Date(2026, time.February, 5, 10, 0, 0, 0, time.UTC)
}

func reopenProof() string {
	return strings.Repeat("ab", 32)
}

func reopenSentence() Sentence {
	return Sentence{
		ID: "sentenca-1", Case: "caso-1", Accused: "ana",
		Sanction:   SanctionRestriction,
		Competence: "inquisicao-severa: fraude contra a Carta",
		Charter:    "v2", FactDigest: reopenProof(),
		Motive:  "fraude provada pelo selo, restricao temporaria proporcional",
		Decider: "arbitro-1", DecidedAt: reopenAnchor(),
	}
}

func reopenRequest(id, ground string) ReopenRequest {
	return ReopenRequest{
		ID: ReopeningID("reabertura-" + id), Sentence: reopenSentence(),
		GroundRaw: ground, Reviewer: "auditor-1",
		Detail:    "fundamento concreto com referencia aos autos",
		DecidedAt: reopenAnchor().Add(24 * time.Hour),
	}
}

func TestGenuineNewProofReopens(t *testing.T) {
	fresh := strings.Repeat("09", 32)
	req := reopenRequest("1", "prova-nova")
	req.NewDigest = fresh
	reopening, err := DecideReopening(req)
	if err != nil {
		t.Fatalf("DecideReopening: %v", err)
	}
	if reopening.Ground != GroundNewProof || reopening.NewDigest != fresh {
		t.Fatalf("reopening = %+v, want the new material proof", reopening)
	}
	if reopening.Case != "caso-1" || reopening.Sentence != "sentenca-1" {
		t.Fatalf("reopening = %+v, want the same fact revisited", reopening)
	}
	if reopening.Reviewer != "auditor-1" || reopening.ByDecree {
		t.Fatalf("reopening = %+v, want the independent reviewer without decree", reopening)
	}
	for _, ground := range []string{"fraude-processual", "erro-decisivo"} {
		req := reopenRequest(ground, ground)
		if _, err := DecideReopening(req); err != nil {
			t.Fatalf("DecideReopening(%s): %v", ground, err)
		}
	}
	decreed := reopenRequest("2", "prova-nova")
	decreed.NewDigest = fresh
	decreed.ByDecree = true
	decreed.DecreeRaw = "decreto-7"
	decreed.Authority = "rei-1"
	royal, err := DecideReopening(decreed)
	if err != nil {
		t.Fatalf("decreed reopening: %v", err)
	}
	if !royal.ByDecree || royal.Decree != "decreto-7" || royal.Authority != "rei-1" {
		t.Fatalf("reopening = %+v, want the exceptional decree identified as royal", royal)
	}
}

func TestTwentyRepeatedPleasOpenNoTwentyCases(t *testing.T) {
	denied := []DeniedPlea{}
	opened := 0
	for i := 0; i < 20; i++ {
		req := reopenRequest("bond", "caucao-maior")
		req.Denied = denied
		_, err := DecideReopening(req)
		if err == nil {
			opened++
			continue
		}
		if i == 0 {
			if !errors.Is(err, ErrInsufficientGround) {
				t.Fatalf("first bond plea = %v, want ErrInsufficientGround", err)
			}
			denied = append(denied, DeniedPlea{Ground: "caucao-maior"})
			continue
		}
		if !errors.Is(err, ErrRepeatedPetition) {
			t.Fatalf("plea %d = %v, want ErrRepeatedPetition", i+1, err)
		}
	}
	if opened != 0 {
		t.Fatalf("opened = %d cases, want none from repeated pleas", opened)
	}
	stale := reopenRequest("3", "prova-nova")
	stale.NewDigest = reopenProof()
	stale.Denied = []DeniedPlea{{Ground: string(GroundNewProof), Digest: reopenProof()}}
	if _, err := DecideReopening(stale); !errors.Is(err, ErrRepeatedPetition) {
		t.Fatalf("replayed proof = %v, want ErrRepeatedPetition", err)
	}
}

func TestLargerBondAloneNeverSuffices(t *testing.T) {
	bond := reopenRequest("4", "caucao-maior")
	if _, err := DecideReopening(bond); !errors.Is(err, ErrInsufficientGround) {
		t.Fatalf("larger bond = %v, want ErrInsufficientGround", err)
	}
	recycled := reopenRequest("5", "prova-nova")
	recycled.NewDigest = reopenProof()
	if _, err := DecideReopening(recycled); !errors.Is(err, ErrInsufficientGround) {
		t.Fatalf("recycled proof = %v, want ErrInsufficientGround", err)
	}
	raw := reopenRequest("6", "prova-nova")
	raw.NewDigest = "prova-crua"
	if _, err := DecideReopening(raw); !errors.Is(err, ErrInsufficientGround) {
		t.Fatalf("unsealed proof = %v, want ErrInsufficientGround", err)
	}
	nameless := reopenRequest("7", "decreto")
	nameless.ByDecree = true
	if _, err := DecideReopening(nameless); !errors.Is(err, ErrInsufficientGround) {
		t.Fatalf("nameless decree plea = %v, want ErrInsufficientGround", err)
	}
}

func TestReopeningReviewerStaysIndependent(t *testing.T) {
	selfReview := reopenRequest("8", "erro-decisivo")
	selfReview.Reviewer = "arbitro-1"
	if _, err := DecideReopening(selfReview); !errors.Is(err, ErrSelfReview) {
		t.Fatalf("decider reopening = %v, want ErrSelfReview", err)
	}
	anonymous := reopenRequest("9", "decreto")
	anonymous.ByDecree = true
	anonymous.DecreeRaw = "decreto-7"
	anonymous.Authority = "rei-1"
	if _, err := DecideReopening(anonymous); !errors.Is(err, ErrInsufficientGround) {
		t.Fatalf("decree without ground = %v, want ErrInsufficientGround", err)
	}
	grounded := reopenRequest("10", "erro-decisivo")
	grounded.ByDecree = true
	grounded.Authority = "rei-1"
	if _, err := DecideReopening(grounded); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("unnamed decree = %v, want ErrInvalidReport", err)
	}
}
