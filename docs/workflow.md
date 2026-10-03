# Workflow: from an OpenAPI spec to green integration tests

This guide walks through one fictional project from start to finish: the **Starport API**, which rents docks to spaceships. It shows how `apitest-gen` and the `apitest` library work together:

1. `apitest-gen` makes the spec testable: it writes the examples `apitest` needs and checks that every case can be sent.
2. `apitest` runs those cases against the API as ordinary `go test` subtests, locally in-process and against a shared environment.

Every flag and every key used here is described in [configuration.md](configuration.md#13-apitest-gen). The outputs shown are illustrative; your numbers depend on your spec.

## Quick reference: review, then apply

```sh
apitest-gen review -spec openapi.yaml
# now check the model and defaults.json; copy MethodOrder/DeleteLast/Tags of the test into "$apitest"
apitest-gen -spec openapi.yaml -base-url http://localhost:8080
apitest-gen review -spec openapi.yaml
```

| Command | What it does |
|---|---|
| `apitest-gen review -spec openapi.yaml` | Prints the **resource model** (which DTOs, keys and operations belong to `Book`, `Article`, …) and finds everything apitest would report. It **writes real data into `defaults.json`**: the lists the records are fetched from (`$snapshot`) and the ids used so far. No bindings, no comments, no placeholders. Creates the file if it does not exist. Changes neither the spec nor the dictionary. The reasons are printed, with `-v` one per entry. |
| *you* | Check the model and the new entries at the end of `defaults.json`. That is the only file you edit. |
| `apitest-gen -spec openapi.yaml -base-url <instance>` | Updates `global-dict.json` and writes the examples and extensions into `openapi.yaml`. Every resource gets records, fetched from the running instance (GET only) or, without `-base-url`, generated. The cases are played in apitest's order: an update changes the record, and every example shows the record as it is at its case. Before saving, it plays the written spec again and checks every example and every entry of `defaults.json` (`verify`); with a problem **nothing is changed**. Creates `global-dict.json` and `defaults.json` if they are missing. |
| `apitest-gen review -spec openapi.yaml` | Again, to see what is still open. Only problems the defaults cannot solve are left, such as a missing 401 in the spec. |

`-dict global-dict.json` and `-defaults defaults.json` are the defaults, so you do not need to type them.

**What `review` writes,** for a spec with `GET /DefaultBook/Level/{level}` (a list with the optional query parameters `bookCode` and `isbn`), `GET /Book/id/{id}`, `GET /Book/{Code}`, `PUT /Book/{Code}` and `GET /Book/{Code}/Article`, after a first run:

```json
{
  "$snapshot": {
    "Article": {
      "$comment": "GetArticles: /Book/{Code}/Article; filled: Code = Code of the first Book",
      "count": 1,
      "from": "/Book/abc/Article",
      "mandatoryfields": []
    },
    "Book": {
      "$comment": "GetDefaultBooks: /DefaultBook/Level/{level}?bookCode={bookCode}&isbn={isbn}; filled: bookCode = Code of the first Book; replace {level} with values that exist in the instance",
      "count": 1,
      "from": "/DefaultBook/Level/{level}?bookCode=abc",
      "mandatoryfields": []
    }
  }
}
```

`from` is the request the records are fetched with. What `review` knows is filled in, and `$comment` says from where, so you can check it against the template. `{level}` is unknown: write a level that exists (`/DefaultBook/Level/A1?bookCode=abc`). In `mandatoryfields` you list what the records must have, e.g. `["Author", "BookRead.BookDetail.ExpireDate"]`: only elements with a value there are taken. Before the first run no record is known; then a parent key stays a placeholder (`/Book/{Code}/Article`), and the snapshot fills it from the first Book.

and what you add by hand:

```json
{
  "$apitest": { "MethodOrder": ["POST", "PUT", "GET", "DELETE"], "DeleteLast": true }
}
```

| Entry | Meaning |
|---|---|
| `"$snapshot"` | the request each resource's records come from with `-base-url`; `count` elements become records and appear in the list examples |
| `"$apitest"` | the same `MethodOrder`, `DeleteLast` and `Tags` as in `apitest.Config`. With `PUT` before `GET`, the GETs expect what the PUTs sent. |
| `"$model"` | only if `review` shows a resource, key or role wrongly |
| `"/path/{id}": 275` | an id no record and no producer provides; the value the generator used so far. Replace it with one that exists. |
| `"<operationId>.x-apitest-forbidden": false` | `x-apitest-forbidden` without a 403 response |

**Review rules:**

| You see | Do this |
|---|---|
| the model | Does every resource have the right DTOs and keys? Correct it with `"$model": {"Book": {"keys": [...]}}`. |
| `ORDER` | Copy `MethodOrder`, `DeleteLast` and `Tags` of your test into `"$apitest"`. If they differ, the examples follow another order than the run. |
| `$snapshot` | Is it the right list? Are the filled parameters right (compare with `$comment`)? Replace every `{…}` that is not a parent key. Raise `count` for more list elements. Put the fields every record must have into `mandatoryfields`. |
| a fixed id (`"/path/{id}": 275`) | Replace it with an id that exists in the test environment, or delete it. |
| a proposal you deleted | Add its key to `"$rejected": ["GetBook.Code"]`, so `review` does not propose it again. |
| a problem printed as `SPEC` | Only a change of `openapi.yaml` helps, e.g. a missing 401. Do it by hand. |

- **No bindings, no links.** apitest finds the producer of an id by itself (its heuristic). The examples get exactly the values it will use, so the spec keeps only examples.
- **Updates are tested.** A PUT sends new values; the GETs after it expect them. After each PUT apitest also reads the resource and checks that it was stored.
- **The instance must hold the test data.** For an integration test with a test container, run `apitest-gen -base-url` against an instance with the same seed.

## Contents

1. [The project](#1-the-project)
2. [Install](#2-install)
3. [Step 1: what keeps apitest from running?](#3-step-1-what-keeps-apitest-from-running)
4. [Step 2: generate the examples](#4-step-2-generate-the-examples)
5. [Step 3: values that must be real](#5-step-3-values-that-must-be-real)
6. [Step 4: the local integration test](#6-step-4-the-local-integration-test)
7. [Step 5: test against QA](#7-step-5-test-against-qa)
8. [Step 6: CI](#8-step-6-ci)
9. [Day to day: the spec changes](#9-day-to-day-the-spec-changes)
10. [Reading the results](#10-reading-the-results)
11. [Troubleshooting](#11-troubleshooting)

---

## 1. The project

```text
starport/
├── api/
│   └── openapi.yaml          # written by hand or generated from code
├── server/                   # the Go API (any language works for apitest-gen)
└── apitest/
    ├── api_test.go           # the integration tests
    ├── defaults.json         # values that must exist, kept by you
    ├── defaults.qa.json      # where QA values come from (sources), kept by you
    ├── global-dict.json      # example values, created by apitest-gen, committed
    └── apitest_deviations.yaml
```

An excerpt of the spec. Like many real specs it has **no examples at all**:

```yaml
openapi: 3.0.3
info: { title: Starport, version: "2.1" }
security: [{ bearer: [] }]
paths:
  /docks:
    get:
      operationId: listDocks
      tags: [Dock]
      responses:
        "200":
          content: { application/json: { schema: { type: array, items: { $ref: "#/components/schemas/Dock" } } } }
        "401": { $ref: "#/components/responses/Unauthorized" }
  /ships:
    post:
      operationId: registerShip
      tags: [Ship]
      requestBody:
        required: true
        content: { application/json: { schema: { $ref: "#/components/schemas/ShipWrite" } } }
      responses:
        "201": { content: { application/json: { schema: { $ref: "#/components/schemas/Ship" } } } }
        "400": { $ref: "#/components/responses/BadRequest" }
        "401": { $ref: "#/components/responses/Unauthorized" }
  /ships/{id}:
    parameters:
      - { name: id, in: path, required: true, schema: { type: integer, minimum: 1 } }
    get:    { operationId: getShip,    tags: [Ship], responses: { "200": { … }, "404": { … }, "401": { … } } }
    put:    { operationId: updateShip, tags: [Ship], requestBody: { … }, responses: { "200": { … }, "401": { … } } }
    delete: { operationId: scrapShip,  tags: [Ship], responses: { "204": { description: scrapped }, "401": { … } } }
  /docks/{dockCode}/bookings:
    post:
      operationId: bookDock
      tags: [Booking]
      parameters:
        - { name: dockCode, in: path, required: true, schema: { type: string, pattern: '^[A-Z]{2}-\d{3}$' } }
      requestBody:
        content: { application/json: { schema: { $ref: "#/components/schemas/BookingWrite" } } }
      responses:
        "201": { content: { application/json: { schema: { $ref: "#/components/schemas/Booking" } } } }
        "401": { $ref: "#/components/responses/Unauthorized" }
components:
  schemas:
    ShipWrite:
      type: object
      required: [Name, Callsign, PilotEmail]
      properties:
        Name:       { type: string, minLength: 3, maxLength: 40 }
        Callsign:   { type: string, pattern: '^(?=.*\d)[A-Z0-9]{4,8}$' }   # needs a digit
        PilotEmail: { type: string, format: email }
        CargoTons:  { type: number, minimum: 0, maximum: 500 }
        Class:      { type: string, enum: [freighter, shuttle, cruiser] }
    Ship:
      allOf:
        - $ref: "#/components/schemas/ShipWrite"
        - type: object
          properties:
            Id:         { type: integer, readOnly: true }
            RegisteredAt: { type: string, format: date-time, readOnly: true }
    Dock:
      type: object
      properties:
        DockCode: { type: string, pattern: '^[A-Z]{2}-\d{3}$' }
        Active:   { type: boolean }
        Berths:   { type: integer, minimum: 1 }
    BookingWrite:
      type: object
      required: [ShipId, From, Until]
      properties:
        ShipId: { type: integer }
        From:   { type: string, format: date }
        Until:  { type: string, format: date }
    Booking:
      allOf: [{ $ref: "#/components/schemas/BookingWrite" }, { properties: { BookingId: { type: string, format: uuid } } }]
```

The docks are **seed data**: no operation creates them, they exist in every environment, but with different codes.

## 2. Install

```sh
go install github.com/fada4773-sketch/apigen-apitest/cmd/apitest-gen@latest
cd starport/apitest && go get github.com/fada4773-sketch/apigen-apitest/apitest@latest
```

`apitest-gen` is a single binary and does not care which language the API is written in. Only the tests in step 4 need Go.

## 3. Step 1: what keeps apitest from running?

```sh
apitest-gen check -spec ../api/openapi.yaml
```

```text
check: 9 of 23 cases can be sent, 14 problems
  NOT_BUILDABLE   Booking/bookDock/default: no value for required parameter "dockCode" (path)
  NOT_BUILDABLE   Booking/bookDock/unauthorized: no value for required parameter "dockCode" (path)
  NOT_BUILDABLE   Ship/registerShip/default: required body without example: no value for Callsign
  NOT_BUILDABLE   Ship/registerShip/invalid-token: required body without example: no value for Callsign
  NOT_BUILDABLE   Ship/updateShip/default: required body without example: no value for Callsign
  …
apitest-gen check: 14 problems
```

`check` uses apitest's own case building, so these are exactly the cases a real run would report as `NOT_BUILDABLE`. `getShip` and `scrapShip` are fine already: apitest takes the `id` from the response of `registerShip` at run time.

## 4. Step 2: generate the examples

A first run without any defaults:

```sh
apitest-gen -spec ../api/openapi.yaml -dict global-dict.json -check
```

```text
dictionary global-dict.json created: 5 DTOs, 13 fields, 2 parameters; values: 14 new, 1 reused, 0 kept, 0 invalid, 0 repaired, 0 without value
spec ../api/openapi.yaml: 9 examples added, 0 replaced, 0 with defaults, 0 kept, 0 incomplete; 0 extensions, 0 bindings; 0 dictionary values from defaults
check: 23 of 23 cases can be sent, 0 problems
```

What happened:

- **`global-dict.json` was created.** It holds one value per DTO field and parameter. Every value fits its schema: `Callsign` matches its pattern including the lookahead (`TQ7KR2`), `PilotEmail` is an e-mail address, `CargoTons` lies between 0 and 500, and `Name` of a `Ship` reads like a ship (`Brave Ship`). Compound names share one value: the path parameter `dockCode` and the field `DockCode` both get `KD-418`.
- **The spec got examples**, at the places apitest reads them. The `$ref`s stay untouched, comments and key order are kept:

  ```yaml
  /ships:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/ShipWrite" }
            example:                       # ← added
              CargoTons: 212.4
              Callsign: TQ7KR2
              Class: freighter
              Name: Brave Ship
              PilotEmail: lumen@example.com
  ```

  Read-only fields (`Id`, `RegisteredAt`) stay out of request examples, write-only fields out of response examples.
- **A second run changes nothing.** Values in the dictionary are write-protected, so the examples stay stable across runs and machines.

Commit `global-dict.json`. It is the memory of the generator. Whether you commit the changed spec depends on where it comes from (see [section 9](#9-day-to-day-the-spec-changes)).

You can edit values in the dictionary by hand. The next run keeps them as long as they fit the schema:

```json
"ShipWrite": {
  "type": "object",
  "properties": {
    "Name": { "type": "string", "minLength": 3, "maxLength": 40, "required": true, "value": "Millennium Dove" }
  }
}
```

## 5. Step 3: values that must be real

Generated values are fine for everything the test creates itself. They are not fine for things that must **already exist**, such as the docks. Those go into `defaults.json`, and they win everywhere a field or parameter of that name appears:

```json
{
  "$comment": "values that exist in every environment",
  "PilotEmail": "test-pilot@starport.example",
  "Class": "shuttle",

  "$comment_scoped": "only in one DTO or one operation",
  "ShipWrite.Name": "Integration Test Ship",
  "updateShip.Name": "Renamed Test Ship",

  "$comment_extensions": "apitest settings per operation",
  "scrapShip.x-apitest-verify": { "poll": true, "timeout": "20s" },
  "listDocks.x-apitest-compare": "schema",

  "$comment_order": "the same order as apitest.Config of the test",
  "$apitest": { "DeleteLast": true }
}
```

```sh
apitest-gen -spec ../api/openapi.yaml -dict global-dict.json -defaults defaults.json -check -v
```

`-v` (verbose) also lists every change and, at the end, at how many places each default matched. The count is the same on every run, also when nothing changes: it counts the places, not the writes. Whether something was written is shown by the summary line (`4 with defaults`) and the `DEFAULTS_APPLIED` lines. Without it apitest-gen prints only the summary lines and the problems.

```text
spec ../api/openapi.yaml: 0 examples added, 0 replaced, 4 with defaults, 5 kept, 0 incomplete; 2 extensions, 0 bindings; 0 dictionary values from defaults
  DEFAULTS_APPLIED      paths./ships.post.requestBody.content[application/json]: {"CargoTons":212.4,"Callsign":"TQ7KR2","Class":"shuttle",…}
  EXT_FROM_DEFAULTS     paths./ships/{id}.delete: x-apitest-verify: {"poll":true,"timeout":"20s"}
records: 2 resources, 2 records (generated); 1 updates on the way; 7 examples from the records, 0 parameters copied into their path
  RECORD                Dock: #1 DockCode="KD-418"
  RECORD                Ship: #1 Id=412
  UPDATE                paths./ships/{id}.put: Ship/updateShip/default changes Ship created by registerShip: Name "Integration Test Ship" → "Renamed Test Ship"
defaults matched (places in the spec this run, also where the value is already there):
  PilotEmail: 3
  Class: 3
  ShipWrite.Name: 1
  updateShip.Name: 1
  scrapShip.x-apitest-verify: 1
  listDocks.x-apitest-compare: 1
check: 23 of 23 cases can be sent, 0 problems
```

- The examples of docks and ships follow one record each through the run (see [configuration.md, 13.5](configuration.md#135-records-examples-that-follow-the-data)). `updateShip` sends `Renamed Test Ship`. The test below keeps apitest's default order, so `getShip` runs before the update and expects the name `registerShip` sent; with `MethodOrder: POST, PUT, GET, DELETE` in the Config and in `$apitest` it would expect the new name. `bookDock` gets the code of the dock record in its path.
- A default that violates a schema stops the run **before anything is written**:

  ```text
  FATAL DEFAULT_INVALID ShipWrite.Class: "Class" = "yacht" violates the schema: value is not one of the allowed values ["freighter","shuttle","cruiser"]
  apitest-gen apply: 1 problems with the defaults; nothing was written
  ```

- An entry that matches nothing is reported as `DEFAULT_UNUSED`, usually a typo.

## 6. Step 4: the local integration test

Locally the API runs in the test process with an empty database. Everything the cases need, they create themselves, so the generated values are all it takes.

```go
// apitest/api_test.go
package apitest_test

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/fada4773-sketch/apigen-apitest/apitest"

	"starport/server"
)

func TestStarport(t *testing.T) {
	db := server.NewMemoryStore()
	apitest.Run(t, apitest.Config{
		SpecPath: "../api/openapi.yaml",
		Handler:  server.NewRouter(db),
		Token:    apitest.StaticToken(server.TestToken),

		// the docks are seed data; the in-memory store creates two
		Hooks: apitest.Hooks{
			BeforeGroup: func(ctx context.Context, group string) error { return db.SeedDocks(ctx) },
			BeforeRequest: func(_ context.Context, c *apitest.Case, req *http.Request) error {
				req.Header.Set("X-Request-Id", c.Name)
				return nil
			},
		},
		// no gateway locally: the app only decodes tokens
		SkipAuthCases: []string{apitest.AuthInvalidToken},
		DeleteLast:    true,
		NumberCases:   true,
		DeviationsPath: "apitest_deviations.yaml",
		Strict:         os.Getenv("CI") != "",
	})
}
```

```sh
go test ./apitest -run TestStarport -v
```

```text
=== RUN   TestStarport/01_Dock/listDocks/default
=== RUN   TestStarport/02_Ship/registerShip/default
=== RUN   TestStarport/03_Ship/getShip/default
=== RUN   TestStarport/04_Ship/updateShip/default
=== RUN   TestStarport/05_Ship/registerShip/unauthorized
…
--- PASS: TestStarport (0.21s)
```

What apitest checks per case: the status code, the schema, the example (subset by default), and after every write a GET that reads the data back. The report lands in `apitest/apitest-report/TestStarport.md`.

### Review the spec findings

The report also lists **spec findings**: things apitest had to guess or could not test.

```text
paths./ships/{id}.get.parameters[id]      parameter "id" is resolved heuristically from registerShip (body /Id); make it explicit with x-apitest-bind or links
paths./ships/{id}.put.parameters[id]      parameter "id" is resolved heuristically from registerShip (body /Id); make it explicit with x-apitest-bind or links
paths./ships/{id}.delete.parameters[id]   parameter "id" is resolved heuristically from registerShip (body /Id); make it explicit with x-apitest-bind or links
```

These need no fix: apitest binds the id to `registerShip` at run time, and the examples already show the ship `registerShip` creates, in the state each case meets it. `review` confirms that nothing is open:

```sh
apitest-gen review -spec ../api/openapi.yaml
```

```text
model: 2 resources; the examples of each follow one record through the run
  Dock: schemas Dock; keys DockCode; list listDocks; read getDock
  Ship: schemas ShipRead, ShipWrite; keys Id; create registerShip; read getShip; update updateShip; delete scrapShip
review: 0 suggestions; 0 defaults proposed, 0 values to choose, 0 defaults to correct, 0 fixed by apply, 0 to fix in the spec
nothing to review
defaults.json: nothing added
```

To make a binding explicit anyway (for example to take an id from another operation than the one apitest guesses), add `"getShip.id": {"bind": "registerShip", "pointer": "/Id"}` to `defaults.json`; the next `apitest-gen` run writes it into the spec. All kinds of findings and their fixes: [configuration.md, 13.11](configuration.md#1311-reviewing-findings).

## 7. Step 5: test against QA

On QA the docks have other codes, and the data differ from the examples. Two things change:

**The seed values come from QA itself.** Instead of writing them by hand, describe where they come from:

```json
{
  "$comment": "defaults.qa.json: fetched from the environment",
  "DockCode": { "from": "GET /docks", "pick": "/[Active=true]/DockCode" }
}
```

```sh
export QA_TOKEN=…   # never on the command line
apitest-gen discover -defaults defaults.qa.json \
  -base-url https://qa.starport.example/v2 -token-env QA_TOKEN -header "X-Tenant: integration" \
  -out defaults.qa.resolved.json
```

```text
DockCode = "QA-201" (GET /docks)
written defaults.qa.resolved.json
```

Sources may build on each other. `{"from": "GET /docks/{DockCode}/berths", "pick": "/0/Number"}` waits for `DockCode`. Only GET requests are sent.

**The QA spec is written next to the original**, so QA values never end up in the shared spec:

```sh
apitest-gen -spec ../api/openapi.yaml -out ../api/openapi.qa.yaml \
  -dict global-dict.json -defaults defaults.json,defaults.qa.resolved.json -check
```

Later files override earlier ones. Discovery and generation also work in one step with `-base-url`, then the values are fetched in memory and no file is written. With `-base-url` the records come from QA too: the examples show the docks and ships QA has (`$snapshot` says from which lists). Data on QA change while others use it, so the QA test below still compares only the schema.

```go
func TestStarportQA(t *testing.T) {
	if os.Getenv("QA_URL") == "" {
		t.Skip("QA_URL not set")
	}
	apitest.Run(t, apitest.Config{
		SpecPath:     "../api/openapi.qa.yaml",
		BaseURL:      os.Getenv("QA_URL"),
		Token:        apitest.StaticToken(os.Getenv("QA_TOKEN")),
		AllowedHosts: []string{"qa.starport.example"},
		HealthPath:   "/health",
		Headers:      map[string]string{"X-Tenant": "integration"},

		CompareMode:    apitest.CompareSchema, // real data, not examples
		IgnoreFields:   []string{"RegisteredAt"},
		DeleteLast:     true,
		DeviationsPath: "apitest_deviations.qa.yaml",
		ReportJSON:     true,
		Strict:         true,
	})
}
```

`invalid-token` runs here: QA sits behind the gateway that validates tokens. The default manipulation sends the first 100 characters of the real token. Set `TamperToken: apitest.TamperSignature` once to prove that the gateway really checks signatures.

## 8. Step 6: CI

```yaml
# .github/workflows/api.yml (excerpt)
jobs:
  api-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.27.x" }
      - run: go install github.com/fada4773-sketch/apigen-apitest/cmd/apitest-gen@v0.2.0

      # the spec must stay testable: fails if a case could not be sent
      - run: apitest-gen check -spec api/openapi.yaml -defaults apitest/defaults.json

      # local integration test, in-process
      - run: go test ./apitest -run TestStarport$

      # QA, behind the gateway
      - run: |
          apitest-gen -spec api/openapi.yaml -out api/openapi.qa.yaml \
            -dict apitest/global-dict.json \
            -defaults apitest/defaults.json,apitest/defaults.qa.json \
            -base-url "$QA_URL" -token-env QA_TOKEN -check
          go test ./apitest -run TestStarportQA
        env:
          QA_URL: ${{ vars.QA_URL }}
          QA_TOKEN: ${{ secrets.QA_TOKEN }}
      - uses: actions/upload-artifact@v4
        if: always()
        with: { name: api-report, path: apitest/apitest-report/ }
```

Pin `apitest-gen` to a version, so a new release does not change the examples behind your back.

## 9. Day to day: the spec changes

| Situation | What to do |
|---|---|
| A field is added | Run `apitest-gen` again. Only the new field gets a value; all existing values stay. |
| A field is removed | The dictionary reports it as `REMOVED` and drops its value. |
| A constraint changes, e.g. `maxLength` gets smaller | The old value is reported as `VALUE_INVALID` and kept. `-repair` generates a new one. |
| The spec is **generated from code** (Swashbuckle, springdoc, …) | Its examples disappear with every generation. Run `apitest-gen` right after generating, in the build. The dictionary makes it write exactly the same examples again. |
| The spec is **written by hand** | Run `apitest-gen` once and commit the spec with its examples. |
| You want fresh values | Delete the entries in `global-dict.json`, or use another `-seed` for a new dictionary. |

`-dry-run -v` shows every change without writing anything.

## 10. Reading the results

| You see | Meaning | Usual fix |
|---|---|---|
| `PATTERN_PENDING` | No value matches the pattern (e.g. a contradiction like `^(?=a)b$`) | a value in `defaults.json`, or fix the pattern |
| `NO_VALUE` | no value can meet the constraints, e.g. `minProperties: 1` with `additionalProperties: false` | fix the schema, or a value in the dictionary or `defaults.json` |
| `EXAMPLE_NAMED_INVALID` | a curated named example violates its schema | fix it by hand; apitest-gen never overwrites named examples |
| `TYPE_CONFLICT` | `type: object` combined with an `allOf` of an enum | remove `type: object` in the spec |
| `PARAM_CONFLICT` | one parameter name, different schemas | usually a spec mistake; `operationId.name` in the defaults |
| `SHARED_PARAM_CONFLICT` | an operation-specific default for a parameter defined once for many operations | define the parameter in the operation, or use a plain default |
| `EXAMPLE_REPLACED` | an existing example did not fit its schema | nothing; check the spec author's intention |
| apitest `NOT_BUILDABLE` | a required value is missing | run `apitest-gen check`, then `apitest-gen review` for the proposed value |
| apitest spec findings (`resolved heuristically`, no 401/403, …) | apitest guessed or could not test something | `apitest-gen review`, then accept the proposals |
| apitest `EXAMPLE_MISMATCH` against a shared environment | real data differ from examples | `CompareMode: CompareSchema` |
| apitest `DATA_MISMATCH` | the GET after a write returns other values | a real bug, or `x-apitest-ignore` for server-made fields |

## 11. Troubleshooting

**`check` passes, but the run reports 404 for a seed value.**
The value fits the schema, but does not exist in that environment. Use a source (`from`/`pick`) or fix the value in the defaults.

**The run creates the same ship twice and gets 409.**
The examples are stable on purpose. Clean up in `Hooks.BeforeGroup`, run DELETEs last (`DeleteLast`), or let the server assign unique fields (`readOnly`).

**A named example was changed.**
Defaults also go into named `examples`, because they usually carry environment values. Exclude a deliberately wrong example, e.g. a negative test, with `x-example-defaults: false` on that example.

**`apitest-gen` writes nothing next to `examples`.**
OpenAPI does not allow `example` and `examples` side by side. With named examples, apitest builds one case per name and needs no `example`.

**The diff of my YAML is larger than expected.**
The YAML is re-emitted from its node tree: comments, key order and block style stay, flow mappings lose inner spaces (`{ a: b }` → `{a: b}`). Run it once, commit, and later runs only touch real changes.
