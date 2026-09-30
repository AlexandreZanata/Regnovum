package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	eligibilityPersonA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	eligibilityPersonB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	eligibilityEpoch   = "2026-W53"
)

func admitReq(account, person string) AdmitRequest {
	return AdmitRequest{
		Account: account, PersonProofHash: person, EpochKey: eligibilityEpoch,
		AccountActive: true, AntifraudClear: true,
	}
}

func mustAdmit(t *testing.T, req AdmitRequest, admitted []Grant) Grant {
	t.Helper()
	grant, err := AdmitNewcomer(req, admitted)
	if err != nil {
		t.Fatalf("AdmitNewcomer(%+v): %v", req, err)
	}
	if grant.Status != NewcomerAdmitted {
		t.Fatalf("status = %q, want admitted", grant.Status)
	}
	if err := grant.VerifyHash(); err != nil {
		t.Fatalf("VerifyHash: %v", err)
	}
	return grant
}

func TestAdmitNewcomerNeedsNoActivity(t *testing.T) {
	grant := mustAdmit(t, admitReq("campones-1", eligibilityPersonA), nil)
	if grant.Account != "campones-1" || grant.PersonHash != eligibilityPersonA {
		t.Fatalf("grant = %+v, want minimal identity kept", grant)
	}
	banned := []string{"email", "name", "cpf", "document", "face", "biometr", "post", "like", "work", "activit"}
	for _, typ := range []any{AdmitRequest{}, Grant{}} {
		fields := reflect.VisibleFields(reflect.TypeOf(typ))
		for _, field := range fields {
			lower := strings.ToLower(field.Name)
			for _, bad := range banned {
				if strings.Contains(lower, bad) {
					t.Fatalf("field %q carries %q: admission stores proof only, never PII or activity", field.Name, bad)
				}
			}
		}
	}
	for _, raw := range []AdmitRequest{
		{Account: "", PersonProofHash: eligibilityPersonA, EpochKey: eligibilityEpoch, AccountActive: true, AntifraudClear: true},
		{Account: "campones-1", PersonProofHash: "not-a-digest", EpochKey: eligibilityEpoch, AccountActive: true, AntifraudClear: true},
		{Account: "campones-1", PersonProofHash: eligibilityPersonA, EpochKey: "2021-W53", AccountActive: true, AntifraudClear: true},
	} {
		if _, err := AdmitNewcomer(raw, nil); !errors.Is(err, ErrInvalidNewcomer) {
			t.Fatalf("AdmitNewcomer(%+v) = %v, want ErrInvalidNewcomer", raw, err)
		}
	}
}

func TestDuplicatePersonBlocksLaterAccount(t *testing.T) {
	first := mustAdmit(t, admitReq("campones-1", eligibilityPersonA), nil)
	admitted := []Grant{first}
	blocked, err := AdmitNewcomer(admitReq("campones-2", eligibilityPersonA), admitted)
	if !errors.Is(err, ErrDuplicateNewcomer) {
		t.Fatalf("duplicate = %v, want ErrDuplicateNewcomer", err)
	}
	if blocked.Status != NewcomerBlocked {
		t.Fatalf("duplicate status = %q, want blocked with appeal", blocked.Status)
	}
	if err := blocked.VerifyHash(); err != nil {
		t.Fatalf("blocked VerifyHash: %v", err)
	}
	replay, err := AdmitNewcomer(admitReq("campones-1", eligibilityPersonA), admitted)
	if err != nil || replay.Hash != first.Hash || replay.Status != NewcomerAdmitted {
		t.Fatalf("replay = %+v/%v, want idempotent admitted %q", replay, err, first.Hash)
	}
	if _, err := AdmitNewcomer(AdmitRequest{Account: "campones-1", PersonProofHash: eligibilityPersonB, EpochKey: eligibilityEpoch, AccountActive: true, AntifraudClear: true}, admitted); !errors.Is(err, ErrInvalidNewcomer) {
		t.Fatalf("account-epoch conflict = %v, want ErrInvalidNewcomer without a grant", err)
	}
}

func TestTwoSimultaneousAccountsShareOneGrant(t *testing.T) {
	first := mustAdmit(t, admitReq("conta-manha", eligibilityPersonA), nil)
	second, err := AdmitNewcomer(admitReq("conta-tarde", eligibilityPersonA), []Grant{first})
	if !errors.Is(err, ErrDuplicateNewcomer) || second.Status != NewcomerBlocked {
		t.Fatalf("simultaneous = %+v/%v, want blocked duplicate", second, err)
	}
}

