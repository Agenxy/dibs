# Matching work, not words

Status: design proposal. This deliberately replaces parts of the matching model in
`SPEC-CHANNELS.md`; it is not a description of the current implementation.

## Decision

Lanes should stop treating duplicate-work detection as one similarity score.

There are three different facts in play:

1. two agents are attached to the same durable work item;
2. two agents may be pursuing the same objective; and
3. two agents may touch the same implementation surface.

Those facts have different meanings and justify different actions. A shared PR is a
good reason to put agents in one coordination lane. Two writes to the same file are a
good reason to create awareness. Similar prose is a good reason to retrieve a
candidate. Only the first is strong enough to act on without another judgement.

The replacement is a hybrid:

- a **high-recall candidate generator** using exact keys, paths, and optionally
  semantics;
- a **typed evidence record**, preserving whether evidence was declared, observed,
  or model-inferred;
- a **conservative rule cascade** that maps evidence to actions; and
- eventually, a calibrated pairwise model inside the uncertain tier, but never in
  front of exact identity rules and never as the authority for auto-join.

There should be no weighted sum in which enough weak signals can add up to an exact
one. “Same repo + same branch + similar prose + two guessed files” must not become
equivalent to “both named `github:org/repo:pr:1231`.”

The unit of matching should be **one active slot against another active slot**. A lane
is the place matched work coordinates; it is not the unit whose ever-growing union of
members, refs, and files should be classified.

## What the current code is actually doing

The production reports are consistent with the implementation.

`internal/overlap/lexical.go` turns individual declaration tokens into path postings,
then expands those paths through commit-message history and co-change. The resulting
paths are hypotheses. `internal/core/channel.go` compares the hypotheses with weighted
Jaccard and returns their intersection as `Shared`, which makes a hypothesis read like
an observation. That is how “gate” becomes `pr-gate.yml` and then comes back as
apparently concrete shared-file evidence.

Adding declared dirs at weight 1 is directionally right and structurally wrong. It
puts declared directories and guessed files in the same untyped vector, retains the
bad guesses, and compares path strings exactly rather than as directory ancestry. It
does not answer whether the objective is shared; it merely makes one kind of path
entry heavier than another.

The recent refs change is also not yet independent of the scorer:

- `matchDeclaration` returns `matchedNoOpinion` when file prediction is empty before
  it asks the core about refs.
- `MatchLanesRefs` says refs are decisive without a footprint, but skips a candidate
  channel with no footprint before comparing the refs.

More importantly, `channelRefs` collects **every slot ref from every member of the
lane**, not the slot that caused that member to join. An agent can join lane A, move
another slot to objective B, and make B look like an objective of lane A. Similarly,
`Channel.Predicted` is a max-union of recorded member predictions. It does not describe
one current objective; it describes everything that has ever been folded into that
lane. Matching against these unions creates contagion: one broad or false match makes
the lane broader, making the next false match easier.

The repository guard prevents some automatic joins but still surfaces generic
cross-repository suggestions. `cwd` is being used as a late veto when repository
identity should instead gate whether path evidence is comparable at all.

Finally, the calibration target is not the product target. Commit-message-to-file
retrieval is a useful component test, but it cannot calibrate a duplicate-work action.
The new five-pair golden set proves the distribution gap: on its reproducing tree the
current tier 0 reports precision 0.33 and recall 0.50, while the declared rule reports
precision 1.00 and recall 0.50. Five pairs are an excellent regression fixture and not
enough data to set a production threshold.

## The relation Lanes should classify

“Overlap” should become a small relation vocabulary rather than a scalar presented as
truth:

| Relation | Meaning | Default action |
|---|---|---|
| `same_work_item` | Both slots name the same canonical work item or Lanes-issued coordination key | Put them in the same lane and say exactly why |
| `probable_duplicate` | Evidence suggests the same objective, but identity is not established | Interruptive suggestion; never auto-join by default |
| `complementary` | Same work item or objective, different roles such as implement/review | Coordinate in one lane; never say “stand down” |
| `surface_collision` | Declared or observed implementation paths overlap, while objectives are different or unknown | Awareness only |
| `related` | Worth reading or subscribing to, without a collision claim | Quiet candidate list |
| `unrelated` | Positive contradictory evidence exists | No action |
| `no_opinion` | Evidence is absent or inadequate | Say nothing beyond honest status |

