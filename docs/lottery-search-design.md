# Lottery Search System — design proposal

Part 2 of the challenge. Design only, no implementation.

Search 10 million lottery tickets by a 6-character pattern of digits and `*`
wildcards, fast, and never hand the same ticket to two users at once.

---

## 1. The arithmetic comes first

Three numbers decide the whole design, so they go before any technology choice.

| | |
| --- | --- |
| Distinct values a 6-digit number can take | 10⁶ = **1,000,000** |
| Tickets | 10⁷ = **10,000,000** |
| Distinct patterns (each position is a digit or `*`) | 11⁶ = **1,771,561** |

**The ticket population is ten times larger than the value space it is drawn
from.** By the pigeonhole principle the dataset cannot hold 10 million distinct
6-digit numbers; on average **ten tickets share every number**. That is also how
real lotteries work — many physical tickets carry the same number.

So "search 10 million records" is misleading. The *key space* being searched is
one million, and it is dense: every key from `000000` to `999999` exists.

That single fact removes the hard part of the problem. There is no need for a
text search engine, a regex scan, a trigram index or a suffix structure. A
pattern does not have to be *matched* against anything — the set of numbers it
covers can be **computed directly**, because a pattern is just a partially
specified integer.

> If the brief instead meant 10 million *distinct* numbers, then the numbers are
> not 6 digits and the space is 10⁸. Section 9 covers that reading; the design
> survives it with one constant changed.

---

## 2. What a pattern actually is

A pattern splits into a **mask** (which positions are wildcards) and the **fixed
digits**. There are 2⁶ = 64 masks.

Write the fixed digits as a base value and give each free position its place
value:

```
pattern  1 * * * * 5
         │         └── fixed, place value 10⁰ ─┐
         └── fixed, place value 10⁵ ───────────┤
                                               ├─ base = 1·100000 + 5·1 = 100005
             free positions: 1,2,3,4 ──────────┘  weights 10⁴ 10³ 10² 10¹

matches = { 100005 + a·10⁴ + b·10³ + c·10² + d·10¹  |  a,b,c,d ∈ 0..9 }
```

Enumerating the matches is a mixed-radix counter plus an add — no string
comparison, no branching, no hashing. A pattern with `w` wildcards matches
exactly **10^w** numbers.

### 2.1 The match set is a union of contiguous runs

This is the part worth exploiting. If the *trailing* `t` positions of the
pattern are wildcards, those positions contribute every value from `0` to
`10^t − 1`, which is a **contiguous interval** of numbers. The higher free
positions then place that interval at `10^(w−t)` different offsets.

> **Cost of a pattern is set by its number of runs, not its number of matches:**
>
> `runs = 10^(w − t)`, each of length `10^t`
>
> where `w` = total wildcards, `t` = trailing wildcards.

Applied to the brief's own examples, plus the two extremes:

| Pattern | `w` | `t` | Matches | **Runs** | Run length |
| --- | --- | --- | --- | --- | --- |
| `123***` | 3 | 3 | 1,000 | **1** | 1,000 |
| `1*2*3*` | 3 | 1 | 1,000 | **100** | 10 |
| `****23` | 4 | 0 | 10,000 | **10,000** | 1 |
| `1****5` | 4 | 0 | 10,000 | **10,000** | 1 |
| `******` | 6 | 6 | 1,000,000 | **1** | 1,000,000 |
| `*****5` | 5 | 0 | 100,000 | **100,000** | 1 |

Two consequences that are not obvious and that shape the rest of the document:

1. **`******` is the cheapest pattern in the system**, not the most expensive.
   It is one range scan over the whole key space. The matches are many, but the
   user only wants the first few.
2. **The expensive patterns are the ones that pin the units digit**, because
   every match becomes its own run.

Enumerating all 64 masks gives the cost distribution over the whole pattern
space, which is lopsided enough to design around:

