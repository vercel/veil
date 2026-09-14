# Acme — the veil playground

A miniature infrastructure ecosystem, small enough to read in one sitting
and complete enough to exercise every veil feature. `../e2e_test.go`
drives it with the real binary; you can drive it by hand the same way.

The suite runs as its own CI check, separate from the unit run:

```
go test ./e2e/...
```

```
vpc/acme-global ← everything sits in the network
  ↑            ↑              ↑
postgres/orders-db   redis/sessions-cache   postgres/billing-db
  ↑            ↑                              ↑
  └─ platform/commerce-platform ─┘            │
             ↑                                │
      service/checkout                 service/billing
```

## The kinds

| kind | what it is | what it shows off |
|---|---|---|
| `vpc` | the network everything lands in | JSON source, dependent hook injecting network env |
| `postgres` | a managed database | validate hook (no tiny prod DBs), dependent hook injecting `*_DATABASE_URL` |
| `redis` | a managed cache | **YAML** source — the codec is per source, not per project |
| `secret` | a credential a database holds | accepts `postgres` and nothing else — forwarding it to a service is an error |
| `platform` | a bundle a team adopts as one unit | `forward_dependencies: true` |
| `service` | an application | typed YAML source, `post_render` normalization, validate gate |

## What to look at

**Forwarding.** `checkout` declares exactly one dependency — the platform
— and comes out wired to a database, a cache and a VPC, because the
platform kind forwards what it depends on:

```
$ veil render resources/services/checkout.json --out /tmp/out
$ cat /tmp/out/checkout/sources/env
ACME_PLATFORM=commerce-platform
ACME_SUBNET=private
ACME_VPC=acme-global
ACME_VPC_CIDR=10.0.0.0/16
ORDERS_DATABASE_URL=postgres://acme@orders-db.us-east-1.rds.acme.internal:5432/orders-db?pool=25
SESSIONS_REDIS_URL=redis://sessions-cache.us-east-1.cache.acme.internal:6379
```

`billing` is the control: it depends on its own database directly and
sees nothing else.

**Per-edge forwarding.** `billing`'s database edge sets `"forward": true`,
so anything depending on `billing` would inherit it. The kind-level flag
on `platform` is the same idea applied to every edge of a kind.

**What forwarding refuses.** `orders-db` holds a `secret`, and the secret
kind registers dependents for `postgres` only. Marking that edge
`"forward": true` fails the load rather than quietly dropping it:

```
cannot inherit secret/commerce-signing-key, forwarded by postgres/orders-db:
kind "secret" declares no dependents for kind "platform"
(stop forwarding that dependency, or give the secret kind a dependents entry for "platform")
```

**Variables and overlays.** `--var environment=production` applies
`checkout.production.json` (2 replicas → 12) and arms the database
validate hook, which rejects single-AZ micro instances in production.

**Encodings.** The service deployment is YAML, the VPC network is JSON.
Neither hook mentions its encoding: `getContent()` hands back an object
either way, `setContent()` writes it back in the source's own format.

## Driving it by hand

The compiled registry under `public/` is build output and is gitignored,
so start with a build — `graph` and `render` both read it.

```
veil build                                     # compile kinds + registry
veil graph resources/services/checkout.json    # see the effective edges
veil render resources/services/checkout.json --out /tmp/out
veil render resources/services/checkout.json --out /tmp/prod --var environment=production
```
