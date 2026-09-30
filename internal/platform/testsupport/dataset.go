package testsupport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	arenasdomain "github.com/AlexandreZanata/Regnovum/internal/arenas/domain"
	argumentsdomain "github.com/AlexandreZanata/Regnovum/internal/arguments/domain"
	identitydomain "github.com/AlexandreZanata/Regnovum/internal/identity/domain"
	"github.com/AlexandreZanata/Regnovum/internal/platform/text"
	positionsdomain "github.com/AlexandreZanata/Regnovum/internal/positions/domain"
)

// A dataset is the seeded graph a suite loads before it runs (P22-T04): the
// accounts that can sign in, the Arenas they published, the positions they
// confirmed and the arguments they paid for, all of it a pure function of one
// seed and one profile.
//
// Four rules shape it, and each one is proved by a test here rather than
// promised in this comment:
//
//   - **One seed, one dataset.** Every identifier, address, slug, statement,
//     instant and content is derived from the registered seed. The same seed
//     builds byte-identical canonical JSON and therefore the same checksum; two
//     different seeds build two datasets that share no identifier, address or
//     slug, so both can be loaded side by side.
//   - **Valid by default.** Every value is judged by the product's own domain
//     before it enters the dataset — ParseEmail, ParseSlug, ParseStatement,
//     ParseLanguage, ParsePosition, ParseRelation, ParseContent with the
//     delivered UAX #29 counter — so a dataset cannot carry a row the product
//     would refuse.
//   - **Relations are closed.** Positions point at an account and an Arena of
//     the dataset, and every reply points at a root argument of its own Arena
//     that appears earlier. There is no dangling reference to resolve or to
//     ignore.
//   - **No real person is in the bytes.** Every address is inside a reserved
//     example domain, every source cites example.test, and SensitiveFindings
//     refuses the dataset whose bytes carry a credential, a public address or
//     an address outside the reserved domains (P22-T04 forbids real PII, and
//     the rule is enforced by a detector, not by intention).
//
// The builder is the source of the seed, the stream and the instants, so a
// dataset never reads the wall clock and never shares state with another.
//
// What a dataset deliberately does not carry: moderation cases, billing passes
// and job payloads. Those rows are decisions of their own — a purchase, a
// report, a leased job — and a synthetic graph that invented them would be
// asserting product states no seed can justify. The suites that exercise them
// compose the nouns from the builders of this package instead.

// Profile names one of the four synthetic datasets. The profiles differ in
// purpose, in size and in the languages of their content, never in validity:
// every profile is loaded the same way and passes the same detector.
type Profile string

const (
	// ProfileSmall is the minimal complete journey: three accounts, two
	// Arenas, and every account participating in both. It is the profile a
	// test asserts against by hand.
	ProfileSmall Profile = "small"
	// ProfileConcurrent is contention: two Arenas every one of twelve
	// accounts takes a position in, which is what a suite that runs the same
	// rows in parallel needs to have something to contend on.
	ProfileConcurrent Profile = "concurrent"
	// ProfileInternational is the same journey published in both content
	// languages the product supports, alternating by Arena, with statements,
	// arguments and sources written in the language of the Arena they belong
	// to.
	ProfileInternational Profile = "international"
	// ProfileLoad is the largest profile: it has enough Arenas, positions
	// and arguments for the capacity workload to read a cold cache and a hot
	// one, and enough accounts to sign in with.
	ProfileLoad Profile = "load"
)

// datasetProfiles is the closed set of profiles, in the order the reports
// render them.
var datasetProfiles = []Profile{ProfileSmall, ProfileConcurrent, ProfileInternational, ProfileLoad}

// Profiles answers every dataset profile in canonical order.
func Profiles() []Profile {
	return append([]Profile(nil), datasetProfiles...)
}

// ParseProfile resolves a profile name. The name is what a suite selects its
// dataset with (an environment variable, a test parameter), so an unknown one
// is refused instead of defaulting to a dataset nobody asked for.
func ParseProfile(raw string) (Profile, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	for _, candidate := range datasetProfiles {
		if Profile(trimmed) == candidate {
			return candidate, nil
		}
	}
	names := make([]string, 0, len(datasetProfiles))
	for _, candidate := range datasetProfiles {
		names = append(names, string(candidate))
	}
	return "", fmt.Errorf("testsupport: %q is not a dataset profile (want one of %s)", raw, strings.Join(names, ", "))
}

