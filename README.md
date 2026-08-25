# Clinic Booking API

A REST API for a small clinic: patients browse a doctor's free 30-minute slots for a
given day, book one, cancel it, or move it. Once a slot is booked it is not offered to
anyone else.

Built for the Savannah Informatics backend take-home assessment.

**Stack:** Go 1.25 · Echo v4 · MySQL 8.0 · Redis 7 · Docker · Railway · GitHub Actions

- Live URL: https://clinic-booking-api.up.railway.app
- Repository: https://github.com/Wanjie-Ryan/clinic-booking-API

**Endpoints** (full request/response details, status codes, and validation order
are in [§3 API notes](#3-api-notes); 5 doctors and 3 patients are pre-seeded, ids
`1`-`5` and `1`-`3` respectively, so the `GET` examples below work as-is):

- `GET https://clinic-booking-api.up.railway.app/healthz`
- `GET https://clinic-booking-api.up.railway.app/doctors/1/availability?date=2026-08-31`
- `POST https://clinic-booking-api.up.railway.app/appointments`
- `PATCH https://clinic-booking-api.up.railway.app/appointments/{id}/cancel`
- `PATCH https://clinic-booking-api.up.railway.app/appointments/{id}/reschedule`
- `GET https://clinic-booking-api.up.railway.app/patients/1/appointments`

`{id}` in the two `PATCH` routes is an appointment id — book one via `POST
/appointments` first (the response includes its `id`), then use that.

---

## Table of contents

1. [System design](#1-system-design)
2. [Running locally](#2-running-locally)
3. [API notes](#3-api-notes)
4. [Deployment & CI/CD](#4-deployment--cicd)
5. [AI reflection](#5-ai-reflection)
6. [Decision log](#6-decision-log)

---

## 1. System design

### 1.1 The problem, restated

Five doctors. Each has set working hours. Work is divided into 30-minute slots. A
patient picks a doctor and a date, sees what is free, and takes one. That slot then
disappears for everyone else. Patients can cancel, which puts the slot back. The clinic
is small today and expects to grow.

Two things in that paragraph are load-bearing and everything else follows from them:

- **"that slot must not be available to others"** — this is a correctness requirement
  under concurrency, not a UI requirement. Two patients hitting *Book* on the same slot
  at the same millisecond must not both succeed.
- **"we want to grow"** — growth means more than one application instance. Any
  correctness mechanism that lives inside one process stops being correct the moment a
  second process starts. That rules out a whole class of tempting solutions before I
  write a line of code.

### 1.2 Data model

Four tables. No slot table — see §1.4 for why.

#### `doctors`

| column | type | notes |
| --- | --- | --- |
| `id` | `BIGINT` PK, auto-increment | |
| `name` | `VARCHAR(200)` | |
| `created`, `updated` | `DATETIME` / `TIMESTAMP` | insert time / last-modified time |

A doctor is a *resource that gets scheduled against*, not an actor. There are no doctor
endpoints in this version — see [D-09](#6-decision-log).

#### `doctor_working_hours`

| column | type | notes |
| --- | --- | --- |
| `id` | `BIGINT` PK | |
| `doctor_id` | `BIGINT` FK → `doctors.id` | `ON DELETE CASCADE` |
| `day_of_week` | `TINYINT` | `0` = Sunday … `6` = Saturday, matching Go's `time.Weekday` |
| `start_time` | `TIME` | clinic-local wall clock, e.g. `08:00:00` |
| `end_time` | `TIME` | exclusive upper bound |

**A doctor may have several rows for the same weekday.** That is the whole design of
breaks: a doctor who works 08:00–13:00 and 14:00–17:00 has two rows for that day, and
the 13:00–14:00 lunch break is simply the gap between them. There is no `is_break`
column and no break table.

The reason is that the assessment never defines break behaviour, and a dedicated break
field would force me to invent semantics for questions it does not answer — can breaks
overlap working hours, can a break be booked over in an emergency, do breaks recur
differently from working hours. Ranges-with-gaps answers all of those by construction:
if there is no working-hour row covering a time, that time is not bookable. One concept
instead of two. See [D-03](#6-decision-log).

`day_of_week` is stored as a recurring weekly pattern rather than concrete dates,
because that is what "each doctor has set working hours" describes. Per-date overrides
(a doctor taking one Tuesday off) are the natural next feature and are discussed in
§1.9.

#### `patients`

| column | type | notes |
| --- | --- | --- |
| `id` | `BIGINT` PK | |
| `name` | `VARCHAR(200)` | |
| `email` | `VARCHAR(200)` | unique |
| `phone` | `VARCHAR(20)` | |

#### `appointments`

| column | type | notes |
| --- | --- | --- |
| `id` | `BIGINT` PK | |
| `doctor_id` | `BIGINT` FK → `doctors.id` | |
| `patient_id` | `BIGINT` FK → `patients.id` | |
| `start_time` | `DATETIME` | UTC — see §1.6 |
| `end_time` | `DATETIME` | UTC, exclusive |
| `status` | `ENUM('booked','cancelled')` | |
| `cancellation_reason` | `VARCHAR(500)` NULL | required when cancelling |
| `active_slot_key` | `VARCHAR(64)` generated, stored | **the concurrency guard — see §1.3** |
| `created`, `updated` | | |

### 1.3 Preventing double-booking

This is the decision the whole assessment turns on, so it gets its own section.

#### What I did

MySQL does not support PostgreSQL-style partial unique indexes
(`CREATE UNIQUE INDEX ... WHERE status = 'booked'`), so I get the same effect with a
stored generated column that is `NULL` for every row I want the index to ignore:

```sql
active_slot_key VARCHAR(64) GENERATED ALWAYS AS (
    CASE WHEN status = 'booked'
         THEN CONCAT(doctor_id, '-', start_time)
         ELSE NULL
    END
) STORED,
UNIQUE KEY uq_active_slot (active_slot_key)
```

InnoDB treats every `NULL` in a unique index as distinct from every other `NULL`. So:

- Two `booked` rows for the same doctor and the same start time produce the same key
  string and collide. The second `INSERT` fails with MySQL error **1062**.
- Any number of `cancelled` rows for that doctor and slot produce `NULL` and never
  collide with anything, including each other. A slot can be booked, cancelled, and
  rebooked indefinitely, and the history of every one of those bookings stays in the
  table.

The application catches error 1062 specifically and returns **`409 Conflict`** with a
message saying the slot was just taken — not a generic `500`. The database is doing the
mutual exclusion; the application's only job is to translate the outcome into the right
HTTP status.

#### What I deliberately did not do

**No `sync.Mutex`.** A process-local mutex serialises bookings within one running
binary and provides exactly zero protection between two binaries. The moment the
service is scaled past one instance — which "we want to grow" makes an explicit
requirement, and which Railway does with a slider — a mutex is silently no longer doing
the thing it appears to be doing. It is worse than nothing, because it looks like a
safeguard in code review. I did not add one "as well as" the constraint either: it adds
no safety on top of the unique index and creates false confidence about where
correctness actually comes from.

**No Redis lock (`SETNX` + TTL).** A distributed lock in Redis is a *liveness*
optimisation, not a *safety* guarantee. If the lock's TTL expires while the holder is
still mid-transaction, or if Redis fails over and loses the key, two writers proceed
concurrently and you are relying on the thing you thought the lock was preventing.
Correctness has to sit in the same place as the data. Redis has two real jobs in this
service (§1.5) and guarding this invariant is not one of them.

**No `SELECT ... FOR UPDATE` pessimistic lock.** This would be correct — but it holds a
row/gap lock across the read-check-write window, which invites deadlocks between
overlapping bookings and adds latency to every booking, including the overwhelming
majority that are not contended. The unique constraint is optimistic: it costs nothing
in the uncontended case and rejects the loser instantly in the contended one.

#### Trade-off I accepted

The generated-column trick is MySQL-specific. On PostgreSQL this would be a one-line
partial unique index and arguably cleaner. I picked MySQL because it is the database I
am most comfortable operating and debugging under time pressure, and because the
constraint is expressible either way — the *design* (let the database enforce the
invariant) ports; only the *syntax* does not.

### 1.4 Availability: computed, not stored

`GET /doctors/{id}/availability?date=YYYY-MM-DD` works like this:

1. Resolve the date to a weekday in the clinic's timezone.
2. Read that doctor's `doctor_working_hours` rows for that weekday (usually one or two).
3. Chop each range into 30-minute slots, discarding any trailing remainder that cannot
   hold a full slot — an 08:00–10:15 range yields 08:00, 08:30, 09:00, 09:30, and stops;
   09:45–10:15 is not offered because a 30-minute appointment would run past the end of
   the doctor's hours.
4. Subtract every slot that already has a `booked` appointment for that doctor that day.
5. If the date is today, also subtract slots that fall inside the minimum-lead window
   (§1.7).

**There is no `slots` table.** Materialising slots would mean generating rows for every
doctor for every working day forever, deciding how far into the future to generate,
running a job to extend the horizon, and backfilling or reconciling whenever a doctor's
working hours change. Every one of those is a source of drift between what the slot
table says and what the doctor's actual hours are.

Computing slots on demand is O(number of working-hour ranges for that day) — in
practice two rows and a loop — and is *definitionally* consistent with the working
hours, because it is derived from them on every read. The trade-off is that the
availability endpoint does a little work per request instead of a single indexed
`SELECT`; that is what the Redis cache in §1.5 is for.

### 1.5 Redis: two jobs, both deliberate

Redis is not in this design because a booking system is expected to have Redis. It does
two specific things.

**1. Availability cache.** The computed slot list is cached per doctor + date under
`availability:{doctor_id}:{date}` with a ~60-second TTL. Availability is the read that
patients hammer — every one of them browses before booking — and the answer is
identical for every patient looking at the same doctor and day.

On any successful write (book, cancel, reschedule) the key for the affected doctor+date
is invalidated **after the database transaction commits, never before**. Invalidating
first opens a window where a concurrent read repopulates the cache from pre-commit
state and then the write lands, leaving a stale entry with no event left to clear it.

If the invalidation call itself fails, it is logged and the request still returns
success. The booking is already durably committed — that is what the patient cares
about. The cache goes stale for at most the remainder of its 60-second TTL. Failing a
successful booking because a cache delete failed would be trading a real outcome for a
cosmetic one.

**2. Idempotency keys.** `POST /appointments` accepts an optional `Idempotency-Key`
header. If present, Redis is checked first; a hit returns the stored response instead of
re-processing. The result is stored *after* the database write succeeds. This makes the
booking endpoint safe against the mobile-network retry — patient taps *Book*, the
response is lost in transit, the client retries, and without this the patient either
gets a confusing `409` on their own booking or, worse, a second appointment.

**What Redis deliberately does not do:** guard the double-booking invariant (§1.3), and
cache `GET /patients/{id}/appointments`. The latter is a small, indexed, per-patient
query with a low read rate and a high freshness expectation — a patient who just
cancelled something and immediately reloads their list should not see it still there.
Not every read needs a cache. Caching reflexively is how you end up debugging staleness
in exchange for microseconds nobody asked for. See [D-06](#6-decision-log).

### 1.6 Time and timezone

Working hours are wall-clock times ("this doctor works 08:00 to 17:00"). Appointments
are instants. Conflating those is where booking systems go wrong, so:

- **`appointments.start_time` / `end_time` are stored in UTC.** The MySQL container runs
  with `--default-time-zone=+00:00` and the Go driver's connection uses UTC, so a
  `DATETIME` round-trips without any implicit local-time reinterpretation.
- **`doctor_working_hours.start_time` / `end_time` are clinic-local wall clock**, which
  is what they mean.
- All conversion between the two happens in Go, using `CLINIC_TIMEZONE`
  (default `Africa/Nairobi`). Slot generation builds candidate times in the clinic's
  location, then converts to UTC to compare against booked rows.
- The API accepts and returns RFC3339 timestamps with an explicit offset, so a client is
  never guessing which zone a bare timestamp is in.

Kenya is UTC+3 year-round with no DST, so today this is a fixed offset and the
distinction costs nothing. I kept it anyway, because the code that assumes "local time
is just UTC plus a constant" is the code that breaks the first time a clinic opens in a
DST jurisdiction — and it breaks silently, twice a year, for one hour.

### 1.7 Validation rules

`POST /appointments` and the reschedule target are validated identically. Order matters,
because the first failure is what the patient sees:

1. **Well-formed** — doctor and patient exist, timestamp parses. → `400` / `404`
2. **Aligned** — start time falls on a 30-minute boundary consistent with the doctor's
   working-hour range start. → `400`
3. **Not in the past** — start time is in the future. → `400`
4. **Minimum lead time** — at least 60 minutes from now (bonus requirement). → `400`
5. **Within working hours** — the whole 30 minutes fits inside one of the doctor's
   working-hour ranges for that weekday. → `400`
6. **Not taken** — no `booked` appointment for that doctor at that start time. → `409`

Rules 1–5 are pure functions of the request and the doctor's schedule, live in
`app/library`, and are unit-tested without a database. Rule 6 is checked twice on
purpose: once with a `SELECT` so the common case returns a clean, cheap `409`, and once
by the unique constraint at `INSERT` time, which is the check that is actually
authoritative under concurrency. The `SELECT` is a courtesy; the constraint is the
contract.

### 1.8 Component layout

```
main.go                     logrus JSON init, OTel/Uptrace init, root span, App bootstrap
app/
  constants/                shared log-field keys, status values, domain constants
  database/                 MySQL pool (otelsql-wrapped), Redis client, health probes
  models/                   request / response / domain structs
  controllers/              bind → validate → SQL → respond; one file per resource
  library/                  pure helpers: slot generation, validation, cache, metrics
  router/                   App struct, dependency wiring, route table, server start
  internal-middleware/      recover, request ID, CORS, tracing, rate limit, tx logger
migrations/                 golang-migrate .up.sql / .down.sql pairs
```

Each package has one job: `controllers` binds and validates a request and shapes a
response, `library` holds pure logic with no framework or database dependency,
`database` owns connection setup, `models` owns the shapes that cross package
boundaries. `library` is deliberately the layer with no `*sql.DB` and no `echo.Context`
in its function signatures — that is what makes the slot-generation and validation
logic testable without standing up MySQL.

### 1.9 What I would build next

Explicitly out of scope for this assessment, but these are the seams I left:

- **Authentication.** There is none, and `patient_id` is supplied in the request body.
  That is fine for an assessment and unacceptable in production — any caller can book or
  cancel on any patient's behalf. The fix is a token carrying the patient identity, with
  `patient_id` derived from it rather than trusted from the body. See
  [D-08](#6-decision-log).
- **Per-date schedule overrides** — a `doctor_schedule_exceptions` table for public
  holidays and individual days off, subtracted after the weekly pattern is expanded.
- **Appointment durations other than 30 minutes.** The slot length is already a config
  value rather than a literal, but variable-length appointments would need the
  concurrency guard to move from "same start time" to "overlapping range", which is a
  genuinely different constraint.
- **Notifications** on book/cancel/reschedule, published to a queue rather than sent
  inline, so a mail outage cannot fail a booking.

---

## 2. Running locally

> Fully written up in Phase 9. What is below is accurate as of Phase 1.

### Prerequisites

- Go 1.25+
- Docker and Docker Compose

### Create your `.env`

`.env` is gitignored — it's local config, not something to commit, even though
every value in it is a harmless local Docker Compose credential (production config
lives entirely separately, in Railway's own dashboard). Create `.env` in the repo
root yourself with this content:

```bash
ENV=dev
SETUP_TYPE=all
DEPLOYMENT_NAME=clinic-booking
SYSTEM_HOST=0.0.0.0
SYSTEM_PORT=8080

# MySQL
DATABASE_HOST=127.0.0.1
DATABASE_PORT=3307
DATABASE_USERNAME=clinic
DATABASE_PASSWORD=clinic
DATABASE_NAME=clinic
DATABASE_IDLE_CONNECTION=10
DATABASE_MAX_CONNECTION=50
DATABASE_CONNECTION_LIFETIME=300

# Redis
REDIS_HOST=127.0.0.1
REDIS_PORT=6380
REDIS_DATABASE_NUMBER=0
REDIS_PASSWORD=
REDIS_KEY_PREFIX=clinic

# Clinic domain
# appointments are stored in UTC.
CLINIC_TIMEZONE=Africa/Nairobi
SLOT_DURATION_MINUTES=30
BOOKING_MINIMUM_LEAD_MINUTES=60
AVAILABILITY_CACHE_TTL_SECONDS=60
IDEMPOTENCY_KEY_TTL_SECONDS=86400

# HTTP middleware
CORS_ALLOWED_ORIGINS=*
API_TIMEOUT_IN_SECONDS=30
RATE_LIMIT_RATE=20
RATE_LIMIT_BURST=5
RATE_LIMIT_INTERVAL=5

# Observability
# Leave empty to run without a tracing backend -- OTel degrades to a no-op tracer
# rather than failing startup.
UPTRACE_DSN=
```

### Start the dependencies

```bash
docker compose up -d
docker compose ps        # wait until both services report (healthy)
```

MySQL is published on host port **3307** and Redis on **6380** so they cannot collide
with a MySQL or Redis you already run locally. `.env` at the repo root already points
at those ports and is read directly — there is no separate example file and no config
library; every value is a plain `os.Getenv` call with an inline fallback in code.

Stop the containers with `docker compose down`, or `docker compose down -v` to also
drop the MySQL volume and start from an empty database.

### Run the API

There is no `.env`-loading library in this codebase (see the dependency list in the
standing rules), so local environment variables have to be exported into the shell
before running the binary:

```bash
set -a; source .env; set +a

go mod tidy                    # resolves go.mod / go.sum from the imports in the code
SETUP_TYPE=migrate go run .    # applies every pending migration, then exits
go run .                       # starts the HTTP server (SETUP_TYPE defaults to "all")
```

`SETUP_TYPE=migrate` is a one-shot mode: it creates the database if it isn't already
there, applies every pending migration, logs `migrations applied`, and exits — it never
starts the HTTP server. It's a convenience for running migrations in isolation
locally. In every other mode (including the normal server boot, `SETUP_TYPE=all`),
`runMigrations` also runs automatically before the HTTP server starts — see D-14 for
why. There's no separate migration step to remember to run before deploying.

Confirm it's up:

```bash
curl http://localhost:8080/healthz
# {"database":"ok","redis":"ok"}
```

### Run the tests

Two kinds of tests exist, and they need different things running.

**Pure logic tests** (`app/library/*_test.go`) — slot generation and booking
validation. No database, no network. Run on their own:

```bash
go test ./app/library/... -v
```

**Integration tests** (`app/controllers/appointments_test.go`) — exercise the actual
HTTP handlers against a real MySQL and Redis, because the thing being tested (the
double-booking guard) is a MySQL unique-constraint behaviour that a mock database
cannot reproduce. They need the same environment as `go run .`:

```bash
docker compose up -d
set -a; source .env; set +a
go test ./... -v
```

If `DATABASE_HOST` isn't set, the integration tests skip themselves with a clear
message rather than failing — so `go test ./...` is always safe to run, even without
the containers up; it just won't exercise the concurrency guard in that case.

The one worth reading is `TestBookAppointment_ConcurrentBookingsOnlyOneSucceeds`: it
fires 10 concurrent goroutines at the exact same doctor+slot and asserts exactly one
gets `201` and the other nine get `409` — the test the assessment explicitly asks
for, proving the database constraint (not just the application's courtesy check)
is what actually prevents double-booking under real concurrency.

---

## 3. API notes

All request/response bodies are JSON. All timestamps are RFC3339 with an explicit
offset (e.g. `2026-08-31T09:00:00+03:00`) — never a bare timestamp a client would
have to guess the timezone of.

### `GET /doctors/{id}/availability?date=YYYY-MM-DD`

Returns every free slot for a doctor on a given date, soonest first, as an array of
RFC3339 timestamps (converted to UTC in the response — the request's `date` is
interpreted in the clinic's local timezone, `CLINIC_TIMEZONE`).

| Situation | Status | Notes |
| --- | --- | --- |
| Success | `200` | `{"doctor_id":1,"date":"2026-08-31","available_slots":[...]}` |
| Doctor has no working hours that day | `200` | `available_slots` is `[]`, not an error — a doctor simply not working a given day is a valid, expected answer |
| `date` missing or malformed | `400` | must be `YYYY-MM-DD` |
| `id` not a positive integer | `400` | |
| Doctor doesn't exist | `404` | |

Cached for 60s per doctor+date (README §1.5); invalidated immediately after any
booking, cancellation, or reschedule affecting that doctor+date.

### `POST /appointments`

Books a slot. Optional `Idempotency-Key` header makes retries safe (README §1.5,
D-18).

Request:
```json
{"doctor_id": 1, "patient_id": 1, "start_time": "2026-08-31T09:00:00+03:00"}
```

**Validation order** (the first failure is what the caller sees — README §1.7):

1. Body well-formed, `doctor_id`/`patient_id` positive, `start_time` parses as
   RFC3339 → `400`
2. Doctor exists → `404`
3. Patient exists → `404`
4. Not in the past → `400`
5. At least `BOOKING_MINIMUM_LEAD_MINUTES` from now → `400`
6. Aligned to the doctor's slot grid and inside a working-hour range → `400`
7. Not already booked (courtesy check, then the authoritative database constraint) →
   `409`

Success returns `201` with the created appointment. A slot lost to a race between
the courtesy check and the insert also returns `409`, with a different message
(`"slot was just taken"` vs. `"slot is already booked"`) so the two paths are
distinguishable in logs even though the HTTP contract is identical.

### `PATCH /appointments/{id}/cancel`

Request: `{"reason": "patient requested cancellation"}` — `reason` is required.

| Situation | Status |
| --- | --- |
| Success | `200`, returns the appointment with `status: "cancelled"` |
| Missing/blank `reason` | `400` |
| Appointment doesn't exist | `404` |
| Already cancelled | `409` |

The freed slot is bookable again immediately — cancelling sets `active_slot_key` to
`NULL` via the generated column (README §1.3), no separate cleanup step.

### `PATCH /appointments/{id}/reschedule`

Request: `{"start_time": "2026-09-01T10:00:00+03:00"}` — validated exactly like a
fresh `POST /appointments` (same 7-step order above, steps 4-6, since doctor/patient
are already known from the existing appointment).

| Situation | Status |
| --- | --- |
| Success | `200` — **note: the response `id` is a new appointment ID**, not the one in the URL (D-20: reschedule cancels the old row and inserts a new one, preserving full history) |
| New `start_time` invalid | `400` |
| Appointment doesn't exist | `404` |
| Appointment already cancelled | `409` |
| New slot already taken | `409` — the original appointment is left untouched (transactional, README §1.3/D-05) |

### `GET /patients/{id}/appointments` (bonus)

Returns a patient's upcoming (`status = 'booked'`, `start_time` in the future)
appointments, soonest first. Deliberately uncached (README D-06) — a patient who
just cancelled and reloads must see that reflected immediately.

| Situation | Status |
| --- | --- |
| Success | `200`, array (possibly empty) of appointments |
| Patient doesn't exist | `404` |

### Decisions where the spec was ambiguous, called out per-endpoint

- **How does a patient come to exist?** The scenario describes patients booking
  against existing clinic records, not self-registering — there's no
  `POST /patients`. Three patients are seeded via migration (`6_seed_patients`,
  same pattern as the doctor seed) so the deployed API is bookable end-to-end
  without direct database access.
- **Reschedule's response ID** — see above; flagged here because it's the one place
  a client might reasonably expect the URL's `:id` and the response's `id` to match,
  and they deliberately don't.
- **Idempotency key scope** — matched by header value alone, not by also checking
  the request body matches (D-18).

---

## 4. Deployment & CI/CD

**Public URL:** `https://clinic-booking-api.up.railway.app` (exact domain as shown in
the Railway dashboard — Settings → Networking).

**Which branch deploys, and how:** `master` is both the branch GitHub Actions checks
and the branch Railway watches. On every pull request into `master`, the GitHub
Actions workflow (`.github/workflows/publish.yml`) runs two jobs in sequence:

1. **Build and vet** — checks out the code, sets up Go 1.25, `go build ./...`,
   `go vet ./...`.
2. **Test** — spins up MySQL 8.0 and Redis 7 as service containers, applies every
   migration against the MySQL container (`SETUP_TYPE=migrate go run .` — the exact
   same code path production uses to migrate itself, see D-14), then runs
   `go test ./...`. This is what actually exercises
   `TestBookAppointment_ConcurrentBookingsOnlyOneSucceeds` — the concurrency test
   needs a real MySQL with the real unique constraint, which is exactly what the
   service container provides.

Railway's own GitHub integration watches `master` directly and rebuilds/redeploys on
every push to it — but with **Wait for CI** enabled on the Railway service, it holds
off starting that redeploy until both GitHub Actions jobs against that same commit
have finished successfully. So merging a PR into `master` is what actually ships:
build, vet, and the full test suite (including the concurrency guard) all have to
pass first, then Railway builds the Docker image and deploys it.

**Infrastructure:** Railway project with three services — the app (built from the
repo's `Dockerfile`, not auto-detected buildpacks — see D-15), a managed MySQL
instance, and a managed Redis instance, connected to the app via Railway's variable
reference syntax (`${{MySQL.MYSQLHOST}}` etc.) rather than copy-pasted values, so
credential rotation on Railway's side doesn't require touching the app's config.
`/healthz` is wired in as Railway's own health check path, so a deploy that can't
reach MySQL or Redis is held back rather than promoted to serve traffic.

---

## 5. AI reflection

> Drafted from the real build log of this project. I've reviewed and edited this
> before submitting — it reflects what actually happened, not a generic answer.

**1. What I used AI for across the four sections.** I used it as a design and
implementation assistant, the way I'd use a pairing partner — never as the author.
I worked through system-design trade-offs with it before deciding on an approach
myself (weighing the double-booking guard against a `sync.Mutex`, a Redis lock, and
`SELECT ... FOR UPDATE` before I settled on the MySQL unique-constraint approach,
for the reasons in §1.3); I had it draft Go code, which I then typed into the
project by hand myself, read line by line, and verified against a real local MySQL
and Redis before accepting any of it into the codebase; I worked through an
extended, genuinely difficult Railway deployment debugging session with it; I had
it draft the CI/CD pipeline for me to review and adopt; and I used it to help draft
this reflection from the real session log, which I've then edited myself.

**2. One example where an AI suggestion genuinely improved the work.** I was stuck
with a Railway deployment that kept failing at the "Pre-Deploy Command" step —
silently, with zero log output, across several attempts, even after I'd fixed the
Dockerfile issue and confirmed the database existed. I asked for help debugging why
the command kept failing. Instead of continuing to chase the exact cause inside
Railway's pre-deploy execution environment, the suggestion was to stop depending on
that separate mechanism altogether and run migrations at the start of every normal
app boot instead — since `golang-migrate` already takes an advisory lock, this is
safe even with multiple replicas, and it removes an entire moving part from the
deploy pipeline rather than working around it. I adopted that approach, wrote it in
myself, and it worked on the first try — it's architecturally better than what I'd
been trying to debug, not just a patch.

**3. One example where AI output was wrong and how I caught it.** While building
the cancel endpoint, an early draft of the handler fetched an appointment's
`doctor_id`, `patient_id`, and `status` to cancel it, but never selected
`start_time` or `end_time` — so the response for a successful cancellation
silently returned blank strings for both fields. It compiled fine and passed
`go vet`, since Go doesn't flag an unset string field. I only caught it because I
ran the actual endpoint against real data myself and read the JSON response,
rather than trusting that the code looked right — `{"start_time":"","end_time":"",...}`
was obviously wrong the moment I saw it. That's why I made a point of testing every
endpoint against a live database myself before accepting any code into the
project, rather than just checking that it compiled.

**4. Two decisions I made without AI, and why I trusted my own judgment.**
- **Sticking to a single `.env` file with no `.env.example`.** A small workflow
  preference, but mine — I didn't want two files that could drift out of sync, and
  I was confident that trade-off (a `.env.example` would be gitignored-safer for a
  team, but this is a solo assessment) was fine for this project's scope.
- **Whether to pay for Railway's Hobby plan after the trial expired.** This was
  explicitly left to me — spending real money isn't something I wanted decided for
  me, and $5/month against the stakes of this assessment was an easy call once I
  weighed it myself.

---

## 6. Decision log

Every point where the assessment was ambiguous and I had to choose. The assessment
instructions ask for these to be called out, so they are recorded here as they are made
rather than reconstructed at the end.

| # | Decision | Reasoning |
| --- | --- | --- |
| **D-01** | Double-booking is prevented by a MySQL unique index on a generated column, not by an application lock. | A process-local mutex is not correct across instances, and "we want to grow" means there will be more than one instance. A Redis lock is a liveness optimisation, not a safety guarantee. The invariant belongs with the data. (§1.3) |
| **D-02** | Slots are computed per request, not stored in a `slots` table. | A materialised slot table needs a generation horizon, a job to extend it, and reconciliation whenever working hours change. Computing from working hours is consistent by construction. Cost is paid down by the Redis cache. (§1.4) |
| **D-03** | Breaks are modelled as gaps between multiple working-hour rows per weekday. There is no break field. | The spec never defines break semantics, so a dedicated field would mean inventing answers to questions it does not ask. Ranges-with-gaps answers them by construction: no covering row means not bookable. (§1.2) |
| **D-04** | Cancelling sets `status = 'cancelled'` and keeps the row. It never deletes. | Preserves the cancellation reason and the booking history, and the generated column frees the slot automatically because the key becomes `NULL`. A clinic needs to be able to answer "who cancelled and why". |
| **D-05** | Only reschedule runs in a transaction. Book and cancel do not. | Reschedule is two writes that must both land or neither. Book and cancel are each a single statement, which InnoDB already makes atomic; wrapping them adds ceremony and no guarantee. |
| **D-06** | `GET /patients/{id}/appointments` is not cached. | Small indexed per-patient query, low read volume, high freshness expectation — a patient who just cancelled and reloads must not see the cancelled appointment. Caching everything reflexively buys staleness bugs for microseconds nobody asked for. (§1.5) |
| **D-07** | Appointment times are stored in UTC; working hours are clinic-local wall clock; conversion happens in Go against `CLINIC_TIMEZONE`. | Working hours are wall-clock facts, appointments are instants. Kenya has no DST so the two coincide today, but code that assumes local time is UTC-plus-a-constant fails silently in a DST jurisdiction. |
| **D-08** | No authentication. `patient_id` comes from the request body. | The assessment does not ask for auth and adding it would expand scope well past the brief. Recorded here as a known gap rather than an oversight — in production `patient_id` must come from a verified token, not the body. (§1.9) |
| **D-09** | Doctors are a passive scheduled-against resource with no endpoints of their own. | The scenario describes exactly one actor who takes actions: the patient. Doctor and working-hour management is an admin surface the brief does not describe, so inventing one would be scope I was not asked for. Seed data covers the five doctors. |
| **D-10** | Dependencies (MySQL, Redis) run on non-default host ports 3307 and 6380. | So `docker compose up` cannot collide with a MySQL or Redis already running on the developer's machine. |
| **D-11** | Redis runs with persistence disabled (`--save "" --appendonly no`). | Everything in it is a cache entry or a short-lived idempotency key. Nothing in Redis needs to survive a restart, and saying so in the container config documents that the data there is disposable. |
| **D-12** | The request logging middleware logs method, path, status, latency and trace ID — never the request or response body. | A booking payload carries a patient's name, email and phone number. There is no operational reason for that to sit in application logs, so the body-capturing pattern I could have used elsewhere is deliberately not used here. |
| **D-13** | Startup runs `CREATE DATABASE IF NOT EXISTS` using the same credentials the app connects with, rather than requiring a separate privileged migration user. | Verified directly, in both environments: locally, `docker-compose.yml` provisions the `clinic` database via `MYSQL_DATABASE` and the app's scoped `clinic` user already has rights on it, so the statement is a same-name no-op. On Railway, the app deliberately uses a different database name (`clinic`) than the one Railway's MySQL plugin auto-provisions (`railway` — see D-15's neighbour), and it works there because Railway's own MySQL user is `root` with full instance privileges, so `CREATE DATABASE` genuinely creates it the first time. Either way, no separate admin-only credential needs to be managed. |
| **D-14** | Migrations run automatically at the start of every normal application boot (`SETUP_TYPE=all`), not only via a separate one-off `SETUP_TYPE=migrate` invocation. | Originally this ran only as Railway's "Pre-Deploy Command" — a separate process Railway executes before starting the real container. In practice this failed consistently and silently (no log output, ~2-6s) every time it invoked the compiled binary, while the exact same binary and code succeeded immediately when run as part of the main container's own startup. The precise root cause sits inside Railway's pre-deploy execution environment and isn't independently verifiable from here, but the fix removes the dependency on that separate mechanism entirely rather than working around it. `golang-migrate` takes an advisory MySQL lock (`GET_LOCK`) before applying anything, so running it on every boot is safe even with multiple replicas starting concurrently — on any boot after the first, it's a cheap version-check no-op (`migrate.ErrNoChange`), not a re-run. |
| **D-15** | The Dockerfile must be named exactly `Dockerfile` (capital D), and Railway's builder is pinned explicitly to Dockerfile rather than left on auto-detection. | Committing the file as `dockerfile` (lowercase) caused Railway to silently fall back to its own auto-buildpack (Railpack) instead of using the multi-stage build in this repo — the app still built and ran, just via a completely different, uncontrolled filesystem layout that didn't match this project's `WORKDIR`/`ENTRYPOINT` assumptions. Caught via the exact contents of Build Logs, not guessed. |
| **D-16** | The deployed app connects to a database named `clinic`, distinct from `railway`, the name Railway's MySQL plugin provisions by default. | Cosmetic, not functional — the app doesn't care what the database is called, it only reads `DATABASE_NAME` from the environment. Done purely so the database name is identical across local dev and production, which is one less thing to explain in an interview. |
| **D-17** | Rescheduling invalidates the availability cache for *both* the old slot's date and the new slot's date, not just one. | The two can be different calendar dates (rescheduling from one day to another), and each date is cached under its own key (`availability:{doctor_id}:{date}`). Invalidating only the new date would leave the old date's cache showing a slot as taken that's actually free again. |
| **D-18** | An `Idempotency-Key` is authoritative on its own — a replay is matched purely by the header value, not by re-checking that the request body matches what was sent the first time. | This is the same interpretation major payment APIs (Stripe, etc.) use: the key, not the body, is the source of truth for "have I seen this request before." The trade-off is explicit: a client that reuses a key with a genuinely different body gets back the *original* booking, not an error about a mismatch. That's the correct behavior for the case idempotency keys exist for (a client retrying its own timed-out request with an unchanged body) and an accepted edge case for a client that reuses a key incorrectly — the latter is a client bug, not something the server can distinguish from a legitimate retry without adding a body-hash check the assessment doesn't ask for. |
| **D-19** | There is no `POST /patients`. Three patients are seeded via migration `6_seed_patients`, the same pattern used for the doctor seed. | The scenario describes patients booking against existing clinic records; nothing in the brief asks for patient self-registration, and adding it would be scope the assessment doesn't call for — the same reasoning as D-09 for doctors. Without some patient rows existing, though, the deployed API would be unbookable by anyone without direct database access, so seeding a few is the minimal fix that keeps the API actually usable end-to-end. |
| **D-20** | `PATCH /appointments/{id}/reschedule` returns an appointment with a *different* ID than the one in the URL. | Consequence of D-05/D-04: reschedule cancels the original row and inserts a new one rather than updating `start_time` in place, so the full booking history survives (every slot an appointment ever held is its own row, with its own cancellation reason if superseded). The trade-off is explicit here because it's the one place a client might reasonably assume the URL's `:id` and the response's `id` match, and they deliberately don't — documented in the API notes (§3) precisely because it's non-obvious. |
