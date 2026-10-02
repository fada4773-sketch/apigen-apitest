# Configuration reference

`apitest.Run(t, apitest.Config{...})` is the only entry point. This page explains every field of `Config`, the file formats and spec extensions it works with, and shows how they combine. The zero value of every field is a sensible default. Only `SpecPath` and one of `BaseURL` or `Handler` are required.

The examples use a small organization API: organizations own workflows and deployments.

```go
func TestAPI(t *testing.T) {
	apitest.Run(t, apitest.Config{
		SpecPath: "openapi.yaml",
		BaseURL:  "http://127.0.0.1:8080/v1",
		Token:    apitest.StaticToken(os.Getenv("API_TOKEN")),
	})
}
```

That is a complete test. apitest turns the examples in the spec into cases. It sends them in dependency order, checks status, schema and example, and reads every write back with GET. It also tests authentication and writes a Markdown report.

## Contents

1. [Where the API runs](#1-where-the-api-runs)
2. [Authentication](#2-authentication)
3. [Selecting what runs](#3-selecting-what-runs)
4. [Parameters and headers](#4-parameters-and-headers)
5. [Comparing responses](#5-comparing-responses)
6. [Accepted deviations and strict mode](#6-accepted-deviations-and-strict-mode)
7. [Reports](#7-reports)
8. [Hooks](#8-hooks)
9. [The returned Result](#9-the-returned-result)
10. [Spec extensions](#10-spec-extensions)
11. [Recipes](#11-recipes)
12. [All fields at a glance](#12-all-fields-at-a-glance)

---

## 1. Where the API runs

### `BaseURL` or `Handler`

Set exactly one of them.

| Field | Use it when |
|---|---|
| `BaseURL string` | The API already runs: in a container, on a dev server, started by `TestMain`. The URL replaces the `servers` entry of the spec and must contain its base path. |
| `Handler http.Handler` | The API can be built in the test process. apitest starts it with `httptest.NewServer` and appends the base path of the spec's `servers` entry. No port, no container, and a debugger works across the test and the API. |

```go
// A running API (the spec says servers: [{url: /v1}])
cfg.BaseURL = "http://127.0.0.1:8080/v1"

// The same API in-process
cfg.Handler = app.NewRouter(app.NewMemoryStore())
```

### `HealthPath string`

This path is requested before the first case and must answer 2xx. The readiness check waits up to 30 s, which is useful right after a container start. Without `HealthPath`, any HTTP response on `/` counts as ready. The health request is sent **without a token**, so point it at a public endpoint.

```go
cfg.HealthPath = "/healthz"
```

### `RequestTimeout time.Duration`

This limits every single request. The default is `apitest.DefaultRequestTimeout` (10 s). A timeout marks the case as `ERROR`. There are no retries, since a retry would hide flaky behaviour.

```go
cfg.RequestTimeout = 3 * time.Second
```

The whole run also respects `go test -timeout`: shortly before the deadline, cases that have not started are reported as `SKIPPED`, and the report is still written.

### `HTTPClient *http.Client`

This client is used for every request, including the readiness check and the GET checks. Use it for proxies, client certificates (mTLS), custom CAs or tracing. The default client follows redirects like any Go `http.Client`. Set `CheckRedirect` as below to check the redirect response itself.

```go
cert, _ := tls.LoadX509KeyPair("client.crt", "client.key")
cfg.HTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: myCAPool},
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
```

### `AllowedHosts []string` and `AllowRemoteWrites bool`

apitest creates, changes and deletes data. To protect shared systems, **writing requests (POST, PUT, PATCH, DELETE) only go to `localhost` and loopback addresses** by default. For every other host, write cases are refused with a clear message, and reads still run.

```go
// A disposable test environment by host name
cfg.AllowedHosts = []string{"api.test.internal"}

// Any host, e.g. an ephemeral preview environment per pull request
cfg.AllowRemoteWrites = true
```

Leave both empty for a read-only smoke test against staging: all GET cases run, while writes are reported and not sent.

---

## 2. Authentication

### `Token TokenSource`

The token is sent with every regular case in the way the spec's `securitySchemes` describe:

| Scheme in the spec | How the token is sent |
|---|---|
| `type: http, scheme: bearer`, `oauth2`, `openIdConnect` | `Authorization: Bearer <token>` |
| `type: apiKey, in: header, name: X-API-Key` | `X-API-Key: <token>` |
| `type: apiKey, in: query, name: api_key` | `?api_key=<token>` |
| `type: apiKey, in: cookie, name: session` | `Cookie: session=<token>` |

The token source is **asked before every request**, so it can refresh tokens:

```go
// A fixed token
cfg.Token = apitest.StaticToken(os.Getenv("API_TOKEN"))

// Log in once, refresh when the token is about to expire
var (
	mu  sync.Mutex
	tok string
	exp time.Time
)
cfg.Token = apitest.TokenFunc(func(ctx context.Context) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if time.Until(exp) < time.Minute {
		var err error
		if tok, exp, err = login(ctx, "ci-user", os.Getenv("CI_PASSWORD")); err != nil {
			return "", err // aborts the run with this error
		}
	}
	return tok, nil
})
```

Details that save debugging time:

- **Expired JWTs are caught first.** A static JWT whose `exp` lies in the past aborts the run with one clear error instead of a 401 for every case.
- **Pasted tokens are cleaned.** Whitespace, line breaks and a leading `Bearer ` are removed, and a warning says so.
- **Specs without security still get the token.** If the spec declares no `security` at all, the token is sent as bearer token to every operation, with a warning. Declare `securitySchemes` and `security` to make this explicit.
- **Tokens never appear in the report.** Real and manipulated tokens are replaced by `***`, and `curl` commands use `$TOKEN`, `$INVALID_TOKEN` and `$FORBIDDEN_TOKEN`.

### Authentication cases

Every operation that requires a token and documents 401 gets two extra cases:

| Case | Sent with | Expects |
|---|---|---|
| `unauthorized` | no token | 401 |
| `invalid-token` | a manipulated token: by default the first 100 characters of the real one (see `TamperToken`) | 401 (or 403 if only 403 is documented) |
| `forbidden` | `Config.ForbiddenToken` | 403; only for operations with `x-apitest-forbidden: true` |

These cases run while the resource still exists, right before the DELETE. If an unauthorized DELETE succeeds, that case fails, and the regular DELETE is skipped with a reference to it. If the API answers an authentication case with 2xx, the message says what was sent, for example that the API does not verify token signatures.

### `ForbiddenToken TokenSource`

This is a valid token with too few rights. Without it, `forbidden` cases are `SKIPPED`.

```go
cfg.Token = apitest.StaticToken(adminToken)          // read and write
cfg.ForbiddenToken = apitest.StaticToken(readerToken) // read only
```

```yaml
/organizations/{organizationId}:
  delete:
    operationId: deleteOrganization
    x-apitest-forbidden: true          # readers must not delete
    responses:
      "204": { description: deleted }
      "401": { description: no or invalid token }
      "403": { description: not allowed }
```

### `TamperToken func(token string) string`

This derives the token of the `invalid-token` case from the real one.

| Value | Sends | Tests |
|---|---|---|
| `nil` = `apitest.TruncateToken` (default) | the first 100 characters of the real token, or its first half if it is shorter; a JWT loses its signature and the end of its payload | that nothing on the way accepts a broken token, including APIs that only decode tokens because a gateway validates them |
| `apitest.TamperSignature` | the real JWT with the same header and claims plus `"apitest": "invalid-token"`, and an inverted signature; other tokens get every second character changed | that the signature is really verified: the token decodes fine, only the signature is wrong |
| your own function | anything, e.g. an expired token or a token of another issuer | whatever your security model needs |

```go
cfg.TamperToken = apitest.TamperSignature

// or: a token of the right form, but signed by another key
cfg.TamperToken = func(real string) string { return signWithForeignKey(claimsOf(real)) }
```

The result must differ from the real token and must not be empty (that is the `unauthorized` case); otherwise the case is an `ERROR`. The manipulated token is redacted like the real one, and `curl` commands show `$INVALID_TOKEN`.

### `SkipAuthCases []string`

These authentication cases are left out **completely**: they are not sent, not reported and do not appear as subtests, so `go test -v` and IDEs do not list them as skipped. Valid names are `apitest.AuthUnauthorized`, `apitest.AuthInvalidToken` and `apitest.AuthForbidden`. An unknown name aborts the run with an error.

A typical use: locally the app runs without the gateway that validates tokens, and the app itself only decodes them. There `invalid-token` cannot succeed, while on dev, behind the gateway, it must.

```go
if os.Getenv("API_ENV") == "local" {
	cfg.SkipAuthCases = []string{apitest.AuthInvalidToken}
}
```

The command line can do the same for one run, but `-skip` shows the cases as skipped:

```sh
go test ./... -skip 'TestAPI/.*/.*/^invalid-token$'
go test ./... -skip 'TestAPI/.*/.*/^(unauthorized|invalid-token|forbidden)$'
```

---

## 3. Selecting what runs

Each case is a Go subtest named `<Tag>/<operationId>/<example>`, for example `TestAPI/Organization/createOrganization/default`. Operations are grouped by their first tag.

### `Tags []string`

This runs only these groups, **in this order**. Groups run after the groups they need values from. If the order in `Tags` contradicts the bindings, the run stops before the first request with an explanation.

```go
cfg.Tags = []string{"Organization", "Workflow", "Deployment"}
```

### `IncludeOps []string` and `ExcludeOps []string`

These select single operations by `operationId` or `"<METHOD> <path>"`. Unknown entries are an error, so typos cannot silently test nothing.

```go
cfg.IncludeOps = []string{"createOrganization", "GET /organizations/{organizationId}"}
cfg.ExcludeOps = []string{"POST /organizations/{organizationId}/export"} // slow, runs nightly
```

`Tags`, `IncludeOps` and `ExcludeOps` combine: an operation must pass all three filters.

### Preconditions run automatically

When you select a case, the cases it needs values from also run and are marked as preconditions, whether you select via `Tags`, `IncludeOps` or `go test -run`.

```sh
# runs createOrganization first, because getOrganization needs its id
go test -run 'TestAPI/Organization/getOrganization/default'
```

### Execution order

The default order is:

- **Groups:** a group runs after the groups it takes values from. Groups without such a dependency follow `Tags`, otherwise the alphabet.
- **Within a group:** create (POST), read by key (GET `/x/{id}`), list (GET `/x`), update (PUT/PATCH), negative examples, authentication cases, delete (DELETE). `x-apitest-order` refines the order of cases with the same rank.
- **Bindings win:** a case always runs after the case it needs a value from.
- **Deferred DELETEs:** the DELETEs of a group that other groups depend on run at the very end, in reverse order.

#### `MethodOrder []string`

This changes the order of the regular cases within a group by method:

```go
cfg.MethodOrder = []string{"POST", "PUT", "GET"} // read after the update
```

- **Unlisted methods** follow in their default order.
- **DELETE** always runs last and may only be the last entry. A DELETE in front of other methods removes what they need, so `{"DELETE", "GET"}` is rejected.
- **Bindings still win:** with `{"PUT", "POST"}`, the PUT still waits for the POST that creates its id.
- **Expected values change:** a GET after the PUT returns the updated data, not the GET example. Combine it with `CompareSchema` or with examples that describe the updated state.

#### `DeleteLast bool`

This runs the DELETE cases of **all** groups after every other case, in reverse group order:

```go
cfg.DeleteLast = true
```

By default, a group's DELETEs only wait for groups that depend on it **through bindings**. If a later group uses the same record through `Params` (seed data), a DELETE that runs earlier removes it, and every following request fails with 404. `DeleteLast` prevents that. On shared environments also check whether the DELETE cases should run at all: with seed values from `Params` they delete real data, and `ExcludeOps` or `x-apitest-skip` keeps them out.

#### `NumberCases bool`

This prefixes every subtest name with its position in the run:

```text
TestAPI/01_Organization/createOrganization/valid
TestAPI/02_Organization/getOrganization/default
…
TestAPI/17_Organization/deleteOrganization/default
```

IDEs that sort subtests alphabetically then show the execution order. The report (`017 Organization/…`), `Result.Cases[i].Number` and the JSON report carry the same number.

- **Stable numbers:** they come from the full run without `-run`, so a case keeps its number when you run it alone: `go test -run 'TestAPI/07_'`.
- **Case names unchanged:** deviations entries, hooks (`Case.Name`) and `Result` names stay without the number, so a new operation that shifts all numbers breaks nothing.
- **`-run` patterns:** unanchored patterns like `TestAPI/Organization` still match. Anchored patterns like `TestAPI/^Organization$` need the number, for example `TestAPI/^\d+_Organization$`.

---

## 4. Parameters and headers

### Where parameter values come from

For each parameter apitest takes the first source that has a value:

1. a **binding**: a value from an earlier response (`x-apitest-bind`, OpenAPI `links`, or the heuristic)
2. **`Config.Params`**
3. the parameter's named example with the case's name
4. the parameter's `example`
5. the schema's `example`
6. the schema's `default`
7. the first `enum` value

Optional query parameters are only sent if one of the first four sources has a value. A required parameter without any value makes the case `NOT_BUILDABLE`, and the message names the parameter.

### `Params map[string]string`

These are fixed values by parameter name, for seed data, tenants and anything the spec has no example for.

```go
cfg.Params = map[string]string{
	"tenant": "t-42",   // header, query or path parameter named tenant
	"limit":  "50",
	"region": "eu-central",
}
```

A key of the form **`"<operationId>.<name>"`** applies to one operation only and takes precedence over the plain name. That is what a generic name like `id` needs:

```go
cfg.Params = map[string]string{
	"id":                   "1",      // fallback for every other "id"
	"getOrganization.id":   "org-1",
	"getDeployment.id":     "dep-7",
	"listAuditEvents.from": "2026-01-01",
}
```

A prefixed key that matches no parameter produces a warning, so a renamed operation does not go unnoticed.

`Params` also rescues **heuristic bindings that come back empty**. Suppose the heuristic guessed that `missionCode` comes from the body of `createMission`, the producer succeeded, but the field is missing. If `Params` has `missionCode`, that value is used with a warning, and the dependent cases are not skipped. Explicit bindings and failed producers are never replaced.

### `Headers map[string]string`

These headers are added to **every** request, including GET checks.

```go
cfg.Headers = map[string]string{
	"X-Tenant":        "t-42",
	"Accept-Language": "de-DE",
}
```

For headers on single cases, use `Hooks.BeforeRequest` ([section 8](#8-hooks)). A global `Accept` would otherwise apply to JSON endpoints too.

---

## 5. Comparing responses

Every response goes through three stages. Each stage runs only if the previous one passed:

1. **Status code.** It must be the expected one. An undocumented code fails even if it is 2xx.
2. **Schema.** Types, formats (`uuid`, `email`, `date`, `date-time`, …), required fields, `additionalProperties`, `enum`, patterns (ECMA-262, including lookahead), headers and content type.
3. **Example.** The response is compared with the expected example of the spec.

### `CompareMode CompareMode`

| Mode | Stage 3 checks | Good for |
|---|---|---|
| `apitest.CompareSubset` (default) | every field of the example is present with the same value; extra fields are fine | APIs whose examples are real data |
| `apitest.CompareExact` | the body equals the example, apart from ignored fields | small, stable responses |
| `apitest.CompareSchema` | nothing beyond the schema | real environments whose data differ from the examples |

```go
cfg.CompareMode = apitest.CompareSchema // contract check only
```

The mode can also be set in the spec per operation, per response or per example. The most specific one wins:

```yaml
/organizations:
  get:
    operationId: listOrganizations
    x-apitest-compare: schema            # the list depends on other tests
  post:
    operationId: createOrganization
    responses:
      "201":
        x-apitest-compare: exact
```

### `IgnoreFields []string`

These fields are excluded from the value comparison, both in stage 3 and in the GET check after writes. There are two forms:

```go
cfg.IgnoreFields = []string{
	"id", "createdAt", "updatedAt", // a plain name matches at every level
	"/items/*/etag",                // a JSON pointer; * matches one element
	"/meta/requestId",
}
```

Per operation or response the same works in the spec with `x-apitest-ignore: [createdAt]`.

### Arrays in any order

Arrays are compared by position unless the response says otherwise:

```yaml
responses:
  "200":
    x-apitest-compare-unordered: true
```

---

## 6. Accepted deviations and strict mode

### `DeviationsPath string`

This is a YAML or JSON file with deviations you accept for now. The case is then reported as `DEVIATION` instead of failing. Every entry needs a reason and an expiry date, so the list cannot rot. Matching is exact: an entry only applies to its case and to exactly this status code or result status.

```go
cfg.DeviationsPath = "apitest_deviations.yaml"
```

What a matching entry does:

| Where | Without entry | With matching entry |
|---|---|---|
| `go test`, GoLand, VS Code | the subtest fails | the subtest **passes**; its log says `DEVIATION: accepted deviation: <reason>, valid until …: <original message> (was FAILED)` |
| Report | under "❌ Errors" | under "🟡 Deviations", with the original differences and the entry that covered it |
| `Result` | `StatusFailed`, … | `StatusDeviation`; `Result.Failed` stays false if nothing else fails |

A deviation does not hide the problem: the case is still sent and checked, and the report still shows every difference. It only stops this known problem from failing the build until the entry expires.

If an entry names a case but accepts another result, the failure message says so:

```text
FAILED: status code 404, expected 200; deviation entry 3 names this case but accepts 200 → 400, the result is 200 → 404
```

Case names can be copied from `go test -v` or the IDE as they are. A leading test name (`TestAPI/`), the position prefix of `NumberCases` (`007_`) and spaces (`Space Station` → `Space_Station`) are handled.

The file offers these variants:

```yaml
# 1. a wrong status code: expected and actual
- case: Organization/deleteOrganization/default
  expected: 204
  actual: 200
  reason: "API returns 200 with the deleted object; fix planned"
  ticket: API-431                  # optional, shown in the report
  expires: 2026-12-31

# 2. a result status that is not about the status code
- case: Deployment/getDeployment/default
  status: SCHEMA_VIOLATION
  reason: "startedAt is sent without time zone"
  expires: 2026-11-30

# 3. only differences at exactly this JSON pointer
- case: Workflow/getWorkflow/default
  status: EXAMPLE_MISMATCH
  pointer: /steps/0/timeout
  reason: "default timeout changed from 30s to 60s; spec update pending"
  expires: 2026-10-31

# 4. a pattern: * matches within one name element
- case: Deployment/*/unauthorized
  expected: 401
  actual: 403
  reason: "gateway answers 403 for missing tokens"
  expires: 2026-12-31
```

The rules:

- **Status codes or result status.** Set either `expected` and `actual` (status codes) or `status` (`SCHEMA_VIOLATION`, `EXAMPLE_MISMATCH`, `DATA_MISMATCH`, …), not both.
- **`pointer`** is only allowed together with `status`.
- **Invalid entries** stop the run with their position in the file.
- **Expiry.** 14 days before `expires`, the run warns. After `expires`, the entry still matches, but the case is marked as "EXPIRED deviation", and with `Strict` it fails the test.
- **Unused entries** are listed as "unused, can be removed" in the report.
- **Matching is exact.** `expected`/`actual` only cover `FAILED` cases with exactly these codes. A case that fails with `SCHEMA_VIOLATION` needs `status: SCHEMA_VIOLATION`, even if the status code was wrong as well.

### `Strict bool`

Without `Strict`, `NOT_BUILDABLE` cases and cases covered only by an expired deviation are reported but do not fail the test. With `Strict`, they fail it. apitest does not detect CI environments, the project decides:

```go
cfg.Strict = os.Getenv("CI") != ""
```

| Status | Fails the test |
|---|---|
| `PASSED`, `DEVIATION`, `SKIPPED` | no |
| `FAILED`, `SCHEMA_VIOLATION`, `EXAMPLE_MISMATCH`, `DATA_MISMATCH`, `ERROR` | yes |
| `NOT_BUILDABLE`, `DEVIATION` from an expired entry | only with `Strict` |

---

## 7. Reports

After every group, apitest writes a Markdown report, so an aborted run still leaves one. Its sections are collapsible blocks (`<details>`), and the title of each block shows its count. Warnings, errors and deviations are open, while coverage, spec findings, cases that did not run and passed cases are closed. It contains:

- a summary
- the coverage of operations and named examples
- the spec findings
- every failed case with differences, schema errors, request and response bodies, the response headers (also when there is no body), and a `curl` command with the headers as sent, including those from `Hooks.BeforeRequest`
- the deviations and the passed cases

| Field | Effect |
|---|---|
| `ReportPath string` | Where the Markdown report goes. Default: `apitest-report/<test name>.md` in the test package directory. |
| `ReportJSON bool` | Also writes the results as JSON next to it (`.json`), for dashboards or your own tooling. |
| `OmitBodies bool` | Leaves request and response bodies out, e.g. for personal data. |
| `Redact []string` | Additional field names whose values become `***`. Tokens, `Authorization`/`Cookie`/`Set-Cookie` headers, API keys in URLs, `writeOnly` fields and fields named like `password`, `secret`, `token` or `apiKey` are always redacted. Header values are also masked if the header name contains one of these names or `signature`, `session` or `cookie` (`X-Api-Key`, `X-Signature`). |
| `DisableReports bool` | Writes no files at all; results only go to `go test` and the returned `Result`. Cannot be combined with `ReportPath` or `ReportJSON`. |
| `ReportPassedDetails bool` | Shows passed cases with request, response, response headers and `curl` command like failed ones, instead of one table row each. Useful to document a run or to compare it with another tool; `OmitBodies` and redaction still apply. |
| `DisableWarnings bool` | Drops all warnings and the spec findings, in the `go test` output and in the report, e.g. the hint about specs without security on every run. Note that this also hides useful hints like an expiring token, an expiring deviation or a heuristic binding. Errors and failed cases are not affected. |

```go
cfg.ReportPath = filepath.Join(os.Getenv("ARTIFACTS"), "api-contract.md")
cfg.ReportJSON = true
cfg.Redact = []string{"iban", "birthDate", "email"}
cfg.ReportPassedDetails = os.Getenv("REPORT_ALL") != ""
cfg.DisableWarnings = true
```

---

## 8. Hooks

Hooks are extension points for everything apitest cannot know about your system. An error or a panic in a hook only affects its case or group.

### `BeforeGroup` and `AfterGroup`

These run around the cases of one resource group (tag). An error in `BeforeGroup` skips the group.

```go
cfg.Hooks.BeforeGroup = func(ctx context.Context, group string) error {
	return db.Truncate(ctx, tablesOf[group]...) // every group starts from seed data
}
cfg.Hooks.AfterGroup = func(ctx context.Context, group string) error {
	t.Logf("group %s done", group)
	return nil
}
```

### `BeforeRequest`

This may change the request before it is sent. An error marks the case as `ERROR`. The `*apitest.Case` tells you which case is running: `Name`, `Group`, `OperationID`, `Method`, `Path` (the template) and `Example`.

```go
cfg.Hooks.BeforeRequest = func(ctx context.Context, c *apitest.Case, req *http.Request) error {
	// idempotency key per case
	req.Header.Set("Idempotency-Key", c.Name)

	// the 401 of the gateway is plain text
	if c.Example == apitest.AuthUnauthorized || c.Example == apitest.AuthInvalidToken {
		req.Header.Set("Accept", "text/plain")
	}

	// request signing that depends on the final URL and body
	return signer.Sign(req)
}
```

### `AfterResponse`

This runs after all built-in checks have passed and adds your own. An error marks the case as `FAILED`.

```go
cfg.Hooks.AfterResponse = func(ctx context.Context, c *apitest.Case, resp *apitest.Response) error {
	if resp.Header.Get("X-Request-Id") == "" {
		return errors.New("missing X-Request-Id")
	}
	if c.OperationID == "createOrganization" {
		var body struct{ ID string }
		_ = json.Unmarshal(resp.Body, &body)
		return db.ExpectAuditEntry(ctx, "organization.created", body.ID)
	}
	return nil
}
```

---

## 9. The returned Result

`Run` returns a `*apitest.Result` for your own assertions after the run:

```go
res := apitest.Run(t, cfg)

// coverage gate
if res.Summary.OperationsCovered < res.Summary.Operations {
	t.Errorf("only %d of %d operations were executed", res.Summary.OperationsCovered, res.Summary.Operations)
}

// no case may be slower than 2 s
for _, c := range res.Cases {
	if c.Duration > 2*time.Second {
		t.Errorf("%s took %s", c.Name, c.Duration)
	}
}

t.Logf("report: %s, counts: %v", res.Report, res.Summary.Counts)
```

| Field | Content |
|---|---|
| `Cases []CaseResult` | `Name`, `Group`, `Operation`, `Example`, `Status`, `Message` (never contains tokens), `StatusCode`, `Duration`, `Precondition` |
| `Summary` | `Counts` per status, `Total`, `Duration`, `Operations`, `OperationsCovered`, `NotSelected` (filtered by `-run`) |
| `Report` | absolute path of the Markdown report, `""` if none was written |
| `Failed` | whether the run failed the test |

---

## 10. Spec extensions

Some rules belong to the API and not to one test run. They live in the spec as `x-apitest-*` extensions, which other tools ignore.

| Extension | Where | Effect |
|---|---|---|
| `x-apitest-bind: {from, pointer}` | parameter | take the value from a field of `from`'s response |
| `x-apitest-bind: {from, header}` | parameter | take it from a response header; for `Location` the last path segment |
| `x-apitest-bind: {from, pointer, source: request}` | parameter | take it from the body `from` sent |
| OpenAPI `links` | response | the standard way; `$response.body#/id`, `$response.header.Location`, `$request.body#/code` and constants |
| `x-apitest-compare: subset\|exact\|schema` | operation, response, example | comparison mode ([section 5](#5-comparing-responses)) |
| `x-apitest-compare-unordered: true` | response | arrays in any order |
| `x-apitest-ignore: [field, /pointer]` | operation, response | fields excluded from comparison |
| `x-apitest-skip: "reason"` | operation, example | case is `SKIPPED` with the reason |
| `x-apitest-order: 10` | operation | order among cases of the same rank in a group |
| `x-apitest-verify: false` | operation | no GET check after the write |
| `x-apitest-verify: {poll: true, timeout: 30s}` | operation | repeat the GET check until it succeeds, for asynchronous processing |
| `x-apitest-forbidden: true` | operation | generate the `forbidden` case |

A spec that uses most of them:

```yaml
paths:
  /organizations:
    post:
      operationId: createOrganization
      tags: [Organization]
      requestBody:
        content:
          application/json:
            examples:
              valid:   { value: { name: Acme, plan: pro } }
              no-name: { value: { plan: pro } }          # a negative case
      responses:
        "201":
          content:
            application/json:
              examples:
                valid: { value: { id: org-1, name: Acme, plan: pro } }
          links:
            getOrganization:
              operationId: getOrganization
              parameters: { organizationId: '$response.body#/id' }
        "400":
          content:
            application/json:
              examples:
                no-name: { value: { error: name is required } }  # same name → expects 400

  /organizations/{organizationId}/deployments:
    post:
      operationId: createDeployment
      tags: [Deployment]
      x-apitest-verify: { poll: true, timeout: 30s }   # deployments are created asynchronously
      parameters:
        - name: organizationId
          in: path
          required: true
          schema: { type: string }
          x-apitest-bind: { from: createOrganization, pointer: /id }

  /organizations/{organizationId}/audit-events:
    get:
      operationId: listAuditEvents
      tags: [Organization]
      x-apitest-compare: schema
      x-apitest-ignore: [timestamp]
      responses:
        "200":
          x-apitest-compare-unordered: true

  /organizations/{organizationId}/export:
    post:
      operationId: exportOrganization
      x-apitest-skip: "takes several minutes; runs in the nightly job"
```

A request example and a response example with the **same name** form a case. If that response is a 4xx, the case is a negative test, and no extra code is needed.

---

## 11. Recipes

### One test, several environments

```go
func TestAPI(t *testing.T) {
	env := cmp.Or(os.Getenv("API_ENV"), "local")
	cfg := apitest.Config{
		SpecPath:       "openapi.yaml",
		BaseURL:        cmp.Or(os.Getenv("API_URL"), "http://127.0.0.1:8080/v1"),
		Token:          apitest.StaticToken(os.Getenv("API_TOKEN")),
		Params:         seedFor(env), // seed data differ per environment
		DeviationsPath: "deviations." + env + ".yaml",
		HealthPath:     "/healthz",
		Strict:         os.Getenv("CI") != "",
	}
	switch env {
	case "local":
		cfg.SkipAuthCases = []string{apitest.AuthInvalidToken} // no token-checking gateway
	case "dev":
		cfg.AllowedHosts = []string{"api.dev.internal"}
		cfg.CompareMode = apitest.CompareSchema // real data, not examples
		cfg.Hooks.BeforeGroup = cleanupTestData
	case "staging":
		// read-only smoke test: no AllowedHosts, so writes are refused
		cfg.CompareMode = apitest.CompareSchema
	}
	apitest.Run(t, cfg)
}
```

### In-process with a fresh database per group

```go
func TestAPI(t *testing.T) {
	db := testdb.New(t) // your helper, e.g. an in-memory SQLite
	apitest.Run(t, apitest.Config{
		SpecPath: "../api/openapi.yaml",
		Handler:  server.New(db),
		Token:    apitest.StaticToken(testToken),
		Hooks: apitest.Hooks{
			BeforeGroup: func(ctx context.Context, _ string) error { return db.Reset(ctx) },
		},
		DisableReports: testing.Short(),
	})
}
```

### Contract check only (no data, no writes)

```go
apitest.Run(t, apitest.Config{
	SpecPath:    "openapi.yaml",
	BaseURL:     "https://api.example.com/v1",
	Token:       apitest.StaticToken(os.Getenv("API_TOKEN")),
	CompareMode: apitest.CompareSchema,
	// no AllowedHosts: POST/PUT/PATCH/DELETE are refused, GETs are checked
})
```

### Split a large spec into several runs

Each run gets its own subtest tree and report, and failures stay easy to find.

```go
func TestOrganizations(t *testing.T) { apitest.Run(t, base(t, "Organization", "Member")) }
func TestDeployments(t *testing.T)   { apitest.Run(t, base(t, "Deployment")) }

func base(t *testing.T, tags ...string) apitest.Config {
	return apitest.Config{
		SpecPath:   "openapi.yaml",
		Handler:    newApp(t),
		Token:      apitest.StaticToken(testToken),
		Tags:       tags,
		ReportPath: "apitest-report/" + strings.Join(tags, "-") + ".md",
	}
}
```

Producers from other tags still run as preconditions, so a run for `Deployment` creates the organizations it needs.

### Useful `go test` invocations

```sh
go test ./... -run TestAPI                                  # everything
go test ./... -run 'TestAPI/Deployment'                     # one group (plus preconditions)
go test ./... -run 'TestAPI/.*/createOrganization/valid'    # one case
go test ./... -run TestAPI -skip 'TestAPI/.*/.*/^invalid-token$'
go test ./... -run TestAPI -v                               # every case with its status
go test ./... -run TestAPI -timeout 5m                      # remaining cases SKIPPED at the deadline
```

---

## 12. All fields at a glance

| Field | Type | Default | Section |
|---|---|---|---|
| `SpecPath` | `string` | required | – |
| `BaseURL` | `string` | – (or `Handler`) | [1](#1-where-the-api-runs) |
| `Handler` | `http.Handler` | – (or `BaseURL`) | [1](#1-where-the-api-runs) |
| `Token` | `TokenSource` | none; required if the spec uses security | [2](#2-authentication) |
| `ForbiddenToken` | `TokenSource` | none; `forbidden` cases are skipped | [2](#2-authentication) |
| `SkipAuthCases` | `[]string` | none | [2](#2-authentication) |
| `TamperToken` | `func(string) string` | `TruncateToken` | [2](#2-authentication) |
| `Tags` | `[]string` | all, alphabetical | [3](#3-selecting-what-runs) |
| `IncludeOps` | `[]string` | all | [3](#3-selecting-what-runs) |
| `ExcludeOps` | `[]string` | none | [3](#3-selecting-what-runs) |
| `MethodOrder` | `[]string` | POST, GET, PUT/PATCH, DELETE | [3](#execution-order) |
| `DeleteLast` | `bool` | `false` | [3](#execution-order) |
| `NumberCases` | `bool` | `false` | [3](#execution-order) |
| `Params` | `map[string]string` | none | [4](#4-parameters-and-headers) |
| `Headers` | `map[string]string` | none | [4](#4-parameters-and-headers) |
| `IgnoreFields` | `[]string` | none | [5](#5-comparing-responses) |
| `CompareMode` | `CompareMode` | `CompareSubset` | [5](#5-comparing-responses) |
| `Strict` | `bool` | `false` | [6](#6-accepted-deviations-and-strict-mode) |
| `DeviationsPath` | `string` | none | [6](#6-accepted-deviations-and-strict-mode) |
| `RequestTimeout` | `time.Duration` | 10 s | [1](#1-where-the-api-runs) |
| `HealthPath` | `string` | any response on `/` | [1](#1-where-the-api-runs) |
| `AllowedHosts` | `[]string` | localhost and loopback only | [1](#1-where-the-api-runs) |
| `AllowRemoteWrites` | `bool` | `false` | [1](#1-where-the-api-runs) |
| `HTTPClient` | `*http.Client` | a default client | [1](#1-where-the-api-runs) |
| `DisableReports` | `bool` | `false` | [7](#7-reports) |
| `ReportPath` | `string` | `apitest-report/<test name>.md` | [7](#7-reports) |
| `ReportJSON` | `bool` | `false` | [7](#7-reports) |
| `OmitBodies` | `bool` | `false` | [7](#7-reports) |
| `ReportPassedDetails` | `bool` | `false` | [7](#7-reports) |
| `DisableWarnings` | `bool` | `false` | [7](#7-reports) |
| `Redact` | `[]string` | built-in list only | [7](#7-reports) |
| `Hooks` | `Hooks` | none | [8](#8-hooks) |

Relative paths (`SpecPath`, `DeviationsPath`, `ReportPath`) are resolved against the test package directory, like any file access in a Go test. Configuration mistakes stop the run before the first request, and each message says what is wrong and how to fix it.