// datasetCategories are the editorial categories the migrations seed. A
// dataset cycles through them instead of inventing a vocabulary the foreign
// key of app.arenas would refuse.
var datasetCategories = []string{"technology", "science", "philosophy", "politics", "economics", "health"}

// datasetStatements holds one synthetic statement per content language. Both
// satisfy the versioned statement policy and neither debates anything real.
var datasetStatements = map[string]string{
	arenasdomain.LanguagePortuguese: "Cenário sintético de teste: o debate público melhora quando as fontes podem ser verificadas.",
	arenasdomain.LanguageEnglish:    "Synthetic test scenario: public debate improves when its sources can be verified.",
}

// datasetArguments holds one synthetic argument per content language, written
// in the language of the Arena that carries it.
var datasetArguments = map[string]string{
	arenasdomain.LanguagePortuguese: "A fonte citada foi confrontada com o documento original antes da publicação deste argumento.",
	arenasdomain.LanguageEnglish:    "The cited source was checked against the original document before this argument was published.",
}

// datasetSources holds the cited source per content language. The address is
// inside the reserved documentation domain, so no synthetic argument points a
// reader at a real page.
var datasetSources = map[string]struct {
	URL         string
	Description string
}{
	arenasdomain.LanguagePortuguese: {
		URL:         "https://example.test/fonte",
		Description: "Documento sintético usado apenas por datasets de teste.",
	},
	arenasdomain.LanguageEnglish: {
		URL:         "https://example.test/source",
		Description: "Synthetic document used only by test datasets.",
	},
}

// datasetPositions is the closed cycle of positions a dataset confirms. The
// vocabulary belongs to the product; the cycle is what makes every profile
// carry participants on both sides of a statement and not only agreement.
var datasetPositions = []string{
	positionsdomain.PositionAgree,
	positionsdomain.PositionDisagree,
	positionsdomain.PositionUndecided,
}

// datasetEmailDomain is the reserved domain every synthetic account lives in.
// example.test is not registrable, so no dataset address can belong to a
// person (RFC 6761).
const datasetEmailDomain = "example.test"

// datasetInkSurplus is the INK credited to every synthetic account on top of
// what its own arguments cost. A dataset whose balances ended at exactly zero
// would describe a wallet that can never spend again; the surplus keeps the
// wallet read by the capacity workload a wallet with a balance.
const datasetInkSurplus int64 = 1000

// datasetPasswordPrefix is the readable half of every synthetic credential. The
// stream appends a token, so no two profiles and no two seeds share one.
const datasetPasswordPrefix = "synthetic-dataset-password"

// profileShape is the declared size and material of one profile.
type profileShape struct {
	purpose              string
	credentials          int
	accounts             int
	arenas               int
	participantsPerArena int
	rootsPerArena        int
	repliesPerArena      int
	languages            []string
}

// profileShapes is the profile table. Sizes are deliberately modest: a dataset
// is loaded inside the test that needs it, and the load profile is the largest
// of them without being so large that a suite pays for it twice.
var profileShapes = map[Profile]profileShape{
	ProfileSmall: {
		purpose:              "the minimal complete journey",
		credentials:          3,
		accounts:             3,
		arenas:               2,
		participantsPerArena: 3,
		rootsPerArena:        2,
		repliesPerArena:      1,
		languages:            []string{arenasdomain.LanguagePortuguese},
	},
	ProfileConcurrent: {
		purpose:              "contention on shared rows",
		credentials:          2,
		accounts:             12,
		arenas:               2,
		participantsPerArena: 12,
		rootsPerArena:        4,
		repliesPerArena:      1,
		languages:            []string{arenasdomain.LanguagePortuguese},
	},
	ProfileInternational: {
		purpose:              "both content languages",
		credentials:          4,
		accounts:             4,
		arenas:               4,
		participantsPerArena: 4,
		rootsPerArena:        2,
		repliesPerArena:      1,
		languages: []string{
			arenasdomain.LanguagePortuguese,
			arenasdomain.LanguageEnglish,
		},
	},
	ProfileLoad: {
		purpose:              "capacity workload material",
		credentials:          2,
		accounts:             12,
		arenas:               24,
		participantsPerArena: 6,
		rootsPerArena:        3,
		repliesPerArena:      1,
		languages: []string{
			arenasdomain.LanguagePortuguese,
			arenasdomain.LanguageEnglish,
		},
	},
}

