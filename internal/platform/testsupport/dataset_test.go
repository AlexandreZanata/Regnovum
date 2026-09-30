package testsupport

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// The dataset tests. Four claims are measured here rather than promised by the
// package comment: the same seed answers the same bytes and the same checksum;
// two seeds answer two datasets that share nothing; every relation of a dataset
// resolves inside it; and no dataset carries a value the detector refuses.

// datasetFor builds the dataset of one profile from an explicit seed, failing
// the test when the builder refuses it.
func datasetFor(t *testing.T, profile Profile, seed int64) *Dataset {
	t.Helper()
	dataset, err := NewWithSeed(t, seed).Dataset(profile)
	if err != nil {
		t.Fatalf("Dataset(%s) error = %v", profile, err)
	}
	return dataset
}

// TestTheSameSeedBuildsTheSameDataset is the first validation of the task: the
// same seed produces the same checksum. It is asserted on the canonical bytes
// and not only on the digest, because two different documents could share a
// digest only by breaking SHA-256, while equal bytes are the property the
// suites that load a dataset depend on.
func TestTheSameSeedBuildsTheSameDataset(t *testing.T) {
	for _, profile := range Profiles() {
		t.Run(string(profile), func(t *testing.T) {
			first := datasetFor(t, profile, 20260923)
			second := datasetFor(t, profile, 20260923)

			firstBytes, err := first.Canonical()
			if err != nil {
				t.Fatalf("Canonical() error = %v", err)
			}
			secondBytes, err := second.Canonical()
			if err != nil {
				t.Fatalf("Canonical() error = %v", err)
			}
			if !bytes.Equal(firstBytes, secondBytes) {
				t.Fatalf("the same seed rendered %d bytes and %d bytes", len(firstBytes), len(secondBytes))
			}
			if first.Checksum() != second.Checksum() {
				t.Fatalf("Checksum() = %s and %s", first.Checksum(), second.Checksum())
			}

			manifest := first.Manifest()
			if manifest.Checksum != first.Checksum() {
				t.Fatalf("the manifest reports checksum %s, the dataset answers %s", manifest.Checksum, first.Checksum())
			}
			if manifest.Seed != 20260923 || manifest.Profile != profile {
				t.Fatalf("the manifest reports %s, want profile %s", manifest.String(), profile)
			}
			if manifest.Accounts != len(first.Accounts) || manifest.Arenas != len(first.Arenas) ||
				manifest.Positions != len(first.Positions) || manifest.Arguments != len(first.Arguments) {
				t.Fatalf("the manifest counts do not report the dataset: %s", manifest.String())
			}
			if len(first.Credentials) == 0 || len(first.Accounts) == 0 || len(first.Arenas) == 0 ||
				len(first.Positions) == 0 || len(first.Arguments) == 0 {
				t.Fatalf("the %s profile is missing a noun: %s", profile, manifest.String())
			}
		})
	}
}

// TestAnotherSeedIsAnotherDataset measures the second validation: two seeds do
// not collide. Distinct checksums are the weak half; the strong half is that no
// identifier, address or slug appears in both, which is what lets two datasets
// be loaded side by side without the schema refusing the second one.
func TestAnotherSeedIsAnotherDataset(t *testing.T) {
	seeds := []int64{20260923, 20260924, 424242}
	for _, profile := range Profiles() {
		t.Run(string(profile), func(t *testing.T) {
			checksums := map[string]int64{}
			identifiers := map[string]int64{}
			for _, seed := range seeds {
				dataset := datasetFor(t, profile, seed)
				checksum := dataset.Checksum()
				if other, found := checksums[checksum]; found {
					t.Fatalf("seeds %d and %d answer the same checksum %s", other, seed, checksum)
				}
				checksums[checksum] = seed

				for _, value := range datasetValues(dataset) {
					if other, found := identifiers[value]; found {
						t.Fatalf("seeds %d and %d share %q", other, seed, value)
					}
					identifiers[value] = seed
				}
			}
		})
	}
}

// datasetValues answers every value that identifies a row of the dataset, which
// is the set two seeds may not share.
func datasetValues(dataset *Dataset) []string {
	values := make([]string, 0, len(dataset.Accounts)*2+len(dataset.Arenas))
	for _, account := range dataset.Accounts {
		values = append(values, account.ID, account.Email)
	}
	for _, arena := range dataset.Arenas {
		values = append(values, arena.ID, arena.Slug)
	}
	for _, argument := range dataset.Arguments {
		values = append(values, argument.Key)
	}
	return values
}