The labels are intentionally not mutually reducible. A pair can be
`same_work_item + complementary + surface_collision`. That is more useful than a score
of 0.71 and avoids turning “related” into “duplicated.”

### Auto-join is not a duplicate verdict

The shipped declared/inferred split is a good emergency safety boundary: an inferred
score should not auto-join. I disagree that “declared” is the right long-term boundary.

- A canonical, repository-scoped PR id is a fact about identity.
- `goal:green-main` is a broad, self-authored label, even if two strings are equal.
- A declared directory is intent, not proof of a shared objective.
- Concurrent edits observed in a worktree are inferred by the daemon but are stronger
  evidence of a file collision than a declared directory.

The useful distinction is **what the evidence establishes**, not who stated it.

Even exact identity establishes “coordinate here,” not “one of you is redundant.” An
implementer and a reviewer should share a PR lane. They should not be told to stand
down. Duplicate wording requires compatible activities or an explicit human/agent
decision in addition to work-item identity.

## The input: one typed work-intent snapshot per slot

The classifier should receive the following normalized snapshot. Only `summary`,
`work_item`, `activity`, and `paths` need to be agent-authored.

```text
WorkIntent
  slot_id                 stable id of this concurrent unit of work
  summary                 current set_slot text
  work_item?              one primary durable id or URL
  activity                investigate | design | implement | review | verify | document | operate | other
  paths[]                 narrow expected files/directories, repo-relative when possible

  related_refs[]          context links; not duplicate keys
  repo_id?                derived canonical repository identity
  worktree_id?            derived checkout/worktree identity
  cwd, branch, host       captured registration facts
  parent?                 proven agent lineage
  started_at, updated_at  ledger facts
  observed_changes[]      attributed only when attribution is sound
  prior_relations[]       explicit join/leave/feedback decisions, not model guesses
```

### What to ask agents for

Keep `text`, but make it a summary rather than the primary machine key. Add:

1. **`work_item` — optional, singular, and literal.** Its schema description should
   say: “If the task already contains a PR, issue, incident, ticket, URL, or Lanes
   coordination key, copy it verbatim. Otherwise omit this field. Do not invent an
   id.” A single primary item is cheaper and more discriminating than a bag of refs.
2. **`activity` — a required enum for new clients.** Selecting `implement` or `review`
   is cheap, and it prevents the most obvious false duplicate: two agents deliberately
   performing complementary roles on the same item. A short stable enum plus `other`
   is preferable to a free-form role taxonomy. Legacy declarations without it
   normalize to `unknown`; absence must never block the declaration or silently mean
   `implement`.
3. **`paths` — optional and narrow.** The description should ask for up to a small
   number of paths the agent expects to change or is directly investigating. “Omit
   when unknown” is better than encouraging `/repo`, read-only reference files, or
   speculative directories.
   Existing `dirs` can be accepted as a compatibility alias.

Keep `refs` for compatibility and display, but split its meaning. A ref that can be
canonicalized into the primary `work_item` is promoted. Other refs become
`related_refs`; equality is supporting context, not automatic identity.
If an agent truly has several primary work items, it should use several slots; related
items can remain in `related_refs`. This keeps the unit being matched coherent.

Do not ask for `cwd`, branch, host, model, harness, title, repository, confidence, or
keywords. Lanes or the bridge already knows the first six, agents cannot calibrate
confidence, and keywords recreate the present problem. Do not add a routine
`excludes` field: agents will rarely fill it. Ask for a negative judgement only when
there is a concrete proposed match to judge.

The reliability rule is simple: an agent-authored field should either be a value the
agent can copy from its task or a small enum it can select. If it requires inventing a
taxonomy, it will be omitted, stale, or filled with plausible noise.

### Refs will not reliably be filled

The current tool description says to “ALWAYS pass refs.” Real tasks often arrive with
no durable id. That instruction will increase fill rate by producing invented values
such as `goal:green-main`, not by producing identity.

