# Changelog

All notable changes are documented here. The project follows
[Semantic Versioning](https://semver.org/); before 1.0 minor versions may
contain breaking changes, which are listed here.

## Unreleased

### Moved

- The project lives at `github.com/fada4773-sketch/apigen-apitest`. The
  library is the package `apitest` in the subdirectory `apitest/`: import
  `github.com/fada4773-sketch/apigen-apitest/apitest`; the API is unchanged
  (`apitest.Run`, `apitest.Config`, …). The versions below were released
  under the previous module path `github.com/fada4773-sketch/apitest`.

### Features

- `apitest-gen` (preview), a command line tool next to the library:
  `apitest-gen dict -spec openapi.yaml -dict global-dict.json` creates or
  updates a global dictionary with one node per DTO field and parameter.
  Constraints come from the spec; values are generated deterministically
  (`-seed`), shared by fields with the same name and write-protected on
  later runs (`-repair` regenerates values that no longer fit).
  Values for `pattern` come from a generator that builds candidates from
  the regex syntax tree and checks each one against the original ECMA-262
  pattern the way apitest validates it, lookaheads included; unsolvable
  patterns are reported as `PATTERN_PENDING`, never filled wrongly.
  Readable values come from gofakeit by field name and DTO (City, Zip,
  FirstName, User.Name, "Brave Garden" for Garden.Name, versions, phones, …),
  matched by whole words. Compound names (`PlanetCode`) share one value
  across DTOs and parameters, generated for the strictest place first;
  generic names (`Name`, `Id`) do not.
- `apitest-gen apply` (the default command) updates the dictionary and
  writes missing or invalid examples into the spec, in place or with `-out`:
  parameter examples, request bodies and 2xx responses, at the `$ref`
  target for shared objects, never next to a `$ref`, never next to named
  `examples`. Comments and key order are kept; a second run changes nothing.
  `defaults.json` sets values everywhere (`Name`, `Dto.Name`,
  `operationId.name`), generic path ids (`/apps/{id}` or `appId` derived
  from the path), extensions (`operationId.x-apitest-verify`) and bindings
  (`{"bind": "createApp", "pointer": "/Id"}`, written as `x-apitest-bind`
  or `links`). A default that violates a schema stops the run before
  anything is written; the written spec is loaded again before it replaces
  the original.
- `apitest-gen discover -defaults defaults.json -base-url <url>` fetches
  sources such as `{"from": "GET /Planet", "pick": "/[Active=true]/Code"}`
  from a running environment, in the order their placeholders need
  (`GET /Planet/{planetCode}/Moon`), and writes the values to a file
  for `apply -defaults defaults.json,defaults.qa.resolved.json`. `apply
  -base-url` does the same in memory. Only GET requests are sent; the token
  comes from an environment variable (`-token-env`) and is never printed.
- `apitest-gen apply` replaces every invalid example apitest validates: also
  in error responses and in `components.schemas` (missing ones are not
  added there). Invalid curated named examples are kept and reported as
  `EXAMPLE_NAMED_INVALID`.
- `defaults.json` values for fields that hold a DTO or a list of DTOs
  (`"Ship.Pilot": {"Name": "Ada"}`) are merged into the generated value and
  into existing examples; fields left out keep their values, a list sets
  the length, readOnly/writeOnly fields are left out where they are not
  allowed. Previously such keys were ignored.
- `"#/components/schemas/<Dto>": {…}` in `defaults.json` sets a DTO itself,
  also a free one: its schema example and every body, field and list
  element of that type. A key that names a DTO instead of a field is
  reported with this key as a hint.
- `apitest-gen review` evaluates what apitest and apitest-gen would report
  (spec findings such as heuristic bindings, missing 401/403, invalid
  examples and links, cases that cannot be sent, values the generator
  cannot create, wrong or unused defaults) and writes the fixes as data
  into `defaults.json` (created if missing): the bindings apitest guesses,
  bindings to list GETs where no POST creates the resource
  (`{"bind": "GetBooks", "pointer": "/0/Id"}`), and the ids used so far.
  No comments are written; the reasons are printed. The next
  `apitest-gen` run takes the entries into dictionary and spec. Keys in
  `$rejected` are not proposed again.
- `defaults.json`: `null` marks a value still to be filled in
  (`DEFAULT_TODO`); `apply` creates an empty defaults file if it is missing.
- Response examples follow the path: `GET /Book/id/{id}` with id 100
  returns `Id: 100`, the Articles under `/Book/id/{id}/Article` get
  `BookId: 100`; a path value from the defaults is also kept in the DTO
  field of the dictionary.
- A path default for a generic id that cannot be written, because the
  parameter object is shared by several paths, is reported as
  `SHARED_PARAM_CONFLICT` instead of `DEFAULT_UNUSED`; for a bound
  parameter it is not needed and only noted with `-v`.
- `review` lists a missing body example once per operation, not once per
  case.
- New `links` are written in block style, one link per line, instead of
  one long flow mapping.
- `examples/path-conflict`: what happens with `/book/{id}` next to
  `/book/{class}`, tested with `check`, `review`, `apply` and apitest,
  before and after the fix.
- Inline schemas outside any DTO (e.g. a response `{type: object,
  properties: …}`) get generated values instead of `EXAMPLE_INCOMPLETE`.
- Applied defaults are kept in `global-dict.json` (`DICT_FROM_DEFAULTS`),
  except operation-scoped ones, so the dictionary shows the values the spec
  uses and a second run reports nothing.
- Free objects (`type: object` without `properties`) get `{}`, or entries
  from a typed `additionalProperties`, `minProperties` and `required`,
  instead of `NO_VALUE`.
- `apitest-gen check -spec openapi.yaml` (and `apply -check`) reports every
  case apitest could not send (`NOT_BUILDABLE`, with the reason) and every
  example that violates its schema, with exit code 1 for CI. It uses
  apitest's own case building, bindings and request preparation; values
  from the defaults count like `Config.Params`.

## v0.1.10

### Changed

- `Config.DisableWarnings` also drops the spec findings from the report.
- The report sections are collapsible (`<details>`) with counts in their
  titles; warnings, errors and deviations are open, the rest is closed.
- Deviation entries accept case names as copied from `go test -v` or an IDE:
  a leading test name, the `NumberCases` prefix and spaces are handled.
- A failed case names deviation entries that match its name but accept
  another result, e.g. `accepts 200 → 400, the result is 200 → 404`.
- `make update-golden` only passes `-update` to the report package.

## v0.1.9

### Changed

- The `invalid-token` case sends the first 100 characters of the real token
  (its first half if it is shorter) instead of a token with an inverted
  signature, so APIs that only decode tokens behind a validating gateway
  reject it as well.
- `Config.SkipAuthCases` leaves the listed cases out completely; they no
  longer appear as `SKIPPED` subtests or in the report.
- `Config.DisableWarnings` also drops the warnings from the report.

### Features

- `Config.TamperToken` sets how the `invalid-token` token is derived;
  `apitest.TruncateToken` (default) and `apitest.TamperSignature` (the
  previous behaviour) are provided.

## v0.1.8

### Features

- `Config.MethodOrder` orders the regular cases of a group by method, e.g.
  `{"POST", "PUT", "GET"}`; DELETE stays last, bindings still win.
- `Config.DeleteLast` runs the DELETEs of all groups after every other case,
  so a DELETE cannot remove seed data that a later group uses through
  `Params`.
- `Config.NumberCases` prefixes subtest names with their position
  (`07_Organization/createOrganization/valid`); numbers are stable under
  `-run`, case names in deviations, hooks and results stay unchanged.
  `CaseResult.Number` and the JSON report carry the number.
- `Config.ReportPassedDetails` shows passed cases with request, response,
  headers and `curl` command in the report.
- `Config.DisableWarnings` keeps warnings out of the `go test` output; the
  report still lists them.

## v0.1.7

### Report

- Every failed case shows the response headers, also when the response has
  no body, e.g. to see whether a 401 came from the API or from a proxy
  (`WWW-Authenticate`, `Server`, `Via`).
- The `curl` command contains the request headers as sent, including the
  ones set in `Hooks.BeforeRequest`.
- Header values are masked if the name looks secret (`X-Api-Key`,
  `X-Signature`, `X-Session-Id`, names containing a `Redact` field), in
  addition to `Authorization`, `Cookie` and `Set-Cookie`.

## v0.1.6

### Features

- `Config.SkipAuthCases` reports the listed authentication cases
  (`AuthUnauthorized`, `AuthInvalidToken`, `AuthForbidden`) as `SKIPPED`,
  e.g. `invalid-token` where no proxy validates tokens.

### Documentation

- `docs/configuration.md` explains every `Config` field, the deviations
  file, the spec extensions and common setups with examples.

## v0.1.0 – v0.1.5

First public version.

### Features

- `apitest.Run` derives test cases from the examples of an OpenAPI 3.0/3.1
  spec (Swagger 2.0 is converted) and runs each case as a subtest named
  `<Tag>/<operationId>/<example>`.
- Three checks per response: status code (undocumented codes fail), schema
  (types, formats, required fields, `additionalProperties`, headers, content
  type) and the expected example (`subset`, `exact`, `schema`; arrays
  optionally unordered).
- Parameters from bindings (`x-apitest-bind`, OpenAPI `links`, heuristic),
  `Config.Params` and examples; values from `Location` headers; bound keys
  follow the value actually stored after a PUT.
- GET check after every write and 404 check after DELETE, optionally with
  polling for asynchronous processing.
- Resource groups ordered by their dependencies; cycles and contradicting
  `Config.Tags` are reported before the first request; dependents of a
  failed producer are skipped with the cause.
- Authentication cases: without token, with a manipulated token and with a
  token that lacks rights (`x-apitest-forbidden`). An unauthorized DELETE
  that succeeds skips the regular DELETE.
- Accepted deviations with reason and expiry date, exact matching, warnings
  before expiry, report of unused entries.
- `go test -run` on a single case runs its producers as preconditions.
- Markdown report after every group (partial report on abort or deadline),
  optional JSON report, redaction of tokens, manipulated tokens, API keys in
  URLs and error messages, `curl` commands with `$TOKEN` placeholders.
- `Config.DisableReports` turns off all report files; results are then only
  reported through `go test` and the returned `Result`.
- Specs without any `security` get `Config.Token` as bearer token for every
  operation, reported as a spec finding. Whitespace, line breaks and a
  `Bearer ` prefix are removed from tokens before sending.
- `Config.Params` keys of the form `"<operationId>.<name>"` set a value for
  one operation only.
- `pattern` keywords are ECMA-262 regular expressions: patterns that Go's
  `regexp` does not support (lookahead, lookbehind, backreferences) are
  evaluated with an ECMAScript-compatible engine with a one-second timeout.
- The binding heuristic matches field names case-insensitively, finds the
  collection POST behind literal path segments, by the resource name or by a
  body field, uses a POST of another tag if it is named after the resource,
  and never adds a binding that would create a cycle.
- The `invalid-token` case changes the token much more: a JWT keeps its
  header and claims, gets the extra claim `"apitest": "invalid-token"` and an
  inverted signature; opaque tokens get every second character changed.
  Previously only one bit of the signature was flipped.
- A heuristic binding whose successful producer returns no value falls back
  to `Config.Params` with a warning instead of skipping its dependents.
- A failed `unauthorized` or `invalid-token` case that the API answered
  with 2xx explains what was sent (no token, or the manipulated token),
  since the report redacts both tokens.
- Readiness check, per-request timeout, no retries, protection against
  writing requests to non-local hosts, expiry check for static JWTs.

### Quality

- Component tests against a built-in test API with 13 fault switches; each
  switch changes exactly the expected cases.
- Reference test against the Swagger Petstore v3 in a container
  (`examples/petstore`), with six classified deviations.
- Loading and planning the GitHub REST description (1,231 operations) takes
  about 2 s.