// TestEveryDatasetRelationResolves is the referential half of the task: the
// dataset preserves its relations. Every index points inside the dataset, every
// reply points at a root of its own Arena that appears earlier, every row is
// identified once, and the credited INK is exactly the cost of the account's own
// arguments plus the declared surplus.
func TestEveryDatasetRelationResolves(t *testing.T) {
	for _, profile := range Profiles() {
		t.Run(string(profile), func(t *testing.T) {
			dataset := datasetFor(t, profile, 11)

			seen := map[string]string{}
			for _, account := range dataset.Accounts {
				if account.ID == "" || account.Email == "" {
					t.Fatalf("account %d is missing its identity", account.Index)
				}
				if account.CredentialIndex < 0 || account.CredentialIndex >= len(dataset.Credentials) {
					t.Fatalf("account %d credits credential %d, and there are %d", account.Index, account.CredentialIndex, len(dataset.Credentials))
				}
				if address := account.Email; !strings.HasSuffix(address, "@"+datasetEmailDomain) {
					t.Fatalf("account %d carries address %q outside %s", account.Index, address, datasetEmailDomain)
				}
				for _, value := range []string{account.ID, account.Email} {
					if owner, repeated := seen[value]; repeated {
						t.Fatalf("%q is both %s and account %d", value, owner, account.Index)
					}
					seen[value] = fmt.Sprintf("account %d", account.Index)
				}
			}

			for _, arena := range dataset.Arenas {
				if arena.Creator < 0 || arena.Creator >= len(dataset.Accounts) {
					t.Fatalf("arena %d is owned by account %d, and there are %d", arena.Index, arena.Creator, len(dataset.Accounts))
				}
				for _, value := range []string{arena.ID, arena.Slug} {
					if owner, repeated := seen[value]; repeated {
						t.Fatalf("%q is both %s and arena %d", value, owner, arena.Index)
					}
					seen[value] = fmt.Sprintf("arena %d", arena.Index)
				}
			}

			confirmed := map[string]bool{}
			for _, position := range dataset.Positions {
				if position.Arena < 0 || position.Arena >= len(dataset.Arenas) {
					t.Fatalf("a position points at arena %d, and there are %d", position.Arena, len(dataset.Arenas))
				}
				if position.Account < 0 || position.Account >= len(dataset.Accounts) {
					t.Fatalf("a position points at account %d, and there are %d", position.Account, len(dataset.Accounts))
				}
				key := fmt.Sprintf("%d/%d", position.Arena, position.Account)
				if confirmed[key] {
					t.Fatalf("arena %d has two confirmed positions of account %d", position.Arena, position.Account)
				}
				confirmed[key] = true
			}

			spent := map[int]int64{}
			for index, argument := range dataset.Arguments {
				if argument.Arena < 0 || argument.Arena >= len(dataset.Arenas) {
					t.Fatalf("argument %d points at arena %d, and there are %d", index, argument.Arena, len(dataset.Arenas))
				}
				if argument.Author < 0 || argument.Author >= len(dataset.Accounts) {
					t.Fatalf("argument %d is authored by account %d, and there are %d", index, argument.Author, len(dataset.Accounts))
				}
				if argument.InkCost < 1 {
					t.Fatalf("argument %d costs %d INK", index, argument.InkCost)
				}
				if argument.Parent >= index {
					t.Fatalf("argument %d replies to %d, which is not an earlier argument", index, argument.Parent)
				}
				if argument.Parent >= 0 {
					parent := dataset.Arguments[argument.Parent]
					if parent.Arena != argument.Arena {
						t.Fatalf("argument %d replies across Arenas (%d and %d)", index, argument.Arena, parent.Arena)
					}
					if parent.Parent >= 0 {
						t.Fatalf("argument %d replies to %d, which is itself a reply", index, argument.Parent)
					}
				}
				spent[argument.Author] += argument.InkCost
			}

			for _, account := range dataset.Accounts {
				if want := spent[account.Index] + datasetInkSurplus; account.Ink != want {
					t.Fatalf("account %d is credited %d INK and its arguments cost %d", account.Index, account.Ink, want)
				}
			}
		})
	}
}

// TestEveryProfileIsDeclaredAndSelectable covers the profile surface: the four
// profiles are named, selectable by name, and every one of them describes a
// distinct purpose and a distinct size. A profile that answered the same shape
// as another would be a name nobody needs.
func TestEveryProfileIsDeclaredAndSelectable(t *testing.T) {
	if len(Profiles()) != 4 {
		t.Fatalf("Profiles() = %d, want the four of the task", len(Profiles()))
	}

	purposes := map[string]Profile{}
	sizes := map[string]Profile{}
	for _, profile := range Profiles() {
		parsed, err := ParseProfile(string(profile))
		if err != nil {
			t.Fatalf("ParseProfile(%q) error = %v", profile, err)
		}
		if parsed != profile {
			t.Fatalf("ParseProfile(%q) = %s", profile, parsed)
		}
		spaced, err := ParseProfile("  " + strings.ToUpper(string(profile)) + "  ")
		if err != nil || spaced != profile {
			t.Fatalf("ParseProfile with padding and case = %s, %v", spaced, err)
		}

		spec, err := profile.shape()
		if err != nil {
			t.Fatalf("shape(%s) error = %v", profile, err)
		}
		if spec.participantsPerArena > spec.accounts {
			t.Fatalf("profile %s asks %d participants of %d accounts, and one account holds one position per Arena", profile, spec.participantsPerArena, spec.accounts)
		}
		if len(spec.languages) == 0 {
			t.Fatalf("profile %s declares no content language", profile)
		}
		if other, repeated := purposes[spec.purpose]; repeated {
			t.Fatalf("profiles %s and %s share the purpose %q", other, profile, spec.purpose)
		}
		purposes[spec.purpose] = profile

		size := fmt.Sprintf("%d/%d/%d/%d", spec.accounts, spec.arenas, spec.rootsPerArena, spec.repliesPerArena)
		if other, repeated := sizes[size]; repeated {
			t.Fatalf("profiles %s and %s have the same declared size %s", other, profile, size)
		}
		sizes[size] = profile
	}

	for _, unknown := range []string{"", "huge", "pequeno", "smallest"} {
		if parsed, err := ParseProfile(unknown); err == nil {
			t.Fatalf("ParseProfile(%q) = %s, want a refusal", unknown, parsed)
		}
	}
}