Lanes should measure three rates separately: field presence, successful
canonicalization, and collision precision. Presence alone is a vanity metric.

Canonicalization should be local and deterministic:

- A full provider URL becomes a provider/repository/type/number tuple.
- `pr:1231` is scoped by the derived repository identity; it must not collide with PR
  1231 in another repository.
- A Lanes-issued coordination key is globally unambiguous on that board.
- Broad labels such as `goal:*` and `gate:*` remain asserted context unless their
  namespace is explicitly configured as unique.

Known ids can also be extracted from `summary` and branch names, but extraction is
inferred evidence. It may propose a normalized value back to the agent; it must not be
silently upgraded to verified identity when ambiguous.

Repository identity also needs normalization. Worktrees that share a Git common
directory are one repository. Separate clones with the same normalized primary remote
can be treated as the same project. With no remote, the real path of the Git common
directory is the honest local identity; Lanes should report separate clones as
unknown rather than guessing from directory names. `worktree_id` is the real path of
the individual checkout root. Raw `cwd` prefix comparison is not repository identity.

### Drift and omission

No field stays honest forever. Match only live slots, include age in the evidence, and
make updating the existing `slot_id` the normal path. If the branch, HEAD, or observed
path set moves far from the declaration, the next Lanes interaction should ask for a
refresh. It should not rewrite the declaration on the agent’s behalf.

Absence is unknown, not negative. One missing `work_item` must not make a differing id
on the other side a contradiction. Different canonical primary work items are strong
negative evidence only when both sides supplied them; even then, duplicate tickets can
exist, so the pair may remain a quiet semantic candidate.

## Signals and their roles

| Signal | What it establishes | Use |
|---|---|---|
| Same canonical primary work item | Shared durable context | Exact candidate and auto-join to coordination lane |
| Same Lanes-issued coordination key | Explicit decision to coordinate | Exact candidate and auto-join |
| Same free-form/context ref | Shared vocabulary or umbrella goal | Supporting evidence only |
| Activity pair | Duplicate-like versus complementary work | Changes relation and wording |
| Canonical `repo_id` | Whether path spaces are comparable | Gate path evidence; not a positive score |
| Declared path ancestry | Intended implementation proximity | Surface awareness and probabilistic corroboration |
| Attributable changed paths | Actual implementation proximity | Stronger surface awareness and probabilistic corroboration |
| Branch/worktree identity | Urgency and attribution quality | Same worktree raises collision urgency; different branches are not a negative |
| Summary semantics | Similarity of stated objectives | Candidate retrieval and uncertain-tier feature |
| Text-to-code retrieval | Possible implementation surface | Candidate retrieval only; always labelled inferred |
| Co-change from a trusted seed path | Nearby implementation surface | Expand surface candidates; never promote a guessed seed into fact |
| Historical path frequency | Whether a shared path is distinctive | Discount path-collision evidence, not objective identity |
| Proven parent/child lineage | Whether two lanes are independent actors | Do not report a child as duplicating its parent’s assigned work |
| Prior explicit decline/feedback | A decision about this pair | Policy memory; never training truth if it was only an old model action |

`model`, `harness`, `title`, and standing lane description should not enter the
classifier. They are presentation metadata. Using them would introduce harness and
role correlations that will fail as soon as a different fleet composition appears.
`host` matters for ports, devices, and other exclusive resources, not for objective
identity.

### What “files touched” can honestly mean

Lanes does not observe editor or shell tool calls, and its ledger records coordination
actions rather than all work an agent performed. It must not claim otherwise.

The daemon can derive a useful path observation by snapshotting HEAD and worktree
status at slot creation and refresh:

- In a unique worktree with one active slot, dirty paths and commits since the
  baseline can be attributed to that slot with stated provenance.
- With several active slots in one agent/worktree, changes are worktree-level
  observations unless the agent associates them with a slot; Lanes must not guess
  which slot produced them.
- If multiple agents share one worktree, changes cannot be attributed to one agent.
  Report “shared worktree changed” rather than attaching the files to both.
- Reads are not edits and should not enter `observed_changes`.
- Commits predating the slot and co-change predictions are history, not observed agent
  behavior.