func TestBlockedStandingWithholdsWithAppeal(t *testing.T) {
	inactive, err := AdmitNewcomer(AdmitRequest{Account: "campones-1", PersonProofHash: eligibilityPersonA, EpochKey: eligibilityEpoch}, nil)
	if !errors.Is(err, ErrAccountBlocked) || inactive.Status != NewcomerBlocked {
		t.Fatalf("inactive = %+v/%v, want blocked", inactive, err)
	}
	fraud, err := AdmitNewcomer(AdmitRequest{Account: "campones-1", PersonProofHash: eligibilityPersonA, EpochKey: eligibilityEpoch, AccountActive: true}, nil)
	if !errors.Is(err, ErrAccountBlocked) || fraud.Status != NewcomerBlocked {
		t.Fatalf("fraud = %+v/%v, want blocked", fraud, err)
	}
	appealed, err := inactive.Appeal("campones-1")
	if err != nil || appealed.Status != NewcomerAppealed {
		t.Fatalf("appeal = %+v/%v, want appealed", appealed, err)
	}
	if _, err := inactive.Appeal("estranho"); !errors.Is(err, ErrNewcomerNotParty) {
		t.Fatalf("stranger appeal = %v, want ErrNewcomerNotParty", err)
	}
	admitted := mustAdmit(t, admitReq("campones-9", eligibilityPersonB), nil)
	if _, err := admitted.Appeal("campones-9"); !errors.Is(err, ErrNewcomerState) {
		t.Fatalf("admitted appeal = %v, want ErrNewcomerState", err)
	}
}

func TestCancelWithdrawsBeforeDistribution(t *testing.T) {
	admitted := mustAdmit(t, admitReq("campones-1", eligibilityPersonA), nil)
	cancelled, err := admitted.Cancel()
	if err != nil || cancelled.Status != NewcomerCancelled {
		t.Fatalf("cancel = %+v/%v, want cancelled", cancelled, err)
	}
	if _, err := cancelled.Cancel(); !errors.Is(err, ErrNewcomerState) {
		t.Fatalf("second cancel = %v, want ErrNewcomerState: terminal never reopens", err)
	}
	if _, err := cancelled.Appeal("campones-1"); !errors.Is(err, ErrNewcomerState) {
		t.Fatalf("cancelled appeal = %v, want ErrNewcomerState", err)
	}
	blocked, _ := AdmitNewcomer(admitReq("campones-2", eligibilityPersonA), []Grant{admitted})
	appealed, _ := blocked.Appeal("campones-2")
	if _, err := appealed.Cancel(); err != nil {
		t.Fatalf("appealed cancel: %v", err)
	}
}

func TestErasureRemovesAccountKeepsUniqueness(t *testing.T) {
	admitted := mustAdmit(t, admitReq("campones-1", eligibilityPersonA), nil)
	erased, err := admitted.Erase()
	if err != nil {
		t.Fatalf("Erase: %v", err)
	}
	if erased.Status != NewcomerErased || erased.Account != "" {
		t.Fatalf("erased = %+v, want account removed", erased)
	}
	if erased.PersonHash != eligibilityPersonA || erased.EpochKey != eligibilityEpoch {
		t.Fatalf("erased = %+v, want digest and epoch kept for uniqueness", erased)
	}
	if err := erased.VerifyHash(); err != nil {
		t.Fatalf("erased VerifyHash: %v", err)
	}
	if _, err := erased.Erase(); !errors.Is(err, ErrNewcomerState) {
		t.Fatalf("second erase = %v, want ErrNewcomerState", err)
	}
	if _, err := erased.Cancel(); !errors.Is(err, ErrNewcomerState) {
		t.Fatalf("erased cancel = %v, want ErrNewcomerState", err)
	}
	if _, err := AdmitNewcomer(admitReq("campones-2", eligibilityPersonA), []Grant{erased}); !errors.Is(err, ErrDuplicateNewcomer) {
		t.Fatalf("post-erasure duplicate = %v, want ErrDuplicateNewcomer: erasure frees nothing", err)
	}
}
