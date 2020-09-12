# gipman

simple ip geolocation service thing

## usage
for the lazy, head over to https://dcarbone.dev/gipman/docs/

## self-usage
1. create an account over at https://dev.maxmind.com/geoip/geoip2/geolite2/
1. log in to your MaxMind account, create a license, and generate a config file
1. `git clone --shallow git@github.com:dcarbone/gipman.git`
1. `mkdir -p tmp/db`
1. place the `GeoIP.conf` created in step 2 into `tmp/GeoIP.conf`
1. `docker build .`
1. `docker run -p 8080:8283 -e "GIPMAN_HOSTNAME=localhost" {image id}`

you can now open a browser and navigate to `http://127.0.0.1:8080/gipman/docs/`