These observations are derived views. If a decision based on them is ledgered, the
normalized evidence and decision must be copied into the op so replay remains pure.

## Combination: candidate generation, relation, policy

### 1. Normalize

Build a `WorkIntent` from the slot and derived repository facts. Preserve source and
time on every evidence item. Never collapse declared directories, observed files, and
model-predicted files into one `[]PredFile`.

### 2. Generate candidates with high recall

Candidate generation is allowed to be noisy because it performs no action.

Retrieve active slots by:

1. exact canonical work item or coordination key;
2. comparable repository plus overlapping narrow declared/observed paths;
3. semantic nearest neighbors among active slot summaries; and
4. optionally, overlap among text-to-code retrieval results.

Co-change is useful after a declared or attributable changed path: it can find a
neighboring surface likely to collide. Expanding a path that was guessed from one
ordinary declaration word compounds inference and must remain low-confidence candidate
generation, not stronger evidence.

On a normal local board there may be only tens of active slots. Comparing every active
slot is cheaper and safer than maintaining a large code-vector index. Optimize only
when measured board size requires it.

Candidates should be matched slot-to-slot. After classification, group results by the
lane in which the matched slot coordinates. Do not classify against a lane-wide union.

### 3. Classify with a rule cascade

Recommended initial policy:

1. **Exact identity.** Same canonical work item or coordination key yields
   `same_work_item`. Join the coordination lane. Use `activity` to say either
   “possible duplicate implementation” or “complementary role.”
2. **Contradictory identity.** Different canonical primary items suppress a duplicate
   action. If paths overlap, emit `surface_collision`; if semantics are high, keep a
   quiet `related` candidate.
3. **Observed collision.** Concurrent writes to the same distinctive file or narrow
   subtree yield `surface_collision`. They do not yield “same objective.”
4. **Probable duplicate.** With no exact identity, require at least two independent
   kinds of support: strong objective semantics plus either a narrow declared scope,
   attributable distinctive changed paths, or a stable local coordination ref.
   Emit a suggestion only. No default auto-join.
5. **Semantic-only or inferred-code-only.** Return a quiet `related` candidate with
   provenance, not an interruptive duplicate warning.
6. **Otherwise abstain.** No score is evidence of no collision.

Directory breadth and path ubiquity should be computed from repository structure and
edit history, not merely the current handful of live lanes. Directory comparisons
must understand ancestor/descendant overlap. Root directories and ubiquitous build
files carry little weight. None of this changes a surface signal into objective
identity.

### 4. Keep action policy separate from classification

The same relation can have different presentation policies without retraining a
model. Suggested friction levels are:

- **automatic coordination:** exact identity only;
- **interruptive warning:** high-precision `probable_duplicate`;
- **awareness:** `surface_collision`;
- **quiet discovery:** `related`; and
- **nothing:** `unrelated` or `no_opinion`.

Every response should name the relation and provenance. “Both declared PR 1231” is
good evidence. “The lexical scorer inferred both may touch `pr-gate.yml`” is honest
but not shared-file evidence. No non-exact result should tell an agent to stand down;
it should ask the agent to inspect the candidate and decide.

### 5. A learned model can come later

Once enough labelled declaration pairs exist, a small calibrated model may rank or
classify the uncertain tier. Inputs should be the typed features above, with monotonic
constraints where possible. It should output a calibrated probability of
same-objective or duplicate relation, not one scorer-relative weighted Jaccard.

The exact-identity and contradictory-identity rules remain outside the model. The
model version, normalized features, evidence, operating threshold, and chosen action
must be recorded at the edge. The pure core folds the recorded result and never runs
the model.

## Evaluation that matches production

### Labels

The gold label is not “did the pair share a file?” It is a point-in-time judgement on
three axes:

```text
same_objective       yes | no | unsure
work_relationship    duplicate | complementary | independent | unsure
surface_collision    yes | no | unknown
```

This keeps a reviewer and implementer on one issue from being labelled duplicate and
keeps two unrelated jobs editing `ci.yml` from becoming a same-objective positive.

Gold labels should come from:

