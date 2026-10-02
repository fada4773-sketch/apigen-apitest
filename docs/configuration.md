# Configuration reference

`apitest.Run(t, apitest.Config{...})` is the only entry point of the library (`import "github.com/fada4773-sketch/apigen-apitest/apitest"`). This page explains every field of `Config`, the file formats and spec extensions it works with, and shows how they combine. The zero value of every field is a sensible default. Only `SpecPath` and one of `BaseURL` or `Handler` are required.

apitest needs examples in the spec. If your spec has none or only a few, the command line tool `apitest-gen` writes them; its commands, flags and files are described in [section 13](#13-apitest-gen). [workflow.md](workflow.md) shows both tools together on a complete fictional project.

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
13. [apitest-gen](#13-apitest-gen)

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

---

## 13. apitest-gen

`apitest-gen` prepares a spec for apitest. It keeps a dictionary of example values, writes missing or invalid examples into the spec, fetches seed values from a running environment and checks whether apitest can send every case. It works for any OpenAPI 3.x file, whatever language the API is written in.

```sh
go install github.com/fada4773-sketch/apigen-apitest/cmd/apitest-gen@latest
```

For a step-by-step example, see [workflow.md](workflow.md).

### 13.1 Commands

| Command | Does |
|---|---|
| `apitest-gen [apply] -spec …` | **default.** Updates the dictionary (creates it on the first run), then writes missing or invalid examples into the spec, in place or to `-out`. With `-base-url` the sources in the defaults are fetched first; with `-check` the written spec is checked afterwards. |
| `apitest-gen dict -spec …` | Only creates or updates the dictionary. |
| `apitest-gen discover -defaults … -base-url …` | Fetches the sources of the defaults (`{"from": "GET …", "pick": …}`) and writes the values to `-out`. |
| `apitest-gen check -spec …` | Reports every case apitest could not send and every example that violates its schema. Writes nothing. |
| `apitest-gen review -spec …` | Evaluates everything apitest and apitest-gen would report: spec findings, cases that cannot be sent, values that cannot be generated, wrong defaults. Proposes a fix for each and writes the proposals to `-out` for you to review (see [13.9](#139-reviewing-findings)). Changes neither spec nor dictionary. |
| `apitest-gen help` | Shows all commands and flags. |

Exit codes: `0` success, `1` a problem (fatal defaults, `check` findings, a file that cannot be read or written), `2` wrong usage.

### 13.2 Flags

| Flag | Default | Commands | Meaning |
|---|---|---|---|
| `-spec <file>` | – | apply, dict, check, review | OpenAPI 3.0/3.1 file, YAML or JSON. **Required.** |
| `-dict <file>` | `global-dict.json` | apply, dict, review | The dictionary; created if it does not exist. |
| `-defaults <files>` | `defaults.json` | apply, discover, check, review | One or more defaults files, comma-separated; later files override earlier ones. A missing file counts as empty, a broken one is an error. |
| `-out <file>` | in place | apply | Write the spec there instead of over `-spec`. |
| `-out <file>` | `defaults.resolved.json` | discover | File for the fetched values. |
| `-out <file>` | `defaults.suggested.json` | review | File for the proposals; overwritten on every run. |
| `-seed <n>` | `42` | apply, dict, review | Seed for **new** values. The same seed and field always give the same value; existing values are never touched by the seed. |
| `-repair` | off | apply, dict | Regenerate dictionary values that no longer fit their schema. Without it they are kept and reported. |
| `-overwrite` | off | apply | Replace valid existing examples too, not only missing or invalid ones. Named `examples` are never replaced. |
| `-generic-ids <names>` | `id,uuid,key` | apply, review | Path parameter names that mean another resource on every path (see [13.5](#135-generic-path-ids)). |
| `-check` | off | apply | Check the written spec afterwards; exit code 1 on problems. |
| `-base-url <url>` | – | apply, discover | Environment to fetch sources from. |
| `-token-env <name>` | – | apply, discover | Environment variable with a bearer token for `-base-url`. The token is never printed. |
| `-header "Name: value"` | – | apply, discover | Extra header for `-base-url`; repeatable. |
| `-dry-run` | off | apply, dict, review | Show what would change; write neither spec nor dictionary (review: no proposals file). |
| `-v` | off | apply, dict, review | review: also print the proposed fix under each finding. apply, dict: verbose: list every change (`VALUE_NEW`, `EXAMPLE_ADDED`, `DEFAULTS_APPLIED`, …) and at how many places each default matched (also places that already have the value, so the count repeats on every run), not only problems. |

### 13.3 Where examples are written

apitest-gen writes where apitest reads:

| Place | Written as |
|---|---|
| Parameters (path, query, header, cookie) on operation and path level | `parameter.example`. A parameter that is a `$ref` is written at its target in `components.parameters`, once. |
| Request bodies (`application/json`, `*+json`, `application/x-www-form-urlencoded`) | `content[mt].example`, at the `components.requestBodies` target for a `$ref`. |
| 2xx and `default` responses | `content[mt].example`, at the `components.responses` target for a `$ref`. |
| Other responses (4xx, 5xx) | an existing `example` that violates its schema is replaced; missing ones are not added. |
| `components.schemas.*` | an existing `example` (or an item of the 3.1 `examples` list) that violates its schema is replaced; missing ones are not added. |
| Named `examples` | never replaced; only values from the defaults are set in the fields they already have. An invalid one is reported as `EXAMPLE_NAMED_INVALID`, except request examples paired with a 4xx response, which are negative tests. `x-example-defaults: false` on an example leaves it alone. |

apitest validates every example it finds, so after a run of apitest-gen every example it wrote or kept fits its schema; only curated named examples can still be invalid, and they are reported. It never writes next to a `$ref` and never adds `example` next to `examples` (OpenAPI forbids both together). Response headers are left as they are. Read-only fields stay out of request examples, write-only fields out of response examples.

Comments, key order and block style of the YAML are kept; JSON files stay JSON. Before the original is replaced, the written file is loaded again with apitest's loader; if that fails, the original stays unchanged. A second run with unchanged inputs changes nothing.

### 13.4 Where a value comes from

For every place, the first source with a value wins:

1. **`defaults.json`**: a matching key (13.6), adjusted to the type if needed. It wins over everything, also inside existing examples.
2. **An existing example** that fits its schema (unless `-overwrite`).
3. **The dictionary**: the value of this DTO field or parameter.

Values in the dictionary are created once and then kept:

1. `const`, `default`, then an `enum` value
2. a value for the `format` (`uuid`, `date`, `date-time`, `time`, `email`, `uri`, `hostname`, `ipv4`, `ipv6`, `byte`, `password`)
3. a readable value for the field name and its DTO: names, cities, zip codes, phone numbers, versions, sentences for descriptions, "Brave Garden" for the `Name` of a `Garden`. Words are matched as whole words: `ReportCode` is a code, not a city.
4. a string built from the `pattern` (ECMA-262, lookaheads included) and checked against the original pattern the way apitest validates
5. a type-based value that meets `minimum`/`maximum`, `exclusive*`, `multipleOf`, `minLength`/`maxLength`, `minItems`/`maxItems` and `uniqueItems`
6. for a **free object** (`type: object` or no type, without `properties`): `{}`, which fits every such schema. With `additionalProperties: {type: …}` it gets one entry of that type (`{"key1": 42}`), with `minProperties` that many entries, and keys listed in `required` are filled. Set a real structure in the dictionary or the defaults if the API expects one.

A field gets **no value** (`NO_VALUE`, `PATTERN_PENDING`, `TYPE_CONFLICT`) only if no value can meet its constraints, for example `minProperties: 1` together with `additionalProperties: false`, a contradictory pattern, or `type: object` combined with an enum.

Every value is validated against its schema before it is used. Compound names share one value across DTOs and parameters (`dockCode` and `DockCode`), generated for the strictest place first, so a parameter with a pattern and a field without one get the same value. Generic names (`Name`, `Id`, `Version`) get a value per DTO.

### 13.5 Generic path ids

A path parameter named `id` (or `uuid`, `key`, see `-generic-ids`) means another resource on every path. Its value comes from the first match of:

1. `"<operationId>.id"` (not for parameters shared by several operations)
2. the path key, e.g. `"/ships/{id}"`
3. the resource derived from the path segment in front: `/ships/{id}` → `shipId`, `/categories/{id}` → `categoryId`, `/space-ports/{id}` → `spacePortId`
4. `"<ResponseDTO>.id"`, the DTO of the lowest 2xx response
5. the plain key `"id"`
6. the value kept for this path in the dictionary (`paths`), or a newly generated one that is kept there

When apitest can take the id from a POST at run time (a binding), that value wins anyway. The example only matters for operations without a producer.

### 13.6 defaults.json

A JSON object. Keys are compared without regard to case; two keys that differ only in case are an error. Keys starting with `$` are comments.

| Key | Example | Applies to |
|---|---|---|
| `#/components/schemas/Dto` | `"#/components/schemas/Pilot": {"Name": "Ada"}` | the DTO itself: its schema example and every place that holds it (see below) |
| `Name` | `"PilotEmail": "test@starport.example"` | every field and parameter with this name, in every DTO, request, response and named example |
| `Dto.Name` | `"ShipWrite.Name": "Test Ship"` | the field in this DTO (the nearest enclosing DTO counts) |
| `operationId.Name` | `"updateShip.Name": "Renamed"` | parameters and body fields of this operation |
| `/path/{param}` | `"/ships/{id}": 7` | the generic path parameter of this path |
| `operationId.x-name` | `"scrapShip.x-apitest-verify": false` | an extension on the operation |
| `operationId.param` with `bind` | `"bookDock.dockCode": {"bind": "listDocks", "pointer": "/0/DockCode"}` | where apitest takes the parameter from at run time |
| any key with `from` | `"DockCode": {"from": "GET /docks", "pick": "/[Active=true]/DockCode"}` | a value fetched by `discover` or `-base-url`, then used like a plain value |

Priority for fields: `Dto.Name` before `operationId.Name` before `Name`.

**Values** may be any JSON type (objects and lists: see below). They are adjusted to the schema where this loses nothing: a single value for an array field becomes a one-element array, an array for a single field gives its first element, a numeric string becomes a number for numeric fields, and numbers and booleans become strings for string fields. A value that still violates the schema of a place it applies to stops the run with `DEFAULT_INVALID`, and nothing is written. For generic names like `Name` or `Code`, use the `Dto.` or `operationId.` form.

#### Objects, lists and whole DTOs

A key does not have to name a single value. Its value can also be an object or a list. Where it lands depends on what the key names:

| Goal | Key | Value |
|---|---|---|
| a list of plain values | `"Ship.Tags"` | `["cargo", "fast"]` |
| a free object (`type: object` without `properties`) | `"Ship.Settings"` | `{"mode": "eco", "lights": {"deck": true}}` |
| some fields of a DTO inside another DTO | `"Ship.Pilot"` | `{"Name": "Ada"}` |
| the elements of a list of DTOs | `"Ship.Crew"` | `[{"Name": "Bo"}, {"Name": "Cy", "Rank": 5}]` |
| the same DTO field everywhere the DTO is used | `"Pilot.Name"` | `"Ada"` |
| a whole DTO everywhere, also a free one | `"#/components/schemas/Pilot"` | `{"Name": "Ada", "Rank": 3}` |
| a field in the body of one operation only | `"createShip.Pilot"`, `"createShip.Name"` | as above |

The examples below use this spec:

```yaml
Ship:
  type: object
  required: [Name, Pilot]
  properties:
    Id:       { type: integer, readOnly: true }
    Name:     { type: string }
    Pilot:    { $ref: "#/components/schemas/Pilot" }
    Crew:     { type: array, items: { $ref: "#/components/schemas/Pilot" } }
    Tags:     { type: array, items: { type: string } }
    Settings: { type: object }
Pilot:
  type: object
  required: [Name]
  properties:
    Id:   { type: integer, readOnly: true }
    Name: { type: string, maxLength: 12 }
    Rank: { type: integer, minimum: 1, maximum: 5 }
```

**Plain values and free objects** (`Tags`, `Settings`) are used as given. A list replaces the generated list. A single value for a list field becomes a one-element list.

**A field that holds a DTO** (`Pilot`) is **merged**. The generator builds the DTO as usual, then lays the default over it:

```json
{ "Ship.Pilot": { "Name": "Ada" } }
```

gives `"Pilot": {"Name": "Ada", "Rank": 3}`. `Name` comes from the default and `Rank` comes from the dictionary. Nested objects are merged the same way, so `{"Pilot": {"Address": {"City": "Kiel"}}}` changes only the city.

**A list of DTOs** (`Crew`) is merged per element. The list in the default sets the length. Each element is laid over a generated element, which supplies the fields you leave out:

```json
{ "Ship.Crew": [ { "Name": "Bo" }, { "Name": "Cy", "Rank": 5 } ] }
```

gives two crew members, both with all their fields. A single object instead of a list counts as a one-element list.

**A whole DTO.** A plain key like `"Person"` names a **field** called `Person`, not the DTO `Person`. If no field has that name, the run reports `DEFAULT_UNUSED` with the right key. The DTO itself has its own key, its `$ref`:

```yaml
Person:
  type: object
  additionalProperties: true
  example: {}
```

```json
{ "#/components/schemas/Person": { "name": "my", "age": 12 } }
```

The value is merged like `"Ship.Pilot"`, wherever a `Person` is built or already exists:

- `components.schemas.Person.example`: `{}` becomes `{"name": "my", "age": 12}`. An invalid example is regenerated with the default.
- request and response bodies of type `Person`
- every field that refers to it (`Owner: {$ref: Person}`) and every element of a `Person` list

This is the only way to give a **free DTO** (no `properties`) a content, because it has no fields to name. For a DTO with `properties`, you can also set single fields with `"Person.Name"`. Fields left out keep their generated values. To fix a value for one operation only, use `"<operationId>.<Field>"`. It applies to the fields of that operation's body at any depth.

**Which DTO a key names.** In `"Ship.Pilot"`, `Ship` is the DTO that **contains** the field and `Pilot` is the field name. `"Pilot.Name"` names the field `Name` inside `Pilot`. The layers are applied from general to specific, and the last one wins: generated value → `Pilot.Name` and other field keys inside the DTO → `#/components/schemas/Pilot` → `Ship.Pilot`.

```json
{
  "Pilot.Name": "Ada",
  "Ship.Pilot": { "Name": "Captain" }
}
```

The pilot of a ship is `Captain`. Every other pilot, for example in `Crew` or a `Pilot` request body, is `Ada`.

**readOnly and writeOnly.** A default may contain every field. Fields the place must not have are left out: `readOnly` fields in request bodies, `writeOnly` fields in responses. With `"Ship.Pilot": {"Name": "Ada", "Id": 9}`, the response example contains `"Id": 9` and the request body does not.

**Existing examples.** Defaults are merged into an example that already exists and is valid. Its other fields and list elements stay unchanged (`DEFAULTS_APPLIED`). A list in the default still sets the length.

**Limits:**

- **Fields cannot be removed.** A default adds or changes fields, but it cannot take out an optional field the generator added. To control every field, list every field.
- **`null` is a value**, not "remove". It is only valid for `nullable` fields.
- **The merged result is validated** against the schema of the place, with the pointer to the failing field. For example, `"Ship.Pilot": {"Rank": 9}` stops the run with `DEFAULT_INVALID … /Rank: number must be at most 5`. Nothing is written.
- **A plain key** like `"Pilot"` matches every field named `Pilot`, in every DTO, never the DTO `Pilot` itself. Use the `Dto.` form when the name is used for different things.
- **Objects and lists are not passed to apitest.** `-check` and `Config.Params` take plain values only (strings, numbers, booleans). Object defaults only shape the examples written into the spec.

**Extensions** are set on the operation and overwrite a value that is already there. apitest's own extensions are type-checked, because apitest ignores wrong types silently:

| Extension | Allowed value |
|---|---|
| `x-apitest-verify` | `true`, `false` or `{"poll": true, "timeout": "30s"}` |
| `x-apitest-skip` | a reason (string) or `true` |
| `x-apitest-forbidden` | `true` or `false` |
| `x-apitest-order` | a whole number |
| `x-apitest-compare` | `"schema"`, `"subset"` or `"exact"` |
| `x-apitest-ignore` | a list of field names or JSON pointers |

`x-apitest-bind` and `x-apitest-compare-unordered` are rejected: apitest reads them on parameters and responses, not on operations. Other `x-…` extensions are set without a check. Removing a key later does not remove the extension from the spec.

**Bindings** `{"bind": "<producer operationId>", "pointer": "/field"}` (or `"header": "Location"`, or `"pointer"` with `"request": true` for the body the producer sent) are written as `x-apitest-bind` when the parameter is defined in the operation. For a shared parameter they become a `links` entry on the producer's lowest 2xx response, unless that response is a shared component (`BIND_NOT_WRITTEN`).

**Sources** `{"from": "GET /path", "pick": "…"}` are fetched with GET requests:

- `pick` is a JSON pointer (`/0/DockCode`, `/items/2/id`, `~1` for `/`). A segment `[Field=value]` selects the first list element whose field has that value (`/[Active=true]/DockCode`); values compare with their JSON text.
- `{Name}` in `from` is filled with another value (a plain default or another source); sources are fetched in that order, and a cycle is an error.
- A failing request, an empty list or a `null` value stops the run. Nothing is invented.
- Without `-base-url`, unresolved sources are reported as `SOURCE_UNRESOLVED` and the dictionary values are used.

### 13.7 The dictionary

`global-dict.json` is the memory of the generator. Commit it: it makes the examples stable across runs, machines and spec regenerations. It is plain JSON with sorted keys, so it diffs well:

```json
{
  "version": 1,
  "schemas": {
    "ShipWrite": {
      "type": "object",
      "properties": {
        "Callsign":  { "type": "string", "pattern": "^(?=.*\\d)[A-Z0-9]{4,8}$", "required": true, "value": "48213" },
        "CargoTons": { "type": "number", "minimum": 0, "maximum": 500, "value": 212.4 },
        "Pilot":     { "ref": "Pilot" }
      }
    },
    "Vessel": { "ref": "ShipWrite" }
  },
  "parameters": {
    "path.dockCode": { "type": "string", "pattern": "^[A-Z]{2}-\\d{3}$", "required": true, "value": "KD-418" }
  },
  "paths": { "/ships/{id}": 412 }
}
```

- **Constraints** (`type`, `format`, `pattern`, `enum`, limits, `required`, `readOnly`, `writeOnly`, `nullable`) always come from the spec and are refreshed on every run.
- **`value`** is write-protected. Edit it by hand and the next run keeps it as long as it fits. A value that no longer fits is reported as `VALUE_INVALID` and kept, unless you run with `-repair`.
- **`ref`** marks a field that holds another DTO, also when the spec writes it as `allOf: [{$ref: X}, {…extensions only}]`, and a DTO that is only an alias of another (`Vessel: {$ref: ShipWrite}`). The value is built from that DTO. A DTO that extends another with `allOf` and own fields gets its own `properties`.
- **`paths`** keeps the values of generic path ids per path.
- **Values from `defaults.json` are kept here** once they are applied (`DICT_FROM_DEFAULTS`, counted in the spec summary line as `dictionary values from defaults`). A field key like `"Pilot.Name"` or a plain key sets the `value` of each field it matched. `"#/components/schemas/Person"` sets the `value` of a free DTO, or passes its fields to the DTO's own fields (not into other DTOs it refers to). A parameter default sets the parameter's `value`. Keys of the form `"<operationId>.<name>"` are not kept: a dictionary node is shared by every operation. A second run with the same defaults reports nothing. Remove a default later and the dictionary keeps its last value, so the examples stay as they are; change the value in the dictionary or set a new default to change them.
- Fields and parameters that disappear from the spec are dropped and reported as `REMOVED`.

### 13.8 Messages

Problems are always listed; messages marked *info* only with `-v`.

| Code | Where | Meaning | What to do |
|---|---|---|---|
| `VALUE_NEW` *info* | dict | a value was generated | – |
| `VALUE_REUSED` *info* | dict | taken from a field with the same compound name | – |
| `VALUE_INVALID` | dict | a kept value no longer fits its schema | fix it, or `-repair` |
| `VALUE_REPAIRED` | dict | an invalid value was regenerated | – |
| `PATTERN_PENDING` | dict | no value matches the pattern | set one in the defaults, or fix the pattern |
| `NO_VALUE` | dict | the constraints cannot be met, e.g. `minProperties: 1` with `additionalProperties: false` | fix the schema, or set a value in the dictionary or the defaults |
| `TYPE_CONFLICT` | dict | `type: object` with an `allOf` of an enum: no value can be valid | remove `type: object` in the spec |
| `PARAM_CONFLICT` | dict | one parameter name with different schemas in different operations | usually a spec mistake |
| `REMOVED` | dict | no longer in the spec | – |
| `EXAMPLE_ADDED` *info* | apply | an example was written | – |
| `EXAMPLE_REPLACED` | apply | the existing example did not fit its schema (also in error responses and `components.schemas`) | – |
| `EXAMPLE_NAMED_INVALID` | apply | a curated named example does not fit its schema; it is kept | fix it by hand |
| `DEFAULTS_APPLIED` *info* | apply | defaults were set inside an existing or named example | – |
| `EXAMPLE_INCOMPLETE` | apply | a required field or parameter has no value | set it in the defaults |
| `EXTERNAL_REF` | apply | the target lives in another file | examples there are not written |
| `SHARED_PARAM_CONFLICT` | apply | an operation-specific default for a parameter object shared by several operations | define the parameter in the operation |
| `GENERIC_ID` *info* | apply | where the value of `{id}` came from | – |
| `EXT_FROM_DEFAULTS` *info* | apply | an extension was set | – |
| `BIND_WRITTEN` *info* | apply | a binding was written | – |
| `BIND_NOT_WRITTEN` | apply | no place for the binding (shared response, no 2xx) | define the parameter in the operation |
| `DEFAULT_UNUSED` | apply | a defaults key matched nothing; a key that names a DTO gets the hint `#/components/schemas/<Dto>` | check the spelling and the operationId |
| `DICT_FROM_DEFAULTS` | dict | an applied default was kept in the dictionary | – |
| `SOURCE_UNRESOLVED` | apply | sources without `-base-url` | `-base-url` or a `discover` file |
| `SOURCE_RESOLVED` | apply | a source was fetched | – |
| `FATAL DEFAULT_INVALID` | apply | a default violates a schema; nothing written | fix or narrow the key |
| `FATAL EXT_INVALID` | apply | an extension has the wrong type or belongs elsewhere | fix the value |
| `FATAL BIND_INVALID` | apply | a binding names an unknown operation | fix the operationId |
| `NOT_BUILDABLE` | check | apitest could not send this case; the reason says which value is missing | `apitest-gen apply`, or a default |
| `EXAMPLE_SCHEMA` | check | an example violates its schema | `apitest-gen apply` replaces it |
| `BINDING` | check | `x-apitest-bind` or `links` are invalid | fix the spec |
| `CASES` | check | cases cannot be built at all, e.g. duplicate names | fix the spec |

### 13.9 Reviewing findings

After a test run, the report lists **spec findings** such as

```text
paths./ships/{id}/manifest.get.parameters[id]   parameter "id" is resolved heuristically from createShip (body /Id); make it explicit with x-apitest-bind or links
```

`apitest-gen review` evaluates these findings, and everything else that would stop a case or an example, **before** the run. For each one it proposes a fix and asks you to review it. It applies nothing:

```sh
apitest-gen review -spec openapi.yaml -dict global-dict.json -defaults defaults.json -v
```

```text
review: 7 findings; 2 defaults proposed, 2 values to choose, 1 defaults to correct, 1 fixed by apply, 1 to fix in the spec
  DEFAULT  getManifest.id = {"bind":"createShip","pointer":"/Id"}  (heuristic at paths./ships/{id}/manifest.get.parameters[id])
           → Makes the guess explicit; apply writes it as x-apitest-bind …
  DEFAULT  getShip.x-apitest-forbidden = false  (auth at paths./ships/{id}.get.x-apitest-forbidden)
  CHOOSE   listDocks.zone  (NOT_BUILDABLE at paths./docks.get.parameters[zone]: no value for required parameter "zone" (query))
  CHOOSE   /pilots/{id}  (GENERIC_ID at paths./pilots/{id}.get.parameters[id]: {id} has no binding and no default)
  EDIT     Pilot  (DEFAULT_UNUSED at defaults: "Pilot" matched no field; to set the DTO Pilot itself use the key "#/components/schemas/Pilot")
  APPLY    paths./ships/{id}.get.responses.200.content[application/json].example: example does not match the schema: …
  SPEC     paths./pilots/{id}.get.responses: the operation requires a token but documents neither 401 nor 403; …
please review defaults.suggested.json: accept an entry by copying it into defaults.json, or pass both files: -defaults defaults.json,defaults.suggested.json
```

**Kinds of fixes:**

| Action | Meaning | In `defaults.suggested.json` |
|---|---|---|
| `DEFAULT` | a defaults entry solves it; the value is proposed | an active key, after a `"$why <key>"` comment that explains the finding |
| `CHOOSE` | a defaults entry solves it, but only you know the value (an id that exists in the environment, a value no generator can find) | `"$choose <key>"` with the constraints and the key to add |
| `EDIT` | an entry of your defaults is wrong or matches nothing | in the list `"$edit"` |
| `APPLY` | `apitest-gen apply` fixes it | in the list `"$apply"` |
| `SPEC` | only a change of the spec fixes it; the entry says what to change, often with a YAML snippet | in the list `"$spec"` |

**What is evaluated, and the proposed fix:**

| Finding | Source | Fix |
|---|---|---|
| `heuristic`: a parameter resolved heuristically | apitest report | `DEFAULT` `"<operationId>.<param>": {"bind": "<producer>", "pointer": "/Id"}` (or `"header"`, `"request": true`), exactly the binding apitest guessed. `apply` writes it as `x-apitest-bind`, or as a link on the producer for a shared parameter. Without operationIds: `SPEC`. |
| `binding`: a link to an unknown operation or parameter | apitest report | `SPEC`, with the closest operationId (`did you mean "getDock"?`) |
| `auth`: `x-apitest-forbidden` without a 403 response | apitest report | `DEFAULT` `"<operationId>.x-apitest-forbidden": false`, or document a 403 |
| `auth`: a secured operation without 401 or 403 | apitest report | `SPEC` with the response to add, or `Config.SkipAuthCases` |
| `auth`: no `security` in the spec | apitest report | `SPEC` with a `securitySchemes` snippet |
| `example_schema` in an `example` | apitest report | `APPLY`: apply replaces it |
| `example_schema` in a named example | apitest report | `SPEC`: named examples are curated and never changed |
| `validation`: conflicting paths | apitest report | `SPEC` |
| `NOT_BUILDABLE`: a required parameter without value | `check` | `APPLY` if the dictionary has a value, otherwise `CHOOSE` `"<operationId>.<param>"` with type, format, pattern and enum |
| `NOT_BUILDABLE`: a body without example, an unsupported media type | `check` | `APPLY` or `SPEC` |
| `GENERIC_ID`: `{id}` without binding and without default | apply | `CHOOSE` `"/path/{id}"`: an id that exists in the environment; the generated one is shown |
| `NO_VALUE`, `PATTERN_PENDING`, `TYPE_CONFLICT` | dictionary | `CHOOSE` `"Dto.Field"` or `"#/components/schemas/Dto"` |
| `VALUE_INVALID` | dictionary | `APPLY` with `-repair`, or correct the value |
| `EXAMPLE_INCOMPLETE` | apply | `CHOOSE` for the field without value, or `SPEC` for a cycle or contradiction |
| `DEFAULT_INVALID`, `EXT_INVALID`, `BIND_INVALID`, `DEFAULT_UNUSED` | apply | `EDIT`: the entry and the key to use instead |
| `SHARED_PARAM_CONFLICT`, `BIND_NOT_WRITTEN`, `EXTERNAL_REF` | apply | `SPEC` |

The Swagger 2.0 conversion note needs no fix and is not listed.

**The proposals file** is itself a valid defaults file. Only `DEFAULT` entries are active keys. Everything else is a `$` comment, which the defaults loader ignores, so nothing takes effect without your review:

```json
{
  "$review": "Proposals by apitest-gen review. Check every entry. …",
  "$why getManifest.id": "heuristic at paths./ships/{id}/manifest.get.parameters[id]: parameter \"id\" is resolved heuristically from createShip (body /Id). Makes the guess explicit; …",
  "getManifest.id": { "bind": "createShip", "pointer": "/Id" },
  "$why getShip.x-apitest-forbidden": "auth at …: x-apitest-forbidden is set but 403 is not documented; …",
  "getShip.x-apitest-forbidden": false,
  "$choose /pilots/{id}": "GENERIC_ID at …: {id} has no binding and no default. … (generated so far: 275); then add it as \"/pilots/{id}\"",
  "$edit": [ "DEFAULT_UNUSED at defaults: \"Pilot\" matched no field; …" ],
  "$apply": [ "example_schema at …: … Run apitest-gen apply: it replaces examples that violate their schema" ],
  "$spec": [ "auth at paths./pilots/{id}.get.responses: … document the response …" ]
}
```

**Review workflow:**

1. Run `review`, read every entry. Check each proposed binding: does the producer really return the value the consumer needs? apitest guessed it from the names.
2. Accept entries by copying them into `defaults.json`. To accept all `DEFAULT` entries at once, pass both files: `-defaults defaults.json,defaults.suggested.json`.
3. Add the `CHOOSE` keys with real values, correct the `EDIT` entries, change the spec for `SPEC`.
4. Run `apply`. Bindings become `x-apitest-bind` or links in the spec, so apitest no longer reports them.
5. Run `review` again. Keys already in your defaults are never proposed again; a clean spec gives `nothing to review`; no file is written then, and an older proposals file is left as it is.

### 13.10 Using the results in apitest

- **Same keys.** The keys of `defaults.json` work the same way as `Config.Params` (`"name"` and `"<operationId>.<name>"`). `check` uses the plain values of the defaults like `Config.Params`, so you can also pass them to apitest instead of writing them into the spec.
- **One spec per environment.** Write environment values into a copy (`-out openapi.qa.yaml`) and point `Config.SpecPath` to it.
- **Comparing generated examples.** Generated examples describe valid data, not the data of your environment. Against shared environments use `CompareMode: apitest.CompareSchema`. In-process tests, where the cases create their own data, can keep the default `subset` comparison.
- **Run `check` in CI** before the tests. It fails as soon as a spec change leaves a case without a value.