| Runs to probe | Patterns | Share | Cumulative |
| --- | --- | --- | --- |
| 1 | 1,111,111 | 62.72% | 62.72% |
| 10 | 543,210 | 30.66% | 93.38% |
| 100 | 106,310 | 6.00% | 99.38% |
| 1,000 | 10,410 | 0.59% | 99.97% |
| 10,000 | 510 | 0.03% | 99.9994% |
| **100,000** | **10** | 0.0006% | 100% |

Nearly two thirds of all possible patterns resolve to a **single contiguous
range**, and the 100,000-run ceiling is reached by exactly **ten** patterns out
of 1.77 million — the ten spellings of `*****d`. The worst case is real and has
to be handled, but it is not the case to optimise for.

This also explains why the trick people usually reach for — *store the number
reversed and index it so suffix patterns become prefix queries* — is not worth
it. It converts suffix patterns into cheap ones and leaves the other 62 masks
exactly as they were. Enumeration is uniform across all 64.

---

## 3. Recommended storage

**PostgreSQL as the single system of record. Redis and an in-process bitmap as
optional read accelerators, added only when measurement says they are needed.**

### Why Postgres

Lottery tickets are money and inventory. The binding requirement is not search
speed — section 2 showed search is nearly free — it is that **a ticket is
allocated to exactly one buyer, durably, with an audit trail**. That is a
transactional requirement, and it is the thing most datastores are worst at.

- 10 million narrow rows is a small table. This is not a scale problem for
  Postgres; it is a correctness problem that happens to involve 10 million rows.
- `SELECT … FOR UPDATE SKIP LOCKED` is a built-in primitive for *exactly* the
  requirement "two concurrent searchers must not receive the same row". It is
  not a workaround; it is the feature, and it has been load-bearing in payment
  and queue systems for a decade.
- One datastore means one backup, one failover story, one consistency model, and
  no reconciliation job between a cache and the truth. The brief lists
  **operational simplicity** as a judging criterion, and a design that needs
  Redis to be correct fails it.

### Why not the usual candidates

| Option | Why not |
| --- | --- |
| Elasticsearch / OpenSearch wildcard query | Solves the easy half. Offers no allocation primitive, so the no-duplicate rule would need an external lock anyway — and then the search engine is pure cost. Near-real-time refresh also means it can offer tickets that are already sold. |
| Redis alone | `SPOP` gives perfect atomic allocation (see 5.3) but Redis is not where a record of sale belongs. Durability is a tuning knob, not a guarantee. Use it in front of Postgres, never instead. |
| MongoDB | `findAndModify` can allocate one document atomically, so it is workable. But there is no `SKIP LOCKED` equivalent for batch allocation, so handing out N tickets becomes N round trips with no way to express "skip what is contended". |
| Key–value store keyed by number | Recreates the dense-array idea, which is right, but then every sale needs a transaction the store does not have. |
| In-memory only (the whole thing fits) | 10⁶ slots genuinely fit in RAM, and this is tempting. Rejected because inventory must survive a restart, and a single process cannot be the system of record for money. |

### Schema

```sql
CREATE TYPE ticket_status AS ENUM ('available', 'reserved', 'sold');

CREATE TABLE ticket (
    id          bigint        PRIMARY KEY,
    number      int           NOT NULL CHECK (number BETWEEN 0 AND 999999),
    status      ticket_status NOT NULL DEFAULT 'available',
    reserved_by uuid,                      -- session holding the soft lock
    reserved_at timestamptz
);

-- The only index the allocation path needs. Partial, so sold tickets —
-- eventually the majority — are not in it at all.
CREATE INDEX ticket_allocatable ON ticket (number)
    WHERE status <> 'sold';

-- The record of sale is its own table, not two more columns on the ticket, so
-- that "a ticket is sold at most once" is a constraint the database enforces
-- rather than an invariant the application is trusted to maintain.
CREATE TABLE sale (
    ticket_id bigint      PRIMARY KEY REFERENCES ticket (id),
    buyer_id  uuid        NOT NULL,
    sold_at   timestamptz NOT NULL DEFAULT now()
);
```

The primary key on `sale.ticket_id` is what makes double-selling impossible: a
second attempt is a constraint violation, whatever the application believes
about the ticket's status. Purchase is then one transaction,