1. **Explicit pair feedback.** Add a cheap `match_feedback` call tied to a decision id:
   `duplicate`, `complementary`, `surface-only`, `unrelated`, or `unsure`, with an
   optional note. Agreement from both agents or human adjudication makes gold.
2. **Incident and artifact review.** Redundant PRs, duplicated patches, explicit
   stand-downs, handoffs, and humans’ task assignments are useful retrospective
   evidence. Final artifacts may label a snapshot but must not be used as features
   available at that earlier snapshot.
3. **Curated real fleet cases.** Keep and grow `GoldenSet`, with rationale and the full
   contemporaneous input. Genericize only when the failure shape still reproduces.
4. **Silver labels for mining only.** Shared refs, lane joins, leaves, and messages can
   find cases to review. They are not truth by themselves, especially when the old
   classifier caused the join.

The daemon should support local, opt-in shadow logging of pair snapshots, candidate
evidence, decisions, and later feedback. Privacy-sensitive text should remain local by
default.

### Sampling and splits

Evaluate the state the classifier actually sees: on every `set_slot`, pair the new
slot with the active slots that existed at that time. Preserve the number of peers and
the base rate. A dataset of arbitrary historical pairs will be dominated by easy
random negatives and report a uselessly high score.

Deliberately oversample hard negatives for diagnosis, then reweight results to the
production candidate distribution. Required hard negatives include:

- same repository, generic shared words such as CI, gate, docs, runtime, and CLI;
- same repository and branch, different objectives;
- same narrow directory, different canonical work items;
- same broad umbrella ref, different child work;
- same canonical item, complementary activities such as implement/review;
- shared or ubiquitous build files only;
- inferred file overlap where neither agent declared nor edited the file;
- different repositories with analogous scaffolding;
- stale slots whose agent has moved on; and
- parent and subagent work that is intentionally one assignment.

Required hard positives include:

- the same objective described with disjoint vocabulary;
- the same objective across languages, directories, branches, and worktrees;
- one terse declaration with missing paths and refs;
- the same defect described by symptom versus root cause; and
- duplicate investigation before any file is changed.

Split by time and by objective identity so paraphrases of one incident cannot land in
both train and test. Hold out entire repositories and fleet configurations as an
external-validity set. Model selection must not touch the production-incident golden
set.

In addition to independent pair tests, replay complete fleet traces in order. The
current lane-footprint union can turn one false match into later matches, which an
independent pair benchmark cannot see. Stateful evaluation should measure bad lane
memberships, erroneous cluster growth, alert load, and whether slot updates remove
stale evidence.

### Metrics and release gates

Report metrics at each action’s operating point, not one aggregate score:

- **automatic-coordination precision** and false auto-joins per 1,000 declarations;
- **interruptive duplicate-warning precision**, with a one-sided confidence bound;
- false duplicate warnings per 100 declarations and per agent-hour;
- candidate recall@k for genuinely same-objective pairs;
- recall of interruptive warnings, secondary to precision;
- abstention/coverage;
- confusion among duplicate, complementary, surface-only, and unrelated;
- stateful lane impurity and maximum erroneous cluster size; and
- latency and resource cost.

Do not optimize accuracy, AUROC, file recall, or MRR as the product metric. File
recall/MRR remain valid component metrics for text-to-code retrieval.

The existing 95th-percentile-negative rule permits roughly a 5% pairwise false-positive
rate by construction. With 19 peers, independent 5% errors imply a
`1 - 0.95^19 = 62%` chance that a declaration gets at least one false candidate. Even
if independence is imperfect, this is the wrong order of magnitude for an
interruptive system. The threshold should be chosen from an alert precision and
false-alert budget under realistic fleet size, not from an unlabeled repository’s
commit-pair percentile.

For an initial interruptive relation, require at least 99% measured precision, a
one-sided 95% lower confidence bound reported beside it, and fewer than 0.1 false
duplicate warnings per 100 declarations in trace replay. Candidate recall@5 should be
at least 95% on the hard-positive holdout; candidate misses are improved without
raising the friction tier. If the data cannot support the interruptive claim, keep the
result in the quiet candidate tier. For automatic coordination, use exact rules rather
than a statistical threshold and require zero known false identities in the gold and
replay suites. These are policy targets, not numbers to hide inside an F-score.

