package main

import (
	"errors"
	"io"
	"io/ioutil"
	"net/http"

	"github.com/dcarbone/zadapters/zstdlog"
	"github.com/emicklei/go-restful/v3"
	"github.com/rs/zerolog"
)

// todo: some sorta authentication maybe
// todo: request rate limiting of some kind
// todo: ingest size limiting

const helpText = `Simple IP geolocation service

Example request: 

curl --location --request POST '{address}/gipman/lookup' \
--header 'Accept: application/json' \
--header 'Content-Type: application/json' \
--data-raw '{
    "source_ip": "8.8.8.8",
    "whitelist_countries": [
        "United States"
    ],
    "minimum_confidence": 0
}'

Example response:

[
    {
        "geo_name_id": 6252001,
        "matched_type": "country_name",
        "matched_value": "United States",
        "confidence": 0
    }
]
`

const envHostname = "GIPMAN_HOSTNAME"
const envDocRoot = "GIPMAN_DOCROOT"

type webservice struct {
	log       zerolog.Logger
	gm        *geoman
	httpAddr  string
	container *restful.Container
}

func handleResult(response *restful.Response, res LookupResult, err error) {
	if err != nil {
		if lerr, ok := err.(LookupError); ok {
			_ = response.WriteHeaderAndEntity(lerr.Code, lerr)
		} else {
			_ = response.WriteError(http.StatusInternalServerError, err)
		}
		return
	}
	_ = response.WriteEntity(res)
}

func (ws *webservice) getHelp(request *restful.Request, response *restful.Response) {
	defer CleanupHTTPRequestBody(request)
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte(helpText))
}

func (ws *webservice) postLookup(request *restful.Request, response *restful.Response) {
	var (
		res LookupResult
		err error

		req = new(LookupRequest)
	)

	defer CleanupHTTPRequestBody(request)

	if err = request.ReadEntity(req); err != nil {
		ws.log.Error().Err(err).Msg("Error reading request entity")
		if errors.Is(err, io.EOF) {
			handleResult(response, nil, LookupError{
				Code:    http.StatusBadRequest,
				Message: "request body cannot be empty",
				Err:     err,
			})
			return
		}
	}

	res, err = ws.gm.lookupCountry(*req)
	handleResult(response, res, err)
}

func (ws *webservice) initRoutes() *restful.WebService {
	rws := new(restful.WebService)
	rws.Path("/gipman")
	rws.Route(rws.GET("/").
		To(ws.getHelp).
		Doc("Help!").
		Produces("text/plain").
		Returns(http.StatusOK, http.StatusText(http.StatusOK), nil))
	rws.Route(rws.POST("/lookup").
		To(ws.postLookup).
		Doc("Determines whether the Source IP is in within the white listed countries").
		Reads(LookupRequest{}).
		Consumes(restful.MIME_JSON).
		Produces(restful.MIME_JSON).
		Returns(http.StatusOK, http.StatusText(http.StatusOK), LookupResult{}).
		Returns(http.StatusBadRequest, http.StatusText(http.StatusBadRequest), LookupError{}))

	return rws
}

func (ws *webservice) run(gm *geoman, errc chan<- error) {
	ws.log.Info().Msg("Initializing webservice...")

	ws.gm = gm

	restful.SetLogger(zstdlog.NewStdLoggerWithLevel(ws.log.With().Str("component", "go-restful").Logger(), zerolog.DebugLevel))
	restful.TraceLogger(zstdlog.NewStdLoggerWithLevel(ws.log.With().Str("component", "go-restful").Bool("trace", true).Logger(), zerolog.ErrorLevel))

	ws.container = restful.NewContainer()
	ws.container.Add(ws.initRoutes())

	if err := bootstrapSwagger(ws.log, ws.container); err != nil {
		ws.log.Error().Err(err).Msg("Cannot init openapi docs")
	}

	ws.log.Info().Msgf("Webservice up and running at %q", ws.httpAddr)

	errc <- http.ListenAndServe(ws.httpAddr, ws.container)
}

func CleanupHTTPRequestBody(r *restful.Request) {
	if r == nil {
		return
	}
	_, _ = io.Copy(ioutil.Discard, r.Request.Body)
	_ = r.Request.Body.Close()
}