// TestTheDatasetsCarryNothingSensitive is the third validation of the task: the
// detector passes. It runs over the canonical bytes of every profile, which is
// the artifact a suite loads — a dataset that failed here would be a dataset
// carrying somebody's address or credential.
func TestTheDatasetsCarryNothingSensitive(t *testing.T) {
	for _, profile := range Profiles() {
		t.Run(string(profile), func(t *testing.T) {
			dataset := datasetFor(t, profile, 20260923)
			findings := dataset.SensitiveFindings()
			if len(findings) != 0 {
				evidence := make([]string, 0, len(findings))
				for _, finding := range findings {
					evidence = append(evidence, finding.String())
				}
				t.Fatalf("the %s dataset carries %d sensitive values: %s", profile, len(findings), strings.Join(evidence, "; "))
			}
		})
	}
}

// TestTheDetectorRefusesARealCredentialOrAddress is the control for the check
// above: a detector that matched nothing would pass every dataset. Each sample
// below is a value that must not appear in a fixture, and every rule of the
// declared vocabulary is exercised by at least one of them, so a rule nobody
// stands in front of cannot be added without failing this test.
func TestTheDetectorRefusesARealCredentialOrAddress(t *testing.T) {
	// Every sample is composed from fragments, deliberately: it has to reach
	// the detector as one credential-shaped value, and this file has to stay
	// free of a literal that a secret scanner reads as a live credential. A
	// sample is evidence about the detector, and the trees of this repository
	// are scanned on push — a synthetic key that a scanner cannot tell from a
	// real one would stop the publication of the dataset that forbids it.
	join := func(parts ...string) string { return strings.Join(parts, "") }
	samples := []struct {
		rule   string
		sample string
	}{
		{rule: "provider-secret", sample: join("key sk_", "live_", "51H8xQ2eZvKYlo2CqRfT7bM9")},
		{rule: "webhook-secret", sample: join("signing whs", "ec_1a2b3c4d5e6f7g8h9i0j")},
		{rule: "cloud-access-key", sample: join("role AK", "IAIOSFODNN7EXAMPLE")},
		{rule: "chat-token", sample: join("bot xox", "b-123456789012-abcdefghijklmnop")},
		{rule: "git-token", sample: join("token gh", "p_0123456789abcdefghijklmnopqrstuvwxyz")},
		{rule: "signed-token", sample: join("claim ey", "JhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.",
			"eyJzdWIiOiIxMjM0NTY3ODkwIn0.", "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U")},
		{rule: "private-key", sample: join("-----BEGIN RSA ", "PRIVATE KEY-----")},
		{rule: "bearer-token", sample: join("Authorization: Bearer ", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "0123456789")},
		{rule: "card-number", sample: join("card 4111", "1111", "1111", "1111")},
		{rule: "public-ip", sample: join("client 8.", "8.", "8.", "8")},
		{rule: "real-email-domain", sample: join("author ana.silva@", "corp.example-business.com")},
	}

	exercised := map[string]bool{}
	for _, sample := range samples {
		findings := SensitiveFindings([]byte(sample.sample))
		found := false
		for _, finding := range findings {
			if finding.Rule == sample.rule {
				found = true
			}
			if strings.Contains(finding.Evidence, sample.sample) {
				t.Fatalf("rule %s republished what it found: %q", finding.Rule, finding.Evidence)
			}
		}
		if !found {
			t.Fatalf("the detector answered %v for %q, want the rule %s", findings, sample.sample, sample.rule)
		}
		exercised[sample.rule] = true
	}

	for _, rule := range SensitiveRules() {
		if !exercised[rule] {
			t.Fatalf("the rule %s matches no sample: a rule nobody exercises is a rule nobody runs", rule)
		}
	}

	// The controls: values a dataset is made of, every one of them accepted.
	accepted := []string{
		"owner-1a2b3c4d@example.test",
		"dataset-load-00-9f8e7d6c@example.test",
		"https://example.test/fonte",
		"client 127.0.0.1",
		"client 10.1.2.3",
		"arena 2026-09",
		"reference 123456789012",
	}
	for _, control := range accepted {
		if findings := SensitiveFindings([]byte(control)); len(findings) != 0 {
			t.Fatalf("the detector refused the control %q: %v", control, findings)
		}
	}
}