### What to do with the current commit benchmark

Keep it, rename its claim, and stop using it to set duplicate-work thresholds.

It answers: “from an after-the-fact commit subject, can this retriever recover files
that commit changed?” That is useful for regression-testing lexical and embedding
text-to-code retrieval, co-change, chunking, and model choice. It does not answer:
“from two before-the-fact agent declarations, should one agent interrupt or stand down
for the other?”

The production declaration-pair benchmark sets action policy. The commit benchmark
tests one optional candidate-generation component.

## Where embeddings help, and where they are theatre

Embeddings genuinely help in two places:

1. **Declaration-to-declaration candidate retrieval.** Paraphrases such as “stale
   owner handle” and “required review is not enforced” may share an objective without
   sharing tokens. Direct task-text similarity is appropriate for retrieving that
   candidate, even though it is not sufficient for a verdict.
2. **Declaration-to-code retrieval when scope is missing.** It can suggest likely
   implementation regions, which may retrieve another active slot. The paths remain
   inferred and must be labelled as such.

This disagrees with the current specification’s blanket statement that tier 2 must not
compare task descriptions directly. It must not **decide duplicate work** from that
comparison. Using it for high-recall candidate retrieval is exactly where direct
semantic similarity is useful, and text-to-code alone cannot find objective-level
duplicates implemented in disjoint files.

Embeddings are theatre when they:

- turn nearest code chunks into allegedly observed “shared files”;
- provide an absolute cosine threshold for auto-join;
- stand in for canonical id, repository, branch, path, or activity normalization;
- are compared only on commit-message file retrieval and called better at duplicate
  detection;
- add a large repository index when the board has twelve slot summaries that can be
  compared directly; or
- improve recall@20 while leaving precision at the action point unchanged.

A smaller model tying a larger one on the old benchmark means only that the old task
did not distinguish them. Select an embedding model on candidate recall over held-out
hard declaration pairs, then measure the end-to-end action precision after the rule
policy. If the larger model does not improve those numbers, it has not earned its
memory or startup cost.

## Delivery plan

### Smallest version that is a real improvement

Ship a structural, precision-first matcher before another semantic threshold:

1. Compare new slots to active slots, not to lane-wide unions.
2. Make normalized, repository-scoped primary work-item matching independent of file
   prediction. Only canonical work items and Lanes-issued coordination keys
   automatically coordinate.
3. Add `activity`; distinguish same-item complementary work from duplicate work.
4. Treat declared path ancestry as `surface_collision` awareness. Remove inferred
   lexical/code paths from duplicate wording and auto-join. If retained, use them only
   to retrieve quiet candidates and label them `inferred`.
5. Use canonical repository identity to gate all path comparison. Same repository is
   context, never positive evidence.
6. Keep the current no-auto-join-on-score default. Change every response so only exact
   identity says “same work item,” and no guess tells an agent to stand down.
7. Add decision ids and cheap relation feedback; run the new matcher in shadow mode
   against the growing declaration-pair set.

This version will miss the terse no-ref/no-path positive. That is an explicit recall
cost, visible in the golden set. It also removes the two reproduced false positives.
Given the asymmetric harm, that is a sound first release rather than a regression
disguised as caution.

### End state

The end state is a small evidence pipeline:

```text
active slot snapshots
        |
        v
exact/path/semantic candidate retrieval
        |
        v
typed pair evidence + calibrated uncertain-tier relation model
        |
        v
deterministic action policy
  exact -> coordinate
  probable -> interruptive suggestion
  path -> awareness
  semantic -> quiet candidate
  weak -> abstain
        |
        v
record decision, evidence, model version, and feedback in replayable ops
```

Embeddings and filesystem observations remain derived, rebuildable views outside the
core. The engine records the normalized evidence and chosen action exactly once; the
core folds it as fact. That preserves `state == fold(ledger)` while making the
decision auditable.

The goal is not a more elaborate score. It is a system that knows the difference
between identity, intent, behavior, and a guess—and imposes friction in proportion to
what it actually knows.
