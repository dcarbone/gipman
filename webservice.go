package main

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"

	"facette.io/natsort"
	"github.com/dcarbone/zadapters/zstdlog"
	restfulspec "github.com/emicklei/go-restful-openapi/v2"
	"github.com/emicklei/go-restful/v3"
	"github.com/go-openapi/spec"
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

func (ws *webservice) bootstrapSwagger() error {
	host := os.Getenv(envHostname)
	docRoot := os.Getenv(envDocRoot)
	if host == "" {
		return fmt.Errorf("envvar %q is not set", "GIPMAN_HOSTNAME")
	}

	if docRoot == "" {
		docRoot = "/tmp/gipman/openapi"
	}

	routePath := "/gipman/docs/"
	specPath := "/gipman/docs/spec.json"
	servePath := http.Dir(docRoot)

	ws.seedRouteTags()

	swgConfig := restfulspec.Config{
		Host:                          host,
		WebServices:                   ws.container.RegisteredWebServices(),
		APIPath:                       specPath,
		PostBuildSwaggerObjectHandler: ws.postBuildSwaggerObjectHandler,
	}

	ws.container.Add(restfulspec.NewOpenAPIService(swgConfig))
	ws.container.ServeMux.Handle(routePath, http.StripPrefix(routePath, http.FileServer(servePath)))

	return nil
}

func (ws *webservice) seedRouteTags() {
	ws.log.Debug().Msg("Seeding route swaggerTags for routes that do not specify one...")
	for _, ws := range ws.container.RegisteredWebServices() {
		// get local reference to route slice
		routes := ws.Routes()
		for i := range routes {
			// test to see if route already has tag defined in metadata...
			if _, ok := routes[i].Metadata[restfulspec.KeyOpenAPITags]; !ok {
				if routes[i].Metadata == nil {
					// if no metadata is defined on the route the map will be nil by the time we get here, so make on.
					routes[i].Metadata = make(map[string]interface{})
				}
				// set route tag to its containing webservice's path.
				routes[i].Metadata[restfulspec.KeyOpenAPITags] = []string{ws.RootPath()}
			}
		}
	}
}

func (ws *webservice) postBuildSwaggerObjectHandler(swo *spec.Swagger) {
	ws.log.Debug().Msgf("Swagger post-build handler called")
	swo.Swagger = "2.0"
	swo.ID = "gipman"

	swo.Schemes = []string{"https", "http"}

	swo.Info = &spec.Info{
		InfoProps: spec.InfoProps{
			Title:       "gipman",
			Description: "simple ip geolocation service",
			Contact: &spec.ContactInfo{
				ContactInfoProps: spec.ContactInfoProps{
					Name:  "Daniel Carbone",
					Email: "daniel.p.carbone@gmail.com",
				},
			},
			License: &spec.License{
				LicenseProps: spec.LicenseProps{
					Name: "Apache-2.0",
				},
			},
			Version: "1.0.0",
		},
	}

	// TODO: eventually fill this thing out?
	swo.Definitions["api.ReadableDuration"] = spec.Schema{
		SchemaProps: spec.SchemaProps{
			Type: []string{"object"},
		},
	}

	swo.Definitions["error"] = spec.Schema{
		SchemaProps: spec.SchemaProps{
			Type: []string{"string"},
		},
	}

	ws.annotateSwaggerTags(swo)
}

// annotateSwagger tags does a few things:
// 		1. Adds any tags and descriptions created when creating a webservice using one of the helpers
//		2. Loops through all registered webservices and their associated routes and locates any defined tags
//		3. If tag(s) defined, takes first (if more than one) defined and creates a new Swagger Tag definition using the
//			defined tag as the name of the tag and the route's containing webservice's description as the swagger tag
//			description
//		4. Appends tags not defined by webservice helper to the swagger object.
func (ws *webservice) annotateSwaggerTags(swo *spec.Swagger) {
	// used to keep track of tags defined in routes so we don't repeat ourselves.
	routeTags := make(map[string]spec.Tag)

	// loop over all webservices...
	for _, ws := range ws.container.RegisteredWebServices() {
		// localize routes so we aren't shallow copying shit all over the place.
		routes := ws.Routes()
		for i := range routes {
			if v, ok := routes[i].Metadata[restfulspec.KeyOpenAPITags]; ok {
				// for now, only look at the first one
				if tagList, ok := v.([]string); ok && len(tagList) > 0 {
					tagName := tagList[0]
					// check to see if the webservice creator already annotated the tag
					if _, ok = routeTags[tagName]; !ok {
						// check to see if we've already seen a route with this tag.
						if _, ok = routeTags[tagName]; !ok {
							routeTags[tagName] = spec.NewTag(tagName, ws.Documentation(), nil)
						}
					}
				}
			}
		}
	}

	// all this nonsense sorts the tags alphabetically

	tagNames := make([]string, 0)

routeTagOuter:
	for _, tag := range routeTags {
		for _, curr := range tagNames {
			if tag.Name == curr {
				continue routeTagOuter
			}
		}
		tagNames = append(tagNames, tag.Name)
	}

swoTagOuter:
	for _, tag := range swo.Tags {
		for _, curr := range tagNames {
			if tag.Name == curr {
				continue swoTagOuter
			}
		}
		tagNames = append(tagNames, tag.Name)
	}

	var cswo []spec.Tag
	if cl := len(cswo); cl != 0 {
		cswo = make([]spec.Tag, cl)
		copy(cswo, swo.Tags)
	}

	// reset source tags
	swo.Tags = make([]spec.Tag, 0)

	natsort.Sort(tagNames)

finalOuter:
	for _, tagName := range tagNames {
		for _, tag := range routeTags {
			if tag.Name == tagName {
				swo.Tags = append(swo.Tags, tag)
				continue finalOuter
			}
		}
		for _, tag := range cswo {
			if tag.Name == tagName {
				swo.Tags = append(swo.Tags, tag)
			}
		}
	}
}

func (ws *webservice) run(gm *geoman, errc chan<- error) {
	ws.log.Info().Msg("Initializing webservice...")

	ws.gm = gm

	restful.SetLogger(zstdlog.NewStdLoggerWithLevel(ws.log.With().Str("component", "go-restful").Logger(), zerolog.DebugLevel))
	restful.TraceLogger(zstdlog.NewStdLoggerWithLevel(ws.log.With().Str("component", "go-restful").Bool("trace", true).Logger(), zerolog.ErrorLevel))

	ws.container = restful.NewContainer()
	ws.container.Add(ws.initRoutes())

	if err := ws.bootstrapSwagger(); err != nil {
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
