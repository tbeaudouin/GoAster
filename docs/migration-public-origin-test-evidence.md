# Migration public-origin test evidence

## Scope

Migration guidance and packaging checks use the fixed public service
`https://goasterweb-thomas.fly.dev/`. The active workflow uses normal public
search/fetch tools for catalogue discovery and rough pricing, then sends one
HTTPS create request after paired-private approval. TLS verification, actual
HTTPS failure reporting, secret protection, exact-name validation, and the
split private URL/key handoff remain required.

## Offline validation

The Go tests use local HTTP and Telegram fakes. The packaging check validates
the installed skill tree and local Markdown links. No provider, Web, Telegram,
SSH, deployment, or cloud request is part of this evidence.