// shape answers the declared shape of a profile.
func (p Profile) shape() (profileShape, error) {
	spec, found := profileShapes[p]
	if !found {
		return profileShape{}, fmt.Errorf("testsupport: %q is not a dataset profile", string(p))
	}
	return spec, nil
}

// CredentialRecord is one synthetic password. Accounts share credentials by
// index, because a suite that signs in has one or two known logins, not one per
// row — and hashing one password per account would buy nothing but time.
type CredentialRecord struct {
	Index    int    `json:"index"`
	Password string `json:"password"`
}

// AccountRecord is one registered, confirmed account of the dataset.
type AccountRecord struct {
	Index           int       `json:"index"`
	ID              string    `json:"id"`
	Email           string    `json:"email"`
	CredentialIndex int       `json:"credential_index"`
	VerifiedAt      time.Time `json:"verified_at"`
	// Ink is what the dataset credits this account: the cost of its own
	// arguments plus the surplus. A ledger that charges the publications is a
	// relation the dataset declares and the load proves.
	Ink int64 `json:"ink"`
}

// ArenaRecord is one published Arena of the dataset.
type ArenaRecord struct {
	Index       int       `json:"index"`
	ID          string    `json:"id"`
	Creator     int       `json:"creator"`
	Slug        string    `json:"slug"`
	Statement   string    `json:"statement"`
	Category    string    `json:"category"`
	Language    string    `json:"language"`
	PublishedAt time.Time `json:"published_at"`
}

