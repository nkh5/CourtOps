# CourtOps

Rotating doubles is usually run on a whiteboard. Somebody writes down who is
waiting, a court frees up, and four names get picked — while the person holding
the pen is also trying to play. Games get missed, the same strong pair keeps
getting drawn together, and nobody can tell you how long they have been sitting.

CourtOps is a small web app for the person holding the pen. Import the roster
from a Google Form, check people in, and press one button when a court frees up:
the four longest-waiting players go on, split into the most balanced pair of
teams. A public court board shows everyone where they are in the queue.

One sport, one club, one live event at a time. That limit is deliberate and it
is what keeps the whole thing explainable.

## What it does

- **Import a roster** from the Google Form response sheet (CSV). Rows are
  validated with line numbers; a file with any bad row imports nothing.
- **Add late arrivals** during a live event, with optional immediate check-in.
  Duplicate players are rejected and new arrivals join the end of the queue.
- **Track state** per player per event: ready, resting, playing, left.
- **Plan registrations** with optional estimated arrival time and paddle needs.
  These answers belong to the event and are visible only to organizers.
- **Delete finished events** with a typed `DELETE` confirmation. Shared player
  profiles are kept; events with saved Elo changes are protected from deletion.
- **Assign the next match** — the four longest-waiting ready players, split into
  the most balanced of the three possible doubles pairings.
- **Record results** with an explicit confirmation, and optionally apply a
  provisional local Elo rating in the same transaction.
- **Show a public board** that polls every five seconds and says so when it is
  showing stale data.

Players never need an account. Organizers sign in with Google, and only the
accounts on a configured allowlist can change anything.

## Stack

| Part | Choice |
| --- | --- |
| Backend | One Go binary: `net/http`, `html/template`, `pgx` |
| Database | PostgreSQL 16, handwritten SQL, versioned migrations |
| Frontend | Server-rendered HTML, one small polling script, no framework |
| Auth | Google OpenID Connect, signed `HttpOnly` session cookie |
| Deploy | One EC2 host: Caddy (HTTPS) + app + PostgreSQL via Docker Compose |

Templates, stylesheet and migrations are embedded in the binary, so a release is
one 24 MB image.

## Run it locally

Needs Go 1.27+ and Docker.

```bash
make db        # PostgreSQL on localhost:5433
make run       # http://localhost:8080, with development sign-in
```

Then open <http://localhost:8080>, use **Development sign-in**, create an event,
and import `testdata/sample_roster.csv`. The synthetic roster has 12 players
across the three skill bands.

Without `make`:

```bash
docker compose up -d
DATABASE_URL="postgres://courtops:courtops@localhost:5433/courtops?sslmode=disable" \
COURTOPS_SESSION_SECRET="local-development-secret-32-bytes-long" \
COURTOPS_ORGANIZERS="dev@example.com" \
COURTOPS_DEV_ORGANIZER="dev@example.com" \
go run ./cmd/courtops
```

Migrations run automatically at start-up; `-migrate-only` applies them and exits.

## Tests

```bash
make test      # everything, with the race detector
make test-unit # only the tests that need no database
```

The store tests create and drop a throwaway database per test, so each one also
re-runs the migrations from an empty database. They skip themselves when
`TEST_DATABASE_URL` is unset.

## How it works

### Google Form and player check-in

The CSV importer accepts **Display name**, **Email or club ID**, and **Skill band**
(required), plus **Estimated arrival time** and **Need a paddle?** (optional).
Paddle answers are Yes, No, or blank. Existing three-column exports still work.
CSV imports remain a setup action; Google Forms do not automatically synchronize.

During a live event, open **Add player**, enter their details, and leave **Check
player in immediately** checked to join the ready queue. **Check in a player**
on the event page jumps to the existing roster. Adding someone never edits a
match already underway. Returning players retain their existing identity and
rating. The public board contains names and attendance counts, never private
identifiers, arrival answers, or equipment needs.

### The matcher

`internal/matchmaking` is one pure function over a slice of candidates: no HTTP,
no database, no clock.

1. Sort the ready players by `ready_since`, oldest first, with player ID as the
   tie-break so the result is deterministic.
2. Take the first four. **Waiting order wins over balance**: nobody is skipped to
   make the teams closer.
3. Try the three ways four players can be split into two doubles teams, and keep
   the one with the smallest gap between team average ratings.

That is an `O(n log n)` sort plus three constant-size comparisons — not a search
over every possible quartet. On this laptop it is 358 ns for 12 players and
4.9 µs for 50, against a request that takes about 10 ms.

### One event lock, every write

Every mutation follows the same rule:

```
BEGIN
SELECT ... FROM events WHERE id = $1 FOR UPDATE   -- take the event's row lock
  read the current state
  validate
  write
COMMIT
```

Reading eligibility *after* taking the lock is the point. Two organizers
pressing "Next match" on the same court at the same moment cannot both see a
free court, because the second one waits at the lock and then reads the world
the first one created. This intentionally serializes one small event's writes; a
process mutex would not, because it cannot coordinate two application processes.

Two partial unique indexes are the backstop if that rule is ever broken:

```sql
CREATE UNIQUE INDEX one_active_match_per_court  ON matches (court_id) WHERE state = 'active';
CREATE UNIQUE INDEX one_active_match_per_player ON match_players (player_id) WHERE active;
```

`internal/store/concurrency_test.go` proves both against real PostgreSQL on
separate connections: eight simultaneous requests for one court produce exactly
one match and eight identical answers; two courts sharing four ready players
produce one match and one honest refusal; and a forced failure halfway through
an assignment leaves no match, no memberships and nobody stuck as "playing".

