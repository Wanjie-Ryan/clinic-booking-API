# Clinic Booking API

A REST API for a small clinic: patients browse a doctor's free 30-minute slots for a
given day, book one, cancel it, or move it. Once a slot is booked it is not offered to
anyone else.

Built for the Savannah Informatics backend take-home assessment.

**Stack:** Go 1.25 · Echo v4 · MySQL 8.0 · Redis 7 · Docker · Railway · GitHub Actions

- Live URL: _filled in Section 4 (Phase 7/8)_
- Repository: _filled in Section 4 (Phase 7/8)_

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
| `created`, `updated` | `DATETIME` / `TIMESTAMP` | Touchvas house convention |

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

This mirrors an existing pattern in our casino-service, which classifies a MySQL
duplicate-key error into a `409` rather than letting it surface as an internal error.

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
partial unique index and arguably cleaner. I picked MySQL because it is what the
Touchvas services this codebase is styled after run on, and because the constraint is
expressible either way — the *design* (let the database enforce the invariant) ports;
only the *syntax* does not.

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

This mirrors the Touchvas identity-service and casino-service layout deliberately —
root `main.go`, everything under `app/`, the same package names and the same
responsibilities in each. The one thing worth calling out is `app/library`: in those
services it is where pure, dependency-free helpers live and it is the package that
carries the test file. That is exactly where the slot-generation and validation logic
sits here, which is what makes the booking rules testable without standing up MySQL.

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

### Start the dependencies

```bash
cp .env.example .env
docker compose up -d
docker compose ps        # wait until both services report (healthy)
```

MySQL is published on host port **3307** and Redis on **6380** so they cannot collide
with a MySQL or Redis you already run locally. `.env.example` already points at those
ports.

Stop them with `docker compose down`, or `docker compose down -v` to also drop the
MySQL volume and start from an empty database.

### Run the API

_Added in Phase 3._

### Run the tests

_Added in Phase 6._

---

## 3. API notes

_Added in Phase 4._

---

## 4. Deployment & CI/CD

_Added in Phases 7–8._

---

## 5. AI reflection

_Drafted in Phase 9 from the actual build log._

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
| **D-10** | Dependencies (MySQL, Redis) run on non-default host ports 3307 and 6380. | So `docker compose up` cannot collide with a MySQL or Redis already running on the developer's machine. Matches the port-remapping convention in the Touchvas sample compose files. |
| **D-11** | Redis runs with persistence disabled (`--save "" --appendonly no`). | Everything in it is a cache entry or a short-lived idempotency key. Nothing in Redis needs to survive a restart, and saying so in the container config documents that the data there is disposable. |
