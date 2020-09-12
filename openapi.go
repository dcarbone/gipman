package main

import (
	"fmt"
	"net/http"
	"os"

	"facette.io/natsort"
	restfulspec "github.com/emicklei/go-restful-openapi/v2"
	"github.com/emicklei/go-restful/v3"
	"github.com/go-openapi/spec"
	"github.com/rs/zerolog"
)

func bootstrapSwagger(log zerolog.Logger, container *restful.Container) error {
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

	seedRouteTags(log, container)

	swgConfig := restfulspec.Config{
		Host:                          host,
		WebServices:                   container.RegisteredWebServices(),
		APIPath:                       specPath,
		PostBuildSwaggerObjectHandler: postBuildSwaggerObjectHandler(container),
	}

	container.Add(restfulspec.NewOpenAPIService(swgConfig))
	container.ServeMux.Handle(routePath, http.StripPrefix(routePath, http.FileServer(servePath)))

	return nil
}

func seedRouteTags(log zerolog.Logger, container *restful.Container) {
	log.Debug().Msg("Seeding route swaggerTags for routes that do not specify one...")
	for _, ws := range container.RegisteredWebServices() {
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

func postBuildSwaggerObjectHandler(container *restful.Container) restfulspec.PostBuildSwaggerObjectFunc {
	return func(swo *spec.Swagger) {
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

		annotateSwaggerTags(container, swo)
	}
}

// annotateSwagger tags does a few things:
// 		1. Adds any tags and descriptions created when creating a webservice using one of the helpers
//		2. Loops through all registered webservices and their associated routes and locates any defined tags
//		3. If tag(s) defined, takes first (if more than one) defined and creates a new Swagger Tag definition using the
//			defined tag as the name of the tag and the route's containing webservice's description as the swagger tag
//			description
//		4. Appends tags not defined by webservice helper to the swagger object.
func annotateSwaggerTags(container *restful.Container, swo *spec.Swagger) {
	// used to keep track of tags defined in routes so we don't repeat ourselves.
	routeTags := make(map[string]spec.Tag)

	// loop over all webservices...
	for _, ws := range container.RegisteredWebServices() {
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
