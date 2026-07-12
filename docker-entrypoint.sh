#!/bin/sh
set -e

# If the operator mounts extra CA certificates at /certs (e.g. an internal Caddy
# root so both the addon AND TorrServer can verify a private-CA Prowlarr over
# HTTPS), install them into the system trust store before starting.
if [ -d /certs ] && ls /certs/*.crt >/dev/null 2>&1; then
  cp /certs/*.crt /usr/local/share/ca-certificates/
  update-ca-certificates >/dev/null 2>&1 || update-ca-certificates
  echo "entrypoint: installed custom CA certificate(s) from /certs"
fi

exec "$@"
