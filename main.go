package main

import (
	"errors"
	"flag"
	stdlog "log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
)

func zerologWriterConfig(w *zerolog.ConsoleWriter) {
	w.Out = os.Stdout
	w.TimeFormat = time.RFC3339

	stdlog.SetOutput(w)
}

func main() {
	var (
		svc  *webservice
		log  zerolog.Logger
		gm   *geoman
		errc chan error
		sigc chan os.Signal
		fs   *flag.FlagSet
		err  error
	)

	svc = new(webservice)
	gm = new(geoman)
	fs = flag.NewFlagSet("gipman", flag.ContinueOnError)

	fs.StringVar(&svc.httpAddr, "bind-http", ":8283", "Address and port to bind http")
	fs.StringVar(&gm.confFile, "geolite-conf", "/tmp/GeoIP.conf", "GeoLite 2 updater conf file")
	fs.StringVar(&gm.dbDir, "geolite-db-dir", "/tmp/db/", "Directory to store GeoLite 2 binary databases")
	fs.StringVar(&gm.updateInterval, "update-interval", "168h", "Rate at which to update GeoLite 2 Country DB [default=7 days]")

	log = zerolog.New(zerolog.NewConsoleWriter(zerologWriterConfig)).
		With().
		Timestamp().
		Str("product", "gipman").
		Logger()

	svc.log = log.With().Str("component", "service").Logger()
	gm.log = log.With().Str("component", "geoman").Logger()

	if err = fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		log.Error().Err(err).Msg("Error parsing flags")
		os.Exit(1)
	}

	errc = make(chan error, 1)
	sigc = make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

	go gm.run(errc)
	go svc.run(gm, errc)

	select {
	case err = <-errc:
		log.Error().Err(err).Msg("Abnormal exit")
		os.Exit(1)
	case sig := <-sigc:
		log.Warn().Str("signal", sig.String()).Msg("Processing exiting")
		os.Exit(0)
	}
}