```sql
BEGIN;
  -- fails outright if this ticket has already been sold
  INSERT INTO sale (ticket_id, buyer_id) VALUES ($ticket, $buyer);

  -- zero rows updated means the reservation lapsed under the buyer: roll back
  UPDATE ticket SET status = 'sold'
   WHERE id = $ticket AND status = 'reserved' AND reserved_by = $session;
COMMIT;
```

where the `INSERT` prevents selling twice and the `UPDATE`'s row count prevents
selling a reservation that has already expired. `ticket.status` is then a cache
of whether a `sale` row exists, which is what keeps the partial index small.

`number` is stored as an `int`, not as text. Patterns are resolved to integer
ranges before the query is issued, so the database never sees a wildcard.

---

## 4. Two different operations, deliberately separated

The brief says *search* must not return the same ticket twice. Taken literally,
every search would reserve inventory — which means one crawler issuing
`GET /search?pattern=******` in a loop drains the entire lottery.

So the API splits the requirement in two:

| | Reserves? | Consistency | Cacheable |
| --- | --- | --- | --- |
| `GET /patterns/{pattern}` — how many are available, sample numbers | no | eventual | yes |
| `POST /reservations {pattern, quantity}` — give me N tickets | **yes, atomically** | strict | no |

The no-duplicate guarantee lives on the allocating call, which is the one that
hands a user something they can act on. Browsing stays free and abuse-resistant.

**This is an interpretation of requirement 3, and it is the only place this
document departs from a literal reading of the brief.** If search itself must
reserve, then `POST /reservations` *is* that search and the browse endpoint is
simply an extra; nothing in the design changes.

---

## 5. Allocation — the no-duplicate mechanism

### 5.1 One statement

```sql
UPDATE ticket SET
    status      = 'reserved',
    reserved_by = $session,
    reserved_at = now()
WHERE id IN (
    SELECT id FROM ticket
    WHERE number >= $run_lo AND number <= $run_hi      -- one run from §2.1
      AND ( status = 'available'
            OR (status = 'reserved' AND reserved_at < now() - $ttl) )
    LIMIT $remaining
    FOR UPDATE SKIP LOCKED
)
RETURNING id, number;
```

Issued once per run, first fit, stopping as soon as `quantity` tickets are
collected. Properties:

- **`SKIP LOCKED` is the guarantee.** Two sessions running this concurrently for
  the same pattern cannot select the same row: the first locks it inside its
  transaction, the second steps over it and takes a different one. No advisory
  locks, no application-level mutex, no queue.
- **Select and reserve are the same statement.** There is no window between
  "decided to offer this ticket" and "marked it as offered", so there is nothing
  to reconcile if the process dies mid-request.
- **Partial results are correct.** If a pattern has 3 tickets left and the user
  asks for 10, they get 3. The alternative — all-or-nothing across runs — would
  need a cross-run transaction for no benefit, since any matching ticket is
  interchangeable with any other.

### 5.2 Reservation expiry without a cron job

Note the `OR (status = 'reserved' AND reserved_at < now() - $ttl)` clause. An
abandoned reservation becomes allocatable again **the moment it is old enough,
because the allocation query says so** — not when a sweeper gets round to it.

This matters more than it looks. The common design puts a background job in
charge of releasing expired holds, which makes that job load-bearing for
revenue: if it stops, inventory silently disappears and nobody notices until
someone counts. Here a dead sweeper costs nothing but tidy statistics.

A sweeper still runs, hourly, to normalise rows so that dashboards and
`COUNT(*) WHERE status='available'` tell the truth. **Correctness does not
depend on it.**

### 5.3 If Postgres becomes the bottleneck

Only then, and only on the serving path:

- **Availability bitmap** — 10⁶ bits = **125 KB**, bit `n` = "number `n` has at
  least one allocatable ticket". That is small enough to sit in every API
  process and be rebuilt from scratch in one query. Its job is to skip empty
  runs before issuing SQL, which is what turns the 100,000-run worst case from
  100,000 statements into roughly as many statements as there are tickets to
  hand out. Staleness is safe in both directions: a false positive costs one
  wasted query, a false negative under-offers a ticket that is still there.
