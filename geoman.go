package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IncSW/geoip2"
	"github.com/maxmind/geoipupdate/v4/pkg/geoipupdate"
	"github.com/maxmind/geoipupdate/v4/pkg/geoipupdate/database"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
)

// todo: support geolite updater config from environment
// todo: support specific time-of-day sync
// todo: support asn / city lookup
// todo: additional context in response?
// todo: case-insensitive comparisons?
// todo: break up lookupCountry a bit

type LookupRequest struct {
	SourceIP           string   `json:"source_ip"`
	MinimumConfidence  *uint16  `json:"minimum_confidence,omitempty"`
	WhitelistCountries []string `json:"whitelist_countries"`
}

func (r LookupRequest) MarshalZerologObject(ev *zerolog.Event) {
	ev.Str("source_ip", r.SourceIP)
	if r.MinimumConfidence == nil {
		ev.Uint16("minimum_confidence", 0)
	} else {
		ev.Uint16("minimum_confidence", *r.MinimumConfidence)
	}
	ev.Strs("whitelist_countries", r.WhitelistCountries)
}

type LookupMatch struct {
	GeoNameID    uint32 `json:"geo_name_id"`
	MatchedType  string `json:"matched_type"`
	MatchedValue string `json:"matched_value"`
	Confidence   uint16 `json:"confidence"`
}

func (r LookupMatch) MarshalZerologObject(ev *zerolog.Event) {
	ev.Uint32("geo_name_id", r.GeoNameID)
	ev.Str("matched_type", r.MatchedType)
	ev.Str("mathed_value", r.MatchedValue)
	ev.Uint16("confidence", r.Confidence)
}

type LookupResult []LookupMatch

func (r LookupResult) MarshalZerologArray(arr *zerolog.Array) {
	for _, m := range r {
		arr.Object(m)
	}
}

type LookupError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Err     error  `json:"error"`
}

func (e LookupError) MarshalZerologObject(ev *zerolog.Event) {
	ev.Int("code", e.Code)
	ev.Str("message", e.Message)
	ev.Err(e.Err)
}

func (e LookupError) Error() string {
	return fmt.Sprintf("code=%d; message=%q; err=%v", e.Code, e.Message, e.Err)
}

type geoman struct {
	log             zerolog.Logger
	confFile        string
	dbDir           string
	updateInterval  string
	updateIntervalD time.Duration

	gconfig *geoipupdate.Config
	gclient *http.Client

	reader   *geoip2.CountryReader
	readerMu sync.RWMutex
}

func (g *geoman) run(errc chan<- error) {
	var (
		err error
	)

	g.log.Info().Msg("Starting GeoLite manager...")

	if g.updateIntervalD, err = time.ParseDuration(g.updateInterval); err != nil {
		errc <- fmt.Errorf("provided update interval value %q is not valid %T: %w", g.updateInterval, g.updateIntervalD, err)
		return
	}

	g.log.Debug().
		Str("geolite-conf", g.confFile).
		Str("geolite-db", g.dbDir).
		Str("interval", g.updateIntervalD.String()).
		Msg("Building updater config")

	if g.gconfig, err = geoipupdate.NewConfig(g.confFile, "", g.dbDir, true); err != nil {
		errc <- fmt.Errorf("error construting geoipupdate config: %w", err)
		return
	}

	g.gclient = geoipupdate.NewClient(g.gconfig)

	g.log.Info().Msg("Checking for db files...")
	for _, editionID := range g.gconfig.EditionIDs {
		dbFile := g.editionFilepath(editionID)
		if _, err := os.Stat(dbFile); err != nil {
			g.log.Warn().Str("db-file", dbFile).Msg("DB missing on boot, downloading...")
			if err := g.download(editionID); err != nil {
				errc <- err
				return
			}
		}
	}

	// todo: handle non-country readers.
	if g.reader, err = g.buildCountryReader(); err != nil {
		g.log.Error().Err(err).Msg("Error opening db")
	}

	g.log.Debug().Msg("GeoLite manager initialization completed")

	errc <- g.handle()
}

func (g *geoman) editionFilepath(editionID string) string {
	return filepath.Join(g.gconfig.DatabaseDirectory, fmt.Sprintf("%s.mmdb", editionID))
}