### Retries and organizer mistakes

The rules are per-operation, not a general framework:

| Action | Repeat of the same request | Conflicting request |
| --- | --- | --- |
| Next match | Returns the existing match, `created: false` | — |
| Record result | Returns the saved result, `changed: false` | Different winner → **409** |
| Cancel match | No-op, `changed: false` | Already completed → **409** |
| Set player state | No-op; keeps the original queue position | Stale `expected_state` → **409** |

A phone that retries a request whose answer it never saw does not take a second
court. Pressing "ready" twice does not send someone to the back of the queue.
And a stale organizer page cannot silently overwrite a change another organizer
made a second earlier — it gets a 409 and refetches.

The board reads its event, queue, courts and results in one read-only,
repeatable-read transaction. Each response describes one database snapshot,
even when an organizer completes or assigns a match while the board refreshes.
Equal waiting times use player ID in both the displayed queue and the matcher.

Events move forward from setup to live to finished. Once finished, player and
court changes are refused and their controls are hidden. An organizer can delete
a finished event without saved Elo results from a separate confirmation page. Court creation and
later additions both enforce the eight-court limit and unique labels ignoring
case.

### Ratings

`internal/ratings` implements team Elo on the mean rating of each pair:

```
expectedA = 1 / (1 + 10^((B - A) / 400))
deltaA    = 32 × (actualA − expectedA)
```

`deltaA` is added to both team A players and subtracted from both team B
players, so the four-player total is unchanged. Two equal teams move ±16.

These are **provisional local numbers**, not a measure of anyone's real skill. A
doubles result cannot separate a player from their partner, and the seed comes
from a self-reported band (beginner 800, intermediate 1000, advanced 1200). The
ratings are off by default (`COURTOPS_RATINGS=false`) so a first pilot cannot
bake bad numbers into player history.

### Privacy by construction

The board is public, so the read model it is built from — `store.EventPlayer` —
simply has no field for the private identifier. An email address cannot reach a
public page by a template forgetting to omit it, and there is a test that checks
the board's HTML and JSON for the roster's addresses.

## Deployment

One EC2 host runs Caddy, the app and PostgreSQL under Docker Compose. Caddy
gets the certificate automatically; PostgreSQL publishes no port and is reachable
only on the internal network.

The app refuses to start in production without Google sign-in configured, unless
`COURTOPS_READ_ONLY=true`. There is no way to accidentally serve organizer
controls to the public, and CI checks that guard on every push.

For an existing deployment, preserve its Compose project name and database
volume, verify an encrypted backup with `scripts/restore-check.sh`, then replace
the app image outside a live event. Migration `0003_registration.sql` runs
automatically on startup. Keep production secrets in the host’s `deploy/.env`;
`deploy/.env.example` contains only placeholders.

The Release workflow builds and publishes images when manually dispatched.
Production deployment additionally requires the `production` GitHub environment,
EC2 SSH secrets, the existing Compose project name, and a verified backup key on
the host. Set the repository variable `COURTOPS_HEALTH_URL` to enable the health
monitor. IPv6 additionally requires AWS networking, DNS, and the optional
`deploy/compose.ipv6.yaml` overlay. Use the separately maintained deployment
guides for the full update, rollback, automation, and IPv6 procedures.

## Configuration

| Variable | Purpose |
| --- | --- |
| `DATABASE_URL` | PostgreSQL connection string (required) |
| `COURTOPS_ENV` | `development` or `production` |
| `COURTOPS_BASE_URL` | Public URL; must be https in production |
| `COURTOPS_SESSION_SECRET` | Signs the session cookie; 32+ bytes in production |
| `COURTOPS_ORGANIZERS` | Comma-separated Google emails allowed to operate events |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | Web-application OAuth client |
| `COURTOPS_READ_ONLY` | Serve the board, refuse every write |
| `COURTOPS_RATINGS` | Persist Elo changes on completion |
| `COURTOPS_DEV_ORGANIZER` | Local sign-in without Google; refused in production |

Invalid `COURTOPS_READ_ONLY` or `COURTOPS_RATINGS` values fail startup rather
than silently selecting a default. Production base and callback URLs must be
valid absolute HTTPS URLs. A read-only deployment with Google login configured
still needs a signing secret.

## Layout

```
cmd/courtops         the server
cmd/loadgen          synthetic board-polling load test
internal/matchmaking pure selection and team-splitting
internal/ratings     pure Elo maths
internal/roster      CSV parsing and validation
internal/store       all SQL, transactions and event locking
internal/web         handlers, templates, sessions, CSRF, Google sign-in
internal/config      environment parsing and production safety rules
migrations           versioned SQL, embedded in the binary
templates, static    embedded HTML and assets
```

## Limits

These are real and are deliberately not worked around:

- **Single host.** An app restart keeps the database volume, but losing the host
  and its storage means restoring from backup. There is no high-availability
  promise.
- **Confirmed results are final.** Once a rated result is saved it cannot be
  edited; a different winner returns 409. Correcting rating history would mean
  replaying it, which is a bigger system than this.
- **Ratings are provisional and local.** They mean nothing outside this club.
- **No offline mode.** During a network outage, pause writes and use the paper
  fallback, then reconcile from the database afterwards.
- **One live event at a time**, one sport, one club.
- **No notifications, queues, workers or caches.** Every action finishes inside
  its request.

## Repository privacy

The configuration examples and test rosters contain synthetic values. Real
credentials, private keys, roster exports, and database backups belong outside
Git. `.gitignore` excludes common secret files, and `.dockerignore` restricts
builds to application source and embedded assets. Supply production credentials
through the host environment and GitHub Actions secrets.
