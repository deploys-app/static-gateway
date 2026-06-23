# static-gateway

The in-cluster HTTP origin that serves **static site releases directly from object
storage** for deploys.app. One shared instance per location (the same shape as the
existing `ipfs-gateway` Service). No per-site pods, scale-to-zero economics, atomic
publish, instant rollback.

This is the core serving primitive of the *GitHub App — Static Web Deploys* feature
(`SPEC-github-static-web.md`, Section 4).

## What it does

A static deploy uploads a Hugo/Node build to object storage as an immutable,
content-addressed **release**: one blob per unique file plus a JSON **manifest**
that maps each logical path to its blob (sha256 + Content-Type + cache class) and
carries the release's `environment`, `spa` flag, and custom `notFound` document.

The edge routes a host to this gateway through an Ingress whose `upstream-path`
annotation prepends the release prefix, so requests arrive already namespaced:

```
GET /<project>/<name>/<release-sha>/getting-started/introduction/
```

For each request the gateway:

1. parses the leading three path segments as `<project>/<name>/<release-sha>` and
   confines the remainder (rejects `..` / absolute escape);
2. loads the release manifest (`releases/<release-sha>`) and caches it in-memory —
   a release-sha is immutable, so the cache never needs invalidation;
3. resolves the clean URL against the manifest (Hugo directory-index, extensionless,
   trailing-slash, SPA fallback — see below);
4. streams the backing blob (`blobs/<sha256>`) with its Content-Type and
   Cache-Control, a strong `ETag` (the blob sha), `Last-Modified`, and security
   headers; honors `If-None-Match` / `If-Modified-Since` with a `304`;
5. on a miss serves the release's `404.html` (HTTP 404) or a built-in default 404.

### Clean-URL resolution

| Request | Serves |
|---|---|
| `/` or empty | `index.html` |
| `/foo/` (trailing slash) | `foo/index.html` |
| `/foo` (extensionless) | `foo/index.html`, else `foo.html` |
| `/style/main.<hash>.css` (exact) | that entry |
| miss, `spa: false` | `404.html` @ 404 (or built-in default 404) |
| miss, `spa: true` | `index.html` @ 200 (client-routed SPAs) |

### Cache classes

| Class | `Cache-Control` |
|---|---|
| `immutable` (fingerprinted assets) | `public, max-age=31536000, immutable` |
| `html` (documents, sitemap, RSS, search-index) | `public, max-age=0, must-revalidate` |

HTML is always revalidated; the `ETag` (= blob sha256) turns each revalidation into
a cheap `304`. Preview releases (`environment` ≠ `production`) get
`X-Robots-Tag: noindex` on HTML responses.

## Request path shape

```
/<project>/<name>/<release-sha>/<path...>
```

The prefix is set by the parapet-ingress-controller via the `upstream-path`
annotation — it is trusted (the controller, not the client, prepends it). The blob
key is always `sites/<project>/<name>/blobs/<blobSha>` where `blobSha` comes from
the **manifest**, never the URL, so a crafted path can only ever resolve to a blob
the requested release already references.

## Storage layout

```
<bucket>/
  sites/<project>/<name>/releases/<release-sha>   # manifest (JSON)
  sites/<project>/<name>/blobs/<sha256>           # one object per unique file
```

## Environment variables

| Var | Required | Default | Meaning |
|---|---|---|---|
| `SITE_BUCKET` | yes | — | GCS bucket holding the `sites/...` layout |
| `PORT` | no | `8080` | listen port |
| `METRICS_PORT` | no | `9090` | Prometheus scrape port (`/metrics`) |
| `MANIFEST_CACHE_BYTES` | no | `268435456` (256 MiB) | approximate-bytes budget for the parsed-manifest cache; `0` = entry-count bound only |
| `BLOB_CACHE_BYTES` | no | `134217728` (128 MiB) | in-process cache budget for small immutable blobs (≤1 MiB each) served from memory instead of a GCS read; `0` disables |
| `METRICS_TTL` | no | `48h` | idle window after which a site's per-site metric label sets are evicted to bound cardinality as previews churn; must exceed the collector's 1-day billing window; `0` disables eviction |
| `METRICS_SWEEP_INTERVAL` | no | `1h` | how often the evictor sweeps for idle sites |

The two caches stack in memory — `BLOB_CACHE_BYTES` fills with real file bytes, so
size the sum against the container memory limit before raising it. Larger blobs
stream straight from GCS and are never cached (they are immutable and edge-cached
for a year, so the origin rarely re-reads them).

Credentials come from **Application Default Credentials** — in cluster this is
Workload Identity bound to a read-only GSA scoped to the static bucket (SPEC §6.5).
The gateway never has write access.

To move storage to Cloudflare R2 / S3, swap the gocloud opener in
`internal/blobstore/gcs.go` for `moonrhythm/r2blob` (`r2://`); no other code changes.

## Package layout

```
main.go                       thin: config -> gcsStore -> server -> parapet listen
internal/manifest             release manifest types + canonical JSON + loader
internal/resolve     (pure)   Hugo clean-URL resolution (heavily table-tested)
internal/contenttype (pure)   canonical extension -> Content-Type table
internal/cacheheader (pure)   cache class -> Cache-Control, ETag, 304 decision
internal/blobstore            read-only object-storage interface + gcsStore + Fake
internal/server               the http.Handler + manifest LRU cache
```

The algorithmic packages (`resolve`, `contenttype`, `cacheheader`, `manifest`) are
pure and have no I/O dependencies. GCS is isolated behind `blobstore.Store`, so the
server is unit-tested against an in-memory `Fake` — no real GCS needed.

## Develop

```sh
go build ./...
go vet ./...
gofmt -l .
go test ./...
```

## Run locally

```sh
SITE_BUCKET=deploysapp-sites-<location> PORT=8080 go run .
# then, against a manually-uploaded release:
curl -i localhost:8080/<project>/<name>/<release-sha>/
```