func (g *geoman) download(editionIDs ...string) error {
	dbReader := database.NewHTTPDatabaseReader(g.gclient, g.gconfig)

	for _, editionID := range editionIDs {
		filename, err := geoipupdate.GetFilename(g.gconfig, editionID, g.gclient)
		if err != nil {
			return errors.Wrapf(err, "error retrieving filename for %s", editionID)
		}
		filePath := filepath.Join(g.gconfig.DatabaseDirectory, filename)
		dbWriter, err := database.NewLocalFileDatabaseWriter(filePath, g.gconfig.LockFile, g.gconfig.Verbose)
		if err != nil {
			return errors.Wrapf(err, "error creating database writer for %s", editionID)
		}
		if err := dbReader.Get(dbWriter, editionID); err != nil {
			return errors.WithMessagef(err, "error while getting database for %s", editionID)
		}
	}
	return nil
}

func (g *geoman) buildCountryReader() (*geoip2.CountryReader, error) {
	return geoip2.NewCountryReaderFromFile(g.editionFilepath("GeoLite2-Country"))
}

func (g *geoman) handle() error {
	var (
		tmpReader *geoip2.CountryReader
		err       error

		updateTimer = time.NewTicker(g.updateIntervalD)
	)

	g.log.Debug().Msg("Entering GeoLite 2 manager handler routine...")

	for range updateTimer.C {
		log := g.log.With().Str("action", "update").Logger()
		log.Info().Msg("Running geo ip update...")
		if err = g.download(g.gconfig.EditionIDs...); err != nil {
			log.Error().Err(err).Msg("Error updating GeoLite 2 databases!")
		} else {
			log.Info().Msg("GeoLite 2 databases updated successfully")
			if tmpReader, err = g.buildCountryReader(); err != nil {
				log.Error().Err(err).Msg("Error reconstructing country reader")
			} else {
				log.Info().Msg("Country reader reconstructed")
				g.readerMu.Lock()
				g.reader = tmpReader
				g.readerMu.Unlock()
			}
		}
		updateTimer.Reset(g.updateIntervalD)
	}

	return nil
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	tmp := make(map[string]struct{})

	for _, iv := range in {
		if _, ok := tmp[iv]; !ok {
			tmp[iv] = struct{}{}
		}
	}
	out := make([]string, 0)
	for k := range tmp {
		out = append(out, k)
	}
	return out
}

func (g *geoman) lookupCountry(req LookupRequest) (LookupResult, error) {
	g.readerMu.RLock()
	defer g.readerMu.RUnlock()

	var (
		ip     net.IP
		lookup *geoip2.CountryResult
		err    error

		res = make(LookupResult, 0)
	)

	g.log.Info().Object("request", req).Msg("Handling lookup request...")

	if req.SourceIP == "" {
		return nil, LookupError{
			Code:    http.StatusBadRequest,
			Message: "\"source_ip\" must be provided",
		}
	}

	if len(req.WhitelistCountries) == 0 {
		return nil, LookupError{
			Code:    http.StatusBadRequest,
			Message: "\"whitelist_countries\" must have at least one entry",
		}
	}

	if ip = net.ParseIP(req.SourceIP); ip == nil {
		return nil, LookupError{
			Code:    http.StatusBadRequest,
			Message: "Invalid \"source_ip\" value provided",
		}
	}

	if lookup, err = g.reader.Lookup(ip); err != nil {
		return nil, LookupError{
			Code:    http.StatusInternalServerError,
			Message: "Error looking up IP",
			Err:     err,
		}
	}

	g.log.Debug().Interface("matched", lookup.Country).Msg("match result")

	for _, target := range uniqueStrings(req.WhitelistCountries) {
		var (
			asUint uint64
			err    error

			lt = strings.ToLower(target)
		)
		if req.MinimumConfidence != nil && lookup.Country.Confidence < *req.MinimumConfidence {
			continue
		}

		for _, cname := range lookup.Country.Names {
			if strings.ToLower(cname) == lt {
				res = append(res, LookupMatch{
					GeoNameID:    lookup.Country.GeoNameID,
					MatchedType:  "country_name",
					MatchedValue: cname,
					Confidence:   lookup.Country.Confidence,
				})
			}
		}

		if lookup.Country.ISOCode == target {
			res = append(res, LookupMatch{
				GeoNameID:    lookup.Country.GeoNameID,
				MatchedType:  "iso_code",
				MatchedValue: lookup.Country.ISOCode,
				Confidence:   lookup.Country.Confidence,
			})
		}

		if asUint, err = strconv.ParseUint(target, 10, 32); err == nil {
			if lookup.Country.GeoNameID == uint32(asUint) {
				res = append(res, LookupMatch{
					GeoNameID:    lookup.Country.GeoNameID,
					MatchedType:  "geo_name_id",
					MatchedValue: target,
					Confidence:   lookup.Country.Confidence,
				})
			}
		}
	}

	return res, nil
}
