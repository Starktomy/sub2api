#!/bin/sh
set -eu
# Root-only host operation. Secrets stay outside the repository.
exec 9>/run/lock/sub2api-origin-tls.lock
flock -n 9 || exit 0
docker run --rm \
 -v /etc/sub2api-openresty/letsencrypt:/etc/letsencrypt \
 -v /var/lib/sub2api-acme:/var/www/acme \
 certbot/certbot@sha256:bc93cb710b413e6b456d622f9b0133b3e109c0a78c2ed9976af43d209b0a1805 \
 renew --non-interactive --webroot -w /var/www/acme
src=/etc/sub2api-openresty/letsencrypt/live/aquawhale-origin
dst=/etc/sub2api-openresty/tls
install -m 0644 "$src/fullchain.pem" "$dst/fullchain.pem.new"
install -m 0640 -g 10001 "$src/privkey.pem" "$dst/privkey.pem.new"
mv "$dst/fullchain.pem.new" "$dst/fullchain.pem"
mv "$dst/privkey.pem.new" "$dst/privkey.pem"
docker exec sub2api-openresty-origin /usr/local/openresty/bin/openresty -t
docker exec sub2api-openresty-origin /usr/local/openresty/bin/openresty -s reload
