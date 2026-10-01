package application

import (
	"context"
	"errors"
	"testing"
	"time"

	seasondomain "github.com/AlexandreZanata/Regnovum/internal/seasons/domain"
)

type fakeOpenerStore struct {
	archived map[string]ArchiveProof
	prepared map[string]bool
	active   map[string]bool
	failleft map[string]error
}

func newFakeOpenerStore() *fakeOpenerStore {
	return &fakeOpenerStore{
		archived: map[string]ArchiveProof{},
		prepared: map[string]bool{},
		active:   map[string]bool{},
		failleft: map[string]error{},
	}
}

func (f *fakeOpenerStore) Archive(_ context.Context, predecessor string) (ArchiveProof, error) {
	if err, ok := f.failleft["archive"]; ok {
		return ArchiveProof{}, err
	}
	if proof, ok := f.archived[predecessor]; ok {
		proof.Replayed = true
		return proof, nil
	}
	proof := ArchiveProof{Season: predecessor, CutoffAt: time.Now(), SealedAt: time.Now(), Milli: 700}
	f.archived[predecessor] = proof
	return proof, nil
}

func (f *fakeOpenerStore) EnsurePrepared(_ context.Context, predecessor string, next seasondomain.ManifestRequest) (bool, error) {
	if err, ok := f.failleft["prepared"]; ok {
		return false, err
	}
	if f.prepared[next.ID] {
		return true, nil
	}
	f.prepared[next.ID] = true
	return false, nil
}

func (f *fakeOpenerStore) Activate(_ context.Context, predecessor, successor string) (bool, error) {
	if err, ok := f.failleft["activate"]; ok {
		return false, err
	}
	if f.active[successor] {
		return true, nil
	}
	f.active[successor] = true
	return false, nil
}

func openReq() seasondomain.ManifestRequest {
	return seasondomain.ManifestRequest{
		ID: "temporada-2", Ordinal: 2,
		StartsAt:       time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC).Add(7776000 * time.Second),
		CharterVersion: "v3", PolicyRef: "politica-inicial",
		InitialMonarch: "fundadora", Regent: "regente-tecnica",
	}
}

func TestOpenerOpensOnceAndReplays(t *testing.T) {
	ctx, cancel := closeCtx()
	defer cancel()
	store := newFakeOpenerStore()
	calls := map[string]int{}
	genesis := func(_ context.Context, season, key string) (bool, error) {
		calls[season+"/"+key]++
		return calls[season+"/"+key] > 1, nil
	}
	opener := &Opener{Store: store, Genesis: genesis}
	first, err := opener.OpenNext(ctx, "temporada-1", openReq(), "genesis-2")
	if err != nil {
		t.Fatalf("OpenNext: %v", err)
	}
	if first.GenesisReplayed || first.ActiveReplayed {
		t.Fatalf("first open = %+v, want fresh effects", first)
	}
	second, err := opener.OpenNext(ctx, "temporada-1", openReq(), "genesis-2")
	if err != nil {
		t.Fatalf("second OpenNext: %v", err)
	}
	if !second.ArchiveReplayed || !second.PreparedReplayed || !second.GenesisReplayed || !second.ActiveReplayed {
		t.Fatalf("second open = %+v, want every step replayed: duas aberturas criam uma Genesis/ACTIVE", second)
	}
}

func TestOpenerBlockedKeepsCustody(t *testing.T) {
	ctx, cancel := closeCtx()
	defer cancel()
	store := newFakeOpenerStore()
	store.failleft["archive"] = seasondomain.ErrCloseBlocked
	opener := &Opener{Store: store, Genesis: func(context.Context, string, string) (bool, error) {
		return false, nil
	}}
	if _, err := opener.OpenNext(ctx, "temporada-1", openReq(), "genesis-2"); !errors.Is(err, seasondomain.ErrCloseBlocked) {
		t.Fatalf("blocked open = %v, want ErrCloseBlocked: sem selo não há sucessora", err)
	}
	if len(store.prepared) != 0 || len(store.active) != 0 {
		t.Fatal("blocked opening wrote successor rows: BLOCKED move nada")
	}
}

func TestOpenerCrashResumesWithoutCarry(t *testing.T) {
	ctx, cancel := closeCtx()
	defer cancel()
	store := newFakeOpenerStore()
	store.failleft["activate"] = errors.New("crash antes do commit")
	genesisCalls := 0
	genesis := func(_ context.Context, _, _ string) (bool, error) {
		genesisCalls++
		return genesisCalls > 1, nil
	}
	opener := &Opener{Store: store, Genesis: genesis}
	if _, err := opener.OpenNext(ctx, "temporada-1", openReq(), "genesis-2"); err == nil {
		t.Fatal("crashed open passed: crash entre arquivo/Genesis/ativação recupera, nunca duplica")
	}
	delete(store.failleft, "activate")
	resumed, err := opener.OpenNext(ctx, "temporada-1", openReq(), "genesis-2")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumed.ArchiveReplayed || !resumed.PreparedReplayed || !resumed.GenesisReplayed {
		t.Fatalf("resume = %+v, want archive/prepared/genesis replayed with active fresh", resumed)
	}
	if resumed.ActiveReplayed {
		t.Fatalf("resume = %+v, want active fresh after crash", resumed)
	}
}
