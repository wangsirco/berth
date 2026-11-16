# berth/caddy: Caddy with the DNS-provider plugins Berth uses for DNS-01 certificates
# (wildcards, hosts without public port 80) and per-client rate limits. With WAF=1 it also
# carries the Coraza WAF (the optional WAF extension).
ARG CADDY_VERSION=2.11
FROM caddy:${CADDY_VERSION}-builder AS build
ARG WAF=0
RUN xcaddy build \
    --with github.com/caddy-dns/cloudflare \
    --with github.com/caddy-dns/alidns \
    --with github.com/caddy-dns/tencentcloud \
    --with github.com/caddy-dns/route53 \
    --with github.com/mholt/caddy-ratelimit \
    $( [ "$WAF" = 1 ] && echo --with github.com/corazawaf/coraza-caddy/v2 )

FROM caddy:${CADDY_VERSION}
COPY --from=build /usr/bin/caddy /usr/bin/caddy
