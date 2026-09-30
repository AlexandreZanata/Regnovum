// Blank imports of every inbound adapter that registers routes, mirroring the
// import list of internal/contract. The header scanner in
// securityheaders_test.go has to walk the same registry the arena binary
// serves; without these imports it would only ever see the health routes and
// would pass while scanning almost nothing.
package securityheaders_test

import (
	_ "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/html"
	_ "github.com/AlexandreZanata/Regnovum/internal/arenas/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/arguments/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/billing/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/html"
	_ "github.com/AlexandreZanata/Regnovum/internal/identity/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/jobs/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/moderation/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/persuasion/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/positions/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/profiles/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/transparency/adapters/http"
	_ "github.com/AlexandreZanata/Regnovum/internal/wallet/adapters/http"
)