// PositionRecord is one confirmed initial position. It points at the account
// and the Arena by index, so the reference is into the dataset and not a value
// that happens to look right.
type PositionRecord struct {
	Arena       int       `json:"arena"`
	Account     int       `json:"account"`
	Position    string    `json:"position"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

// ArgumentRecord is one publication the dataset asks for. Root arguments carry
// no parent; a reply carries the index of a root of its own Arena.
type ArgumentRecord struct {
	Arena      int    `json:"arena"`
	Author     int    `json:"author"`
	Parent     int    `json:"parent"`
	Relation   string `json:"relation"`
	Content    string `json:"content"`
	SourceURL  string `json:"source_url"`
	SourceNote string `json:"source_note"`
	Key        string `json:"key"`
	InkCost    int64  `json:"ink_cost"`
}

// Dataset is one seeded graph. It is the artifact a suite loads and the
// document a report carries: the checksum of Canonical() is what "the same seed
// produces the same dataset" is measured by.
type Dataset struct {
	Profile     Profile            `json:"profile"`
	Seed        int64              `json:"seed"`
	Credentials []CredentialRecord `json:"credentials"`
	Accounts    []AccountRecord    `json:"accounts"`
	Arenas      []ArenaRecord      `json:"arenas"`
	Positions   []PositionRecord   `json:"positions"`
	Arguments   []ArgumentRecord   `json:"arguments"`
}

// Manifest is the summary of a dataset: what it is, which seed built it, its
// checksum and its counts. It is what a workload or an evidence artifact
// records instead of the dataset itself.
type Manifest struct {
	Profile     Profile `json:"profile"`
	Seed        int64   `json:"seed"`
	Checksum    string  `json:"checksum"`
	Accounts    int     `json:"accounts"`
	Arenas      int     `json:"arenas"`
	Positions   int     `json:"positions"`
	Arguments   int     `json:"arguments"`
	InkCredited int64   `json:"ink_credited"`
	InkCharged  int64   `json:"ink_charged"`
}

// String renders the manifest as one JSON line, which is the form a workload
// records and a log carries.
func (m Manifest) String() string {
	encoded, err := json.Marshal(m)
	if err != nil {
		// The manifest is a struct of scalars: this cannot fail, and returning
		// a message instead of panicking keeps a log line from taking a suite
		// down.
		return fmt.Sprintf("manifest(profile=%s seed=%d checksum=%s)", m.Profile, m.Seed, m.Checksum)
	}
	return string(encoded)
}

// Dataset builds the dataset of a profile for the scenario of this builder.
// Every value is parsed by the domain it belongs to, and every reference is
// resolved while the dataset is built, so the outcome is either a complete,
// valid graph or the error naming the value the domain refused.
func (b *Builder) Dataset(profile Profile) (*Dataset, error) {
	b.t.Helper()

	spec, err := profile.shape()
	if err != nil {
		return nil, err
	}
	base := b.Now()

	dataset := &Dataset{
		Profile:     profile,
		Seed:        b.Seed(),
		Credentials: make([]CredentialRecord, 0, spec.credentials),
		Accounts:    make([]AccountRecord, 0, spec.accounts),
		Arenas:      make([]ArenaRecord, 0, spec.arenas),
		Positions:   make([]PositionRecord, 0, spec.arenas*spec.participantsPerArena),
		Arguments:   make([]ArgumentRecord, 0, spec.arenas*(spec.rootsPerArena+spec.repliesPerArena)),
	}

	for index := 0; index < spec.credentials; index++ {
		dataset.Credentials = append(dataset.Credentials, CredentialRecord{
			Index:    index,
			Password: fmt.Sprintf("%s-%s-%d-%s", datasetPasswordPrefix, profile, index, b.shortToken()),
		})
	}

	for index := 0; index < spec.accounts; index++ {
		address := fmt.Sprintf("dataset-%s-%02d-%s@%s", profile, index, b.shortToken(), datasetEmailDomain)
		email, parseErr := identitydomain.ParseEmail(address)
		if parseErr != nil {
			return nil, fmt.Errorf("testsupport: dataset %s: account %d: %w", profile, index, parseErr)
		}
		dataset.Accounts = append(dataset.Accounts, AccountRecord{
			Index:           index,
			ID:              b.Identifier(),
			Email:           strings.ToLower(email.String()),
			CredentialIndex: index % spec.credentials,
			VerifiedAt:      base.Add(time.Duration(index) * time.Minute).UTC(),
			Ink:             datasetInkSurplus,
		})
	}

	policy := arenasdomain.DefaultStatementPolicy()
	for index := 0; index < spec.arenas; index++ {
		languageName := spec.languages[index%len(spec.languages)]
		language, parseErr := arenasdomain.ParseLanguage(languageName)
		if parseErr != nil {
			return nil, fmt.Errorf("testsupport: dataset %s: arena %d: %w", profile, index, parseErr)
		}
		token := b.shortToken()

		statement, parseErr := arenasdomain.ParseStatement(
			fmt.Sprintf("%s [%s-%02d-%s]", datasetStatements[languageName], profile, index, token), policy,
		)
		if parseErr != nil {
			return nil, fmt.Errorf("testsupport: dataset %s: arena %d statement: %w", profile, index, parseErr)
		}
		slug, parseErr := arenasdomain.ParseSlug(fmt.Sprintf("dataset-%s-%02d-%s", profile, index, token))
		if parseErr != nil {
			return nil, fmt.Errorf("testsupport: dataset %s: arena %d slug: %w", profile, index, parseErr)
		}
		category, parseErr := arenasdomain.ParseCategory(datasetCategories[index%len(datasetCategories)])
		if parseErr != nil {
			return nil, fmt.Errorf("testsupport: dataset %s: arena %d category: %w", profile, index, parseErr)
		}

		dataset.Arenas = append(dataset.Arenas, ArenaRecord{
			Index:       index,
			ID:          b.Identifier(),
			Creator:     index % spec.accounts,
			Slug:        slug.String(),
			Statement:   statement.String(),
			Category:    category.String(),
			Language:    language.String(),
			PublishedAt: base.Add(time.Duration(index) * time.Minute).UTC(),
		})
	}

	for index := range dataset.Arenas {
		arena := dataset.Arenas[index]
		for participant := 0; participant < spec.participantsPerArena; participant++ {
			position, parseErr := positionsdomain.ParsePosition(datasetPositions[participant%len(datasetPositions)])
			if parseErr != nil {
				return nil, fmt.Errorf("testsupport: dataset %s: position: %w", profile, parseErr)
			}
			dataset.Positions = append(dataset.Positions, PositionRecord{
				Arena:       arena.Index,
				Account:     (arena.Index*3 + participant) % spec.accounts,
				Position:    position.String(),
				ConfirmedAt: base.Add(time.Duration(index+participant+1) * time.Minute).UTC(),
			})
		}
	}

	for index := range dataset.Arenas {
		arena := dataset.Arenas[index]
		language := arena.Language
		source := datasetSources[language]
		firstRoot := len(dataset.Arguments)

		for root := 0; root < spec.rootsPerArena; root++ {
			if err := b.appendArgument(dataset, arena.Index, (arena.Index+root)%spec.accounts, -1, language, source.URL, source.Description); err != nil {
				return nil, err
			}
		}
		for reply := 0; reply < spec.repliesPerArena; reply++ {
			if err := b.appendArgument(dataset, arena.Index, (arena.Index+reply+1)%spec.accounts, firstRoot+(reply%spec.rootsPerArena), language, source.URL, source.Description); err != nil {
				return nil, err
			}
		}
	}

	// The credit is the cost of the account's own arguments plus the surplus,
	// so the balance left after the load is a relation the verification can
	// state in numbers instead of in prose.
	for _, argument := range dataset.Arguments {
		dataset.Accounts[argument.Author].Ink += argument.InkCost
	}

	return dataset, nil
}

// appendArgument validates one argument through the domain and appends the
// record. The grapheme count answered by the delivered counter is the cost the
// publication charges, so the dataset carries the ledger relation and not only
// the text.
func (b *Builder) appendArgument(
	dataset *Dataset,
	arena int,
	author int,
	parent int,
	language string,
	sourceURL string,
	sourceNote string,
) error {
	b.t.Helper()

	profile := dataset.Profile
	token := b.shortToken()
	content, err := argumentsdomain.ParseContent(
		fmt.Sprintf("%s [%s-%03d-%s]", datasetArguments[language], profile, len(dataset.Arguments), token),
		text.GraphemeCount,
	)
	if err != nil {
		return fmt.Errorf("testsupport: dataset %s: argument %d content: %w", profile, len(dataset.Arguments), err)
	}
	relation, err := argumentsdomain.ParseRelation(
		[]string{argumentsdomain.RelationSupport, argumentsdomain.RelationOppose, argumentsdomain.RelationContext}[len(dataset.Arguments)%3],
	)
	if err != nil {
		return fmt.Errorf("testsupport: dataset %s: argument %d relation: %w", profile, len(dataset.Arguments), err)
	}
	if _, err := argumentsdomain.ParseSource(sourceURL, sourceNote); err != nil {
		return fmt.Errorf("testsupport: dataset %s: argument %d source: %w", profile, len(dataset.Arguments), err)
	}
	key, err := argumentsdomain.ParseIdempotencyKey(fmt.Sprintf("dataset-%s-%03d-%s", profile, len(dataset.Arguments), token))
	if err != nil {
		return fmt.Errorf("testsupport: dataset %s: argument %d idempotency key: %w", profile, len(dataset.Arguments), err)
	}
	if parent >= len(dataset.Arguments) {
		return fmt.Errorf("testsupport: dataset %s: argument %d replies to %d, which is not in the dataset yet", profile, len(dataset.Arguments), parent)
	}
	if parent >= 0 && dataset.Arguments[parent].Arena != arena {
		return fmt.Errorf("testsupport: dataset %s: argument %d replies across Arenas (%d, %d)", profile, len(dataset.Arguments), arena, dataset.Arguments[parent].Arena)
	}

	dataset.Arguments = append(dataset.Arguments, ArgumentRecord{
		Arena:      arena,
		Author:     author,
		Parent:     parent,
		Relation:   relation.String(),
		Content:    content.String(),
		SourceURL:  sourceURL,
		SourceNote: sourceNote,
		Key:        key.String(),
		InkCost:    int64(content.GraphemeCost()),
	})
	return nil
}

// Canonical renders the dataset as the bytes its checksum is taken over. The
// encoding is plain JSON of the structure declared above: field order is the
// declaration, slices keep their construction order and no map is involved, so
// the same dataset always renders the same bytes.
func (d *Dataset) Canonical() ([]byte, error) {
	return json.Marshal(d)
}

// Checksum answers the SHA-256 of the canonical rendering, hex encoded. It is
// the value a suite records and compares: the same seed and profile answer the
// same checksum on any machine, and two seeds answer two different ones.
func (d *Dataset) Checksum() string {
	encoded, err := d.Canonical()
	if err != nil {
		// The dataset is a struct of scalars and strings: this cannot fail.
		return "unrenderable"
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Manifest answers the summary of the dataset, checksum included.
func (d *Dataset) Manifest() Manifest {
	manifest := Manifest{
		Profile:   d.Profile,
		Seed:      d.Seed,
		Checksum:  d.Checksum(),
		Accounts:  len(d.Accounts),
		Arenas:    len(d.Arenas),
		Positions: len(d.Positions),
		Arguments: len(d.Arguments),
	}
	for _, account := range d.Accounts {
		manifest.InkCredited += account.Ink
	}
	for _, argument := range d.Arguments {
		manifest.InkCharged += argument.InkCost
	}
	return manifest
}
