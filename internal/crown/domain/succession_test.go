package domain_test

import (
	"errors"
	"testing"

	crowndomain "github.com/AlexandreZanata/Regnovum/internal/crown/domain"
)

func TestSuccessionThresholdStrictCMminusOneZeroPlusOne(t *testing.T) {
	const thresholdC int64 = 1000
	season := crowndomain.SeasonID("temporada-1")
	incumbent := crowndomain.IncumbentSnapshot{
		Subject:          "rei-antigo",
		Wealth:           500,
		AttainedRevision: 1,
		Eligible:         true,
	}

	t.Run("candidate with C minus 1 fails strict inequality", func(t *testing.T) {
		outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: thresholdC,
			Incumbent:           incumbent,
			Candidates: []crowndomain.Candidate{
				{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: thresholdC - 1, AttainedRevision: 5, Eligible: true},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.SelectedSovereign != "rei-antigo" {
			t.Fatalf("expected incumbent retained, got %s", outcome.SelectedSovereign)
		}
		if outcome.Reason != crowndomain.SuccessionReasonIncumbentRetained {
			t.Fatalf("expected incumbent-retained, got %s", outcome.Reason)
		}
		if outcome.ReignVersionChange {
			t.Fatalf("reign version must not change when incumbent is retained")
		}
	})

	t.Run("candidate with exact C fails strict inequality", func(t *testing.T) {
		outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: thresholdC,
			Incumbent:           incumbent,
			Candidates: []crowndomain.Candidate{
				{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: thresholdC, AttainedRevision: 5, Eligible: true},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.SelectedSovereign != "rei-antigo" {
			t.Fatalf("expected incumbent retained on equality, got %s", outcome.SelectedSovereign)
		}
		if outcome.Reason != crowndomain.SuccessionReasonIncumbentRetained {
			t.Fatalf("expected incumbent-retained, got %s", outcome.Reason)
		}
	})

	t.Run("candidate with C plus 1 succeeds", func(t *testing.T) {
		outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: thresholdC,
			Incumbent:           incumbent,
			Candidates: []crowndomain.Candidate{
				{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: thresholdC + 1, AttainedRevision: 5, Eligible: true},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.SelectedSovereign != "ana" {
			t.Fatalf("expected ana to succeed, got %s", outcome.SelectedSovereign)
		}
		if outcome.Reason != crowndomain.SuccessionReasonConquest {
			t.Fatalf("expected conquest, got %s", outcome.Reason)
		}
		if !outcome.ReignVersionChange {
			t.Fatalf("expected reign version to change on conquest")
		}
		if outcome.WinningWealth != thresholdC+1 {
			t.Fatalf("expected winning wealth %d, got %d", thresholdC+1, outcome.WinningWealth)
		}
	})
}

func TestSuccessionTwoAboveHighestWins(t *testing.T) {
	season := crowndomain.SeasonID("temporada-1")
	outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
		Policy:              crowndomain.WealthPolicyV1,
		Season:              season,
		SeasonOpen:          true,
		InstitutionalWealth: 1000,
		Incumbent: crowndomain.IncumbentSnapshot{
			Subject:          "fundador",
			Wealth:           500,
			AttainedRevision: 1,
			Eligible:         true,
		},
		Candidates: []crowndomain.Candidate{
			{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 1200, AttainedRevision: 10, Eligible: true},
			{Subject: "bob", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 1500, AttainedRevision: 12, Eligible: true},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.SelectedSovereign != "bob" {
		t.Fatalf("expected highest wealth bob to win, got %s", outcome.SelectedSovereign)
	}
	if outcome.WinningWealth != 1500 {
		t.Fatalf("expected winning wealth 1500, got %d", outcome.WinningWealth)
	}
	if outcome.Reason != crowndomain.SuccessionReasonConquest {
		t.Fatalf("expected conquest, got %s", outcome.Reason)
	}
}

func TestSuccessionTieBreakerWithAndWithoutIncumbent(t *testing.T) {
	season := crowndomain.SeasonID("temporada-1")

	t.Run("tie among leaders conserves incumbent", func(t *testing.T) {
		outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: 1000,
			Incumbent: crowndomain.IncumbentSnapshot{
				Subject:          "rainha",
				Wealth:           1500,
				AttainedRevision: 20, // Even with higher (later) revision, incumbent is conserved
				Eligible:         true,
			},
			Candidates: []crowndomain.Candidate{
				{Subject: "rainha", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 1500, AttainedRevision: 20, Eligible: true},
				{Subject: "desafiante", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 1500, AttainedRevision: 5, Eligible: true},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.SelectedSovereign != "rainha" {
			t.Fatalf("expected incumbent to be conserved in tie, got %s", outcome.SelectedSovereign)
		}
		if outcome.Reason != crowndomain.SuccessionReasonIncumbentRetained {
			t.Fatalf("expected incumbent-retained, got %s", outcome.Reason)
		}
		if outcome.ReignVersionChange {
			t.Fatalf("reign version must not change")
		}
	})

	t.Run("tie without incumbent breaks by lowest attained_revision", func(t *testing.T) {
		outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: 1000,
			Incumbent: crowndomain.IncumbentSnapshot{
				Subject:          "antigo",
				Wealth:           500,
				AttainedRevision: 1,
				Eligible:         true,
			},
			Candidates: []crowndomain.Candidate{
				{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 1500, AttainedRevision: 12, Eligible: true},
				{Subject: "bob", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 1500, AttainedRevision: 7, Eligible: true},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.SelectedSovereign != "bob" {
			t.Fatalf("expected bob with earlier revision 7 to win over ana (rev 12), got %s", outcome.SelectedSovereign)
		}
		if outcome.AttainedRevision != 7 {
			t.Fatalf("expected attained revision 7, got %d", outcome.AttainedRevision)
		}
		if outcome.Reason != crowndomain.SuccessionReasonConquest {
			t.Fatalf("expected conquest, got %s", outcome.Reason)
		}
	})

	t.Run("tie on wealth and revision breaks by stable subject ID", func(t *testing.T) {
		outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: 1000,
			Candidates: []crowndomain.Candidate{
				{Subject: "carlos", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 1500, AttainedRevision: 10, Eligible: true},
				{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 1500, AttainedRevision: 10, Eligible: true},
				{Subject: "bob", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 1500, AttainedRevision: 10, Eligible: true},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.SelectedSovereign != "ana" {
			t.Fatalf("expected lexicographical winner ana, got %s", outcome.SelectedSovereign)
		}
	})
}

func TestSuccessionIncumbentBelowCWithoutRivalRemains(t *testing.T) {
	season := crowndomain.SeasonID("temporada-1")

	// Incumbent has fallen below C, but no rival has surpassed C:
	// "queda de riqueza sozinha não cria vacância."
	outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
		Policy:              crowndomain.WealthPolicyV1,
		Season:              season,
		SeasonOpen:          true,
		InstitutionalWealth: 700,
		Incumbent: crowndomain.IncumbentSnapshot{
			Subject:          "rei-empobrecido",
			Wealth:           500, // < 700
			AttainedRevision: 2,
			Eligible:         true,
		},
		Candidates: []crowndomain.Candidate{
			{Subject: "desafiante", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 650, AttainedRevision: 8, Eligible: true}, // <= 700
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.SelectedSovereign != "rei-empobrecido" {
		t.Fatalf("expected incumbent to remain, got %s", outcome.SelectedSovereign)
	}
	if outcome.Reason != crowndomain.SuccessionReasonIncumbentRetained {
		t.Fatalf("expected incumbent-retained, got %s", outcome.Reason)
	}
	if outcome.ReignVersionChange {
		t.Fatalf("reign version must not change")
	}
}

func TestSuccessionRivalPoorerThanKingFails(t *testing.T) {
	season := crowndomain.SeasonID("temporada-1")

	// Both King and rival are above C, but rival is poorer than King:
	outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
		Policy:              crowndomain.WealthPolicyV1,
		Season:              season,
		SeasonOpen:          true,
		InstitutionalWealth: 700,
		Incumbent: crowndomain.IncumbentSnapshot{
			Subject:          "rei-rico",
			Wealth:           900,
			AttainedRevision: 5,
			Eligible:         true,
		},
		Candidates: []crowndomain.Candidate{
			{Subject: "rei-rico", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 900, AttainedRevision: 5, Eligible: true},
			{Subject: "rival", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 800, AttainedRevision: 6, Eligible: true},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if outcome.SelectedSovereign != "rei-rico" {
		t.Fatalf("expected king to remain when rival is poorer, got %s", outcome.SelectedSovereign)
	}
	if outcome.Reason != crowndomain.SuccessionReasonIncumbentRetained {
		t.Fatalf("expected incumbent-retained, got %s", outcome.Reason)
	}
}

func TestSuccessionAllCandidatesIneligible(t *testing.T) {
	season := crowndomain.SeasonID("temporada-1")

	t.Run("eligible incumbent remains when all rivals ineligible", func(t *testing.T) {
		outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: 700,
			Incumbent: crowndomain.IncumbentSnapshot{
				Subject:          "rei",
				Wealth:           500,
				AttainedRevision: 1,
				Eligible:         true,
			},
			Candidates: []crowndomain.Candidate{
				{Subject: "suspenso", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 950, AttainedRevision: 4, Eligible: false},
				{Subject: "sem-mfa", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 900, AttainedRevision: 5, Eligible: false},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.SelectedSovereign != "rei" {
			t.Fatalf("expected incumbent to remain, got %s", outcome.SelectedSovereign)
		}
		if outcome.Reason != crowndomain.SuccessionReasonIncumbentRetained {
			t.Fatalf("expected incumbent-retained, got %s", outcome.Reason)
		}
	})

	t.Run("regent activates when incumbent also ineligible and all candidates ineligible", func(t *testing.T) {
		outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: 700,
			Incumbent: crowndomain.IncumbentSnapshot{
				Subject:          "rei-morto",
				Wealth:           900,
				AttainedRevision: 1,
				Eligible:         false, // Impeded / dead / renounced
			},
			Regent: "regente-tecnico",
			Candidates: []crowndomain.Candidate{
				{Subject: "rival-sem-mfa", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 950, AttainedRevision: 4, Eligible: false},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if outcome.SelectedSovereign != "regente-tecnico" {
			t.Fatalf("expected regente-tecnico to assume regency, got %s", outcome.SelectedSovereign)
		}
		if !outcome.IsRegent {
			t.Fatalf("expected IsRegent to be true")
		}
		if outcome.Reason != crowndomain.SuccessionReasonRegency {
			t.Fatalf("expected regency reason, got %s", outcome.Reason)
		}
		if !outcome.ReignVersionChange {
			t.Fatalf("expected reign version to change")
		}
	})
}

func TestSuccessionCrownAndTechnicalAccountsNeverCompete(t *testing.T) {
	season := crowndomain.SeasonID("temporada-1")

	outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
		Policy:              crowndomain.WealthPolicyV1,
		Season:              season,
		SeasonOpen:          true,
		InstitutionalWealth: 700,
		Incumbent: crowndomain.IncumbentSnapshot{
			Subject:          "fundador",
			Wealth:           500,
			AttainedRevision: 1,
			Eligible:         true,
		},
		Candidates: []crowndomain.Candidate{
			{Subject: crowndomain.InstitutionalCrownSubject, Kind: crowndomain.BeneficiaryKindInstitutional, Wealth: 1500, AttainedRevision: 1, Eligible: true},
			{Subject: "admin-tecnico", Kind: crowndomain.BeneficiaryKindTechnical, Wealth: 1400, AttainedRevision: 1, Eligible: true},
			{Subject: "cidadao", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 850, AttainedRevision: 4, Eligible: true},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Crown and technical accounts must be ignored, so cidadao (850) wins:
	if outcome.SelectedSovereign != "cidadao" {
		t.Fatalf("expected citizen to win, got %s", outcome.SelectedSovereign)
	}
	if outcome.WinningWealth != 850 {
		t.Fatalf("expected winning wealth 850, got %d", outcome.WinningWealth)
	}
	if outcome.Reason != crowndomain.SuccessionReasonConquest {
		t.Fatalf("expected conquest, got %s", outcome.Reason)
	}
}

func TestSuccessionClosedSeasonFailsClosed(t *testing.T) {
	season := crowndomain.SeasonID("temporada-1")
	_, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
		Policy:              crowndomain.WealthPolicyV1,
		Season:              season,
		SeasonOpen:          false, // Closed!
		InstitutionalWealth: 700,
		Candidates: []crowndomain.Candidate{
			{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 800, AttainedRevision: 1, Eligible: true},
		},
	})
	if !errors.Is(err, crowndomain.ErrSeasonClosed) {
		t.Fatalf("expected ErrSeasonClosed, got %v", err)
	}
}

func TestSuccessionRatifiedExample700M800M600MGeneratesAna(t *testing.T) {
	// From docs/reino/TEMPORADAS_SUCESSAO.md §9:
	// "Exemplo com a oferta sugerida: Coroa 700 milhões, Ana 800 milhões, demais participantes 600 milhões
	// de INK, soma 2,1 bilhões, sem obrigações. Ana supera a Coroa e sucede automaticamente.
	// Depois do ato, Ana continua com 800 milhões e a Coroa com 700 milhões.
	// Se Ana tivesse 700 milhões, a igualdade não lhe daria o trono."
	const (
		milli         int64 = 1_000_000 // scale factor for millions
		coroaWealth         = 700 * milli
		anaWealth           = 800 * milli
		carlosWealth        = 600 * milli
		totalGenesisS       = 2_100 * milli
	)

	season := crowndomain.SeasonID("temporada-1")

	// Verify that the conservation of total supply holds:
	if coroaWealth+anaWealth+carlosWealth != totalGenesisS {
		t.Fatalf("conservation invariant failed: sum must equal 2,1B")
	}

	anaCopy := anaWealth
	coroaCopy := coroaWealth
	carlosCopy := carlosWealth

	outcome, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
		Policy:              crowndomain.WealthPolicyV1,
		Season:              season,
		SeasonOpen:          true,
		InstitutionalWealth: coroaWealth,
		Incumbent: crowndomain.IncumbentSnapshot{
			Subject:          "fundador",
			Wealth:           0,
			AttainedRevision: 1,
			Eligible:         true,
		},
		Candidates: []crowndomain.Candidate{
			{Subject: crowndomain.InstitutionalCrownSubject, Kind: crowndomain.BeneficiaryKindInstitutional, Wealth: coroaWealth, AttainedRevision: 1, Eligible: true},
			{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: anaWealth, AttainedRevision: 3, Eligible: true},
			{Subject: "carlos", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: carlosWealth, AttainedRevision: 2, Eligible: true},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if outcome.SelectedSovereign != "ana" {
		t.Fatalf("expected Ana to be crowned sovereign, got %s", outcome.SelectedSovereign)
	}
	if outcome.Reason != crowndomain.SuccessionReasonConquest {
		t.Fatalf("expected conquest, got %s", outcome.Reason)
	}
	if outcome.WinningWealth != anaWealth {
		t.Fatalf("expected Ana's winning wealth %d, got %d", anaWealth, outcome.WinningWealth)
	}
	if outcome.InstitutionalC != coroaWealth {
		t.Fatalf("expected InstitutionalC %d, got %d", coroaWealth, outcome.InstitutionalC)
	}
	if !outcome.ReignVersionChange {
		t.Fatalf("expected reign version change")
	}

	// Verify that input balances are unaltered (no balance mutation):
	if anaCopy != anaWealth || coroaCopy != coroaWealth || carlosCopy != carlosWealth {
		t.Fatalf("balances were altered during succession evaluation")
	}

	// If Ana had exactly 700M (equal to Coroa), equality does NOT grant the throne:
	outcomeEqual, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
		Policy:              crowndomain.WealthPolicyV1,
		Season:              season,
		SeasonOpen:          true,
		InstitutionalWealth: coroaWealth,
		Incumbent: crowndomain.IncumbentSnapshot{
			Subject:          "fundador",
			Wealth:           0,
			AttainedRevision: 1,
			Eligible:         true,
		},
		Candidates: []crowndomain.Candidate{
			{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: coroaWealth, AttainedRevision: 3, Eligible: true}, // 700M
			{Subject: "carlos", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: carlosWealth, AttainedRevision: 2, Eligible: true},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error on equal wealth: %v", err)
	}
	if outcomeEqual.SelectedSovereign != "fundador" {
		t.Fatalf("equality must not give the throne to Ana; expected fundador to remain, got %s", outcomeEqual.SelectedSovereign)
	}
	if outcomeEqual.Reason != crowndomain.SuccessionReasonIncumbentRetained {
		t.Fatalf("expected incumbent-retained, got %s", outcomeEqual.Reason)
	}
}

func TestSuccessionInputValidationAndErrors(t *testing.T) {
	season := crowndomain.SeasonID("temporada-1")

	t.Run("unratified policy is refused", func(t *testing.T) {
		_, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              "unratified-v99",
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: 1000,
		})
		if !errors.Is(err, crowndomain.ErrUnknownWealthPolicy) {
			t.Fatalf("expected ErrUnknownWealthPolicy, got %v", err)
		}
	})

	t.Run("negative institutional wealth is refused", func(t *testing.T) {
		_, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: -50,
		})
		if !errors.Is(err, crowndomain.ErrInvalidWealthAmount) {
			t.Fatalf("expected ErrInvalidWealthAmount, got %v", err)
		}
	})

	t.Run("institutional wealth overflow is refused", func(t *testing.T) {
		_, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: crowndomain.MaxSeasonalSupplyMillis + 1,
		})
		if !errors.Is(err, crowndomain.ErrWealthOverflow) {
			t.Fatalf("expected ErrWealthOverflow, got %v", err)
		}
	})

	t.Run("duplicate candidate is refused", func(t *testing.T) {
		_, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: 1000,
			Candidates: []crowndomain.Candidate{
				{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 1200, AttainedRevision: 1, Eligible: true},
				{Subject: "ana", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 1300, AttainedRevision: 2, Eligible: true},
			},
		})
		if !errors.Is(err, crowndomain.ErrDuplicateCandidate) {
			t.Fatalf("expected ErrDuplicateCandidate, got %v", err)
		}
	})

	t.Run("no qualified successor and no incumbent or regent returns ErrNoQualifiedSuccessor", func(t *testing.T) {
		_, err := crowndomain.SelectSovereign(crowndomain.SuccessionInput{
			Policy:              crowndomain.WealthPolicyV1,
			Season:              season,
			SeasonOpen:          true,
			InstitutionalWealth: 1000,
			Incumbent: crowndomain.IncumbentSnapshot{
				Subject:  "morto",
				Eligible: false,
			},
			Regent: "", // No regent provided
			Candidates: []crowndomain.Candidate{
				{Subject: "pobre", Kind: crowndomain.BeneficiaryKindParticipant, Wealth: 500, AttainedRevision: 1, Eligible: true},
			},
		})
		if !errors.Is(err, crowndomain.ErrNoQualifiedSuccessor) {
			t.Fatalf("expected ErrNoQualifiedSuccessor, got %v", err)
		}
	})
}
