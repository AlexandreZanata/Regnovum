// Package exportjson renders the versioned personal export document with
// the standard library JSON encoder. It implements the profiles application
// encoder port, keeping serialization out of the domain and application
// layers (the architecture gate forbids encoding/json there).
package exportjson

import (
	"encoding/json"

	"github.com/AlexandreZanata/Regnovum/internal/profiles/application"
)

// Encoder renders personal export documents as canonical JSON bytes.
type Encoder struct{}

var _ application.PersonalExportEncoder = (*Encoder)(nil)

// NewEncoder creates a personal export encoder.
func NewEncoder() *Encoder {
	return &Encoder{}
}

// EncodePersonalExport renders the exact bytes that will be stored, hashed
// and served. The application document declares its JSON field order, so the
// same document always encodes identically.
func (*Encoder) EncodePersonalExport(document application.PersonalExportDocument) ([]byte, error) {
	return json.Marshal(document)
}
