# Custom Domains

The Blob platform terminates TLS at a Traefik edge that ships with two trust roots out of the box:

1. The wildcard cert for `*.<base>` (e.g. `*.irrigate.cc`) — what every default app URL uses.
2. The Let's Encrypt resolver `le`, configured for the http-01 challenge in `bootstrap-host.sh`. Any router rule whose Host(...) clause is *not* covered by the wildcard will trigger a fresh ACME order.

`blob certs` (v0.22) is the operator surface for binding a custom hostname to an app and confirming that the LE cert actually landed.

## Quick start

```sh
# 1) Point an A record at the platform IP. *Do not* proxy it through
#    Cloudflare — http-01 must reach the edge directly. Authoritative
#    DNS only.
#
#       tls.demo.example.com.  IN  A  <platform-ip>
#
# 2) Bind the hostname to an app.
blob certs add my-app tls.demo.example.com

# 3) Hit the URL once. Traefik will queue an LE order on the first
#    request to https://tls.demo.example.com — the call may take 5-15s
#    while http-01 completes.
curl -I https://tls.demo.example.com/

# 4) Confirm the cert actually came from Let's Encrypt and includes
#    your hostname in the SAN list.
blob certs verify tls.demo.example.com
```

## What `blob certs add` actually does

It re-renders the running Nomad job for the target app and adds a `Host(\`...\`)` clause to the existing Traefik router rule. The `tls.certresolver=le` tag is already in place on every web-service / static deploy, so Traefik picks up the new hostname on the next request and starts an http-01 order.

The binding meta is stored at `/srv/blob/certs/<hostname>.json`:

```json
{
  "app": "my-app",
  "hostname": "tls.demo.example.com",
  "created_at": "2026-05-04T02:47:31Z",
  "verified": true,
  "last_probe": "2026-05-04T02:48:07Z",
  "last_issuer": "R13"
}
```

## Verification

`blob certs verify <host>` opens a TLS dial to `<host>:443`, inspects the served leaf, and pass/fails on:

- The leaf's SAN list contains `<host>` (proves the wildcard fallback isn't hiding a missing cert)
- The issuer CN matches one of Let's Encrypt's public intermediates: `R10`, `R11`, `R12`, `R13`, `E5`, `E6`, `E7`, or any string containing `Let's Encrypt`

Failure cases are persisted to `last_error` so `blob certs list` can show the operator what to fix:

```
HOSTNAME                  APP        VERIFIED  ISSUER       LAST PROBE
tls.demo.example.com      my-app     yes       R13          2026-05-04T02:48:07Z
broken.example.com        my-app     no        TRAEFIK DEFAULT CERT  2026-05-04T02:50:01Z
    last error: leaf SAN list [...] does not include "broken.example.com" (probably still serving the wildcard fallback)
```

## Removal

```sh
blob certs remove tls.demo.example.com
```

Drops the hostname from the Traefik router rule and re-submits the job. The cached cert in `/srv/traefik/acme.json` is **not** removed — re-binding the same hostname later will reuse the cached cert until it expires (LE certs are valid for 90 days).

## Common failure modes

**"leaf SAN list does not include ..."** — the http-01 challenge didn't complete. Most likely the hostname is still proxied through Cloudflare / a CDN that intercepts `:80` traffic. ACME can only see the platform's IP if the A record is direct (gray-cloud / DNS-only on Cloudflare). Verify with `dig +short <host> @ns1.cloudflare.com` (or whichever authoritative server) — the answer must be the platform IP, not a `172.67.*` / `104.21.*` Cloudflare anycast address.

**"leaf issuer is not Let's Encrypt"** — Traefik fell back to its self-signed default cert because the LE order failed. Check `docker logs edge-traefik | grep -i acme` on the platform host; the most common cause is rate limits (50 certs/registered-domain/week on the production endpoint) or `:80` being firewalled.

**"hostname is under the platform wildcard"** — you tried to bind `whatever.<base-domain>`. The wildcard already covers it; just point your DNS at the platform IP and use it directly.

## UFW

The platform host needs `:80` open to the internet for http-01. The bootstrap script (`bootstrap-host.sh`) already sets this up, but if you're running on a host you provisioned by hand:

```sh
sudo ufw allow 80/tcp comment "blob acme http-01"
sudo ufw allow 443/tcp comment "blob edge tls"
```

## Verified live (v0.22 ship)

`tls.demo.irrigate.cc` (a sub-sub host — `*.irrigate.cc` only matches one label) was bound to the `blob-mongo-demo` app with `blob certs add`. Within 15s of the first request, Traefik served a Let's Encrypt R13 leaf with the new SAN. `curl https://tls.demo.irrigate.cc/count` returned the demo's mongo round-trip count, proving the bind reached the right backend over a real LE cert.