- **Per-number available sets in Redis** — `SPOP key 1` is atomic on a single
  key, so it allocates correctly with no transaction and no cross-slot Lua.
  Because the no-duplicate rule applies per ticket rather than per batch, the
  batch does **not** need to be atomic, and the usual Redis Cluster hash-slot
  problem never arises. Postgres still records the sale.

Both are caches. Neither is required for the guarantee in 5.1.

---

## 6. Counting without scanning

`GET /patterns/****23` wants "1,284 available". Counting by enumeration means
touching up to 100,000 runs, which is the one genuinely expensive read.

Every ticket belongs to exactly **2⁶ = 64 patterns** — one per mask. So keep a
counter per pattern and maintain it on write:

- **1,771,561 counters**, one for each possible pattern. At 8 bytes that is
  about **14 MB** — the entire answer space for every query the system can be
  asked, resident in memory.
- A sale decrements **64** counters; a reservation expiry increments 64. Both
  are a single pipelined round trip.
- Any count is then O(1), with no scan and no lock.

This is an aggregate cube over the 64-element mask lattice, and it is only
affordable because 11⁶ happens to be small. Worth noting as a tradeoff: it moves
cost from reads to writes, so it earns its place only if counts are read more
often than tickets are sold — which, for a lottery storefront, they are, by a
wide margin. If not, drop it and count by enumeration.

---

## 7. Performance analysis

Everything below is derived from the arithmetic in sections 1 and 2. **None of
it is measured** — section 10 lists what has to be benchmarked before any of
these numbers are trusted.

| Operation | Work | Bounded by |
| --- | --- | --- |
| Parse pattern → mask + base | 6 character reads | constant |
| Enumerate matching numbers | one add per match | 10^w ≤ 10⁶ |
| Runs to probe | `10^(w−t)` | **≤ 100,000**; ≤ 100 for 99.4% of patterns (§2.1) |
| Runs actually probed when stock is normal | ≈ `quantity` | user's request size |
| Bitmap filter per run | one bit test, 125 KB working set | fits in L2 |
| Allocate N tickets | ≤ N index lookups + 1 write | `quantity` |
| Count for any pattern | 1 counter read | constant |

The important line is the fourth. With 10 million tickets over 1 million
numbers, the mean number holds 10 tickets, so a user asking for 10 tickets
normally gets them from the **first run probed**. The 100,000-run ceiling is not
the expected cost — it is what the system degrades to as a pattern approaches
sold out, and the bitmap is what keeps that degradation from reaching the
database.

### Tradeoffs taken

| Decision | Bought | Paid |
| --- | --- | --- |
| Enumerate integers instead of indexing text | Uniform cost across all 64 masks; no search engine to operate | Must special-case nothing, but must compute runs correctly |
| Partial index on `status <> 'sold'` | Index shrinks as the lottery sells through | Heap filter on the TTL predicate |
| TTL enforced at read time | A dead sweeper cannot leak inventory | Allocation query is slightly more complex |
| First-fit, not `ORDER BY random()` | No sort over 10,000 candidate rows | Needs the rotation in 8.1 to avoid hotspots |
| Pattern-count cube | O(1) counts | 14 MB, and 64 counter writes per sale |
| Postgres over Redis-first | One source of truth, real durability | Lower ceiling before caching is needed |

---

## 8. Concurrency, fairness and scale

### 8.1 Hot patterns

If every user searches `****23` and allocation always starts at the lowest
number, all of them contend on the same rows. `SKIP LOCKED` keeps them from
blocking, but they still do wasted work.

Fix: start the run enumeration at a **random offset** and wrap around. The
matching numbers are an indexed sequence `f(0) … f(10^(w−t) − 1)`, so starting
at a random `i₀` and walking `i₀, i₀+1, … (mod runs)` spreads concurrent
searchers across the whole candidate set. It costs one random number and keeps
every access index-ordered — unlike `ORDER BY random()`, which sorts.

