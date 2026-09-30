package application

import (
	"github.com/AlexandreZanata/Regnovum/internal/pricing/domain"
)

// observationsFromInputs validates raw sightings into domain
// observations once for every use case that prices from them: source
// slugs, positive prices, instants and payload provenance.
func observationsFromInputs(inputs []SightingInput) ([]domain.Observation, error) {
	sightings := make([]domain.Observation, 0, len(inputs))
	for _, input := range inputs {
		source, err := domain.ParseSourceID(input.Source)
		if err != nil {
			return nil, err
		}
		sighting, err := domain.NewObservation(source, input.PriceMinor, input.ObservedAt, input.Payload)
		if err != nil {
			return nil, err
		}
		sightings = append(sightings, sighting)
	}
	return sightings, nil
}