### 8.2 Sharding, if it is ever needed

A pattern spans numbers, so sharding **by number** would make every search
fan out to every shard. Shard **by ticket id** instead:

- Each shard holds a disjoint slice of the inventory, every shard covering the
  full number range.
- A search fans out, each shard allocates independently with the statement in
  5.1, and the coordinator merges and trims to `quantity`.
- **No cross-shard transaction is required**, because the constraint is "this
  ticket goes to one user", not "this batch is globally ordered". Over-allocation
  across shards is returned by cancelling the surplus reservations, which expire
  on their own anyway.

Allocation therefore scales linearly in shards. This is a property of the
problem, not of the implementation: interchangeable inventory parallelises.

### 8.3 Failure modes

| Failure | Consequence | Why it is contained |
| --- | --- | --- |
| Two users allocate the same pattern simultaneously | disjoint ticket sets | `SKIP LOCKED` |
| Client abandons a reservation | ticket returns after the TTL | read-time TTL (5.2) |
| Sweeper job stops | statistics drift; no inventory lost | correctness is in the query |
| Bitmap stale (false positive) | one wasted query | re-checked by SQL |
| Bitmap stale (false negative) | a ticket is temporarily not offered | rebuilt periodically from Postgres |
| API process dies mid-allocation | reservation stands, then expires | reserve is the same statement as select |
| Double-sell attempted | rejected | primary key on `sale.ticket_id` |
| Pattern sold out | fewer tickets than asked for, or none | partial results are a defined outcome |

---

## 9. If the dataset is not what section 1 assumes

The design rests on "dense key space, many tickets per key". Both ways that can
be wrong:

- **10 million distinct numbers.** Then numbers are not 6 digits. For 8-digit
  numbers the key space is 10⁸, the bitmap grows from 125 KB to 12.5 MB (still
  trivial), the pattern cube to 11⁸ ≈ 214 million counters (**no longer
  affordable** — drop it and count by enumeration), and the run ceiling rises to
  10⁷. Enumeration and `SKIP LOCKED` are unaffected.
- **Sparse key space** — only a few thousand numbers in use. Then enumeration
  wastes effort on numbers that do not exist, and the bitmap becomes the primary
  index rather than a filter: iterate set bits within each run instead of every
  value. Same structures, different loop.

---

## 10. What to measure before trusting this

The honest limit of a design document is that every performance claim in it is
arithmetic, not evidence. Before committing:

1. **Allocation under contention.** N concurrent clients, one hot pattern,
   `quantity = 10`. Measure p99 latency and verify zero duplicate ticket ids
   across all responses — that is the requirement, and it is directly testable.
2. **The 100,000-run worst case.** Drive a `*****5` pattern down to near
   sold-out and measure allocation latency with and without the bitmap. This is
   the number that decides whether the bitmap ships at launch or later.
3. **Index behaviour as the lottery sells through.** The partial index shrinks
   while the heap does not; confirm the planner keeps choosing it at 10%, 50%
   and 95% sold.
4. **Cube write amplification.** 64 counter updates per sale, at peak sales
   rate — confirm it stays within one round trip and does not become the write
   bottleneck it is meant to relieve.
5. **TTL predicate cost.** The `OR status = 'reserved'` branch is a heap filter;
   measure it with a deliberately large pool of expired reservations.

---

## Summary

- A 6-digit pattern is a partially specified integer, so matching is
  **computation, not search**. No text index, no search engine.
- Cost follows **runs** (`10^(w−t)`), not matches — which makes `******` cheap
  and `*****5` the worst case, the opposite of the intuitive answer.
- The real requirement is allocation, not retrieval. **`SELECT … FOR UPDATE SKIP
  LOCKED`** satisfies "never the same ticket twice" directly, in one statement
  that both selects and reserves.
- **Expiry is enforced at read time**, so no background job is load-bearing for
  inventory.
- Postgres alone is sufficient and is the recommendation. A 125 KB bitmap and a
  14 MB count cube are the two accelerators worth adding, in that order, when
  measurement calls for them.
