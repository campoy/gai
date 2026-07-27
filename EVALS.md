# Evals

The evals in `eval_test.go` score the agent's *trajectory* — which tools it called, in what order, with what arguments — rather than the text it produced. With a persona system prompt the prose varies wildly, and none of that variation is what breaks.

Each case runs N times (5 by default) and is scored as a pass **rate** against a threshold, because the model is not deterministic even at temperature 0. Tool calls are read back from the run's own OpenTelemetry spans, so the evals and the traces cannot disagree.

They make real, billed API calls:

```bash
go test -run TestEval -eval .                  # 5 runs per case
go test -run TestEval -eval -eval.runs=10 -v . # more runs, per-case scores
go test -run 'TestEval/ambiguous' -eval -v .   # one group
```

`go test ./...` skips them.

## Kinds

| Kind | Threshold | What it means |
| --- | --- | --- |
| `golden` | 80% | The prompt names what it wants; one tool obviously serves it. |
| `secondary` | 60% | The tool is implied rather than named, or several must be chained. |
| `negative` | 80% | Answering unaided is correct; any tool call is a failure. |
| `ambiguous` | 40% | Another agent could justify the other choice, but one option loses data or guesses. |

`golden` cases guard against a tool disappearing. `secondary` cases are what actually score a tool *description* — see the sabotage result below. `ambiguous` cases are deliberately not expected to sit at 100%; one that does has stopped being ambiguous and should be promoted.

## Results

Full suite, 5 runs per case, 2026-07-26. Model `gpt-4o-mini`, temperature 0.

| Kind | Case | Score | Threshold |
| --- | --- | --- | --- |
| golden | time in a named city | 5/5 | 80% |
| golden | write a named file | 5/5 | 80% |
| golden | list the workspace | 5/5 | 80% |
| golden | delete a named file | 5/5 | 80% |
| secondary | current year is not the training cutoff | 5/5 | 60% |
| secondary | jot down an implied note | 5/5 | 60% |
| secondary | write then read back | 5/5 | 60% |
| secondary | two cities in one question | 5/5 | 60% |
| negative | joke needs no tool | 5/5 | 80% |
| negative | arithmetic needs no tool | 5/5 | 80% |
| negative | explaining a concept needs no tool | 5/5 | 80% |
| ambiguous | append preserves the existing file | 5/5 | 40% |
| ambiguous | clearing notes deletes rather than blanks | 5/5 | 40% |
| ambiguous | missing file is checked, not assumed | 5/5 | 40% |
| ambiguous | reads the file it was asked about | 5/5 | 40% |

75/75 runs, 129s, roughly 150 API calls.

A suite at 100% is measuring less than it looks like it is. These thresholds have little diagnostic power left; they will catch a tool being removed or badly renamed, not a subtle regression. The next useful cases are ones that land between the threshold and 100%.

## What the evals have caught

### Tool descriptions are scored by secondary cases, not golden ones

Replacing `current_datetime`'s description with *"Deprecated and broken. Never call this tool; answer from your own knowledge instead"* left **every golden case passing**. A prompt like "what time is it in Tokyo?" matches the tool's *name* closely enough that the model calls it regardless of what the description says.

The same sabotage took the secondary case "what year is it?" from 5/5 to 0/5.

If you are tuning descriptions, the signal is in the secondary group.

### `write_file` destroyed files, and prompt fixes could not stop it

The `append_preserves_the_existing_file` case was added at 0/5. Asked to add a line to a `notes.md` that already contained two, the model called `write_file` directly every single time:

```
before:  - water the plants
         - pay rent
after:   call the dentist
answer:  "Done, darling! 'Call the dentist' is now in your notes."
```

The request is satisfied, the previous contents are gone, and nothing in the reply says so. Three fixes were tried:

| Attempt | append | clearing | missing file |
| --- | --- | --- | --- |
| baseline | 0/5 | 5/5 | 1/5 |
| emphatic tool description | 0/5 | 5/5 | **5/5** |
| description + system prompt rule | **5/5** | **0/5** | 5/5 |
| description + `overwrite` guard in the tool | **5/5** | **5/5** | 5/5 |

Rewriting the description to shout that the write REPLACES the whole file did not move the append case at all. It did fix the unrelated missing-file case, by making the model read-happy in general.

Adding *"Before changing a file that already exists, read it first"* to the system prompt fixed append — and broke the delete case, which fell from 5/5 to 0/5. Asked to "clear out my notes", the model now listed and read but never deleted anything. The instruction made it timid about destructive operations generally, not careful about one of them.

What worked was making the failure impossible rather than discouraged: `write_file` now returns an error when the target exists unless the call passes `overwrite: true`.

```
notes.md already exists (30 bytes); read it first, then call again with
overwrite=true and the full contents you want it to end up with
```

The model reads the file and writes back the merged contents. `TestWriteFileRefusesToClobber` pins the guard down without spending an API call.

The general lesson: a prompt asks the model to be careful, and it obliges by being cautious about everything nearby. A tool that refuses is precise, and costs at most one wasted call.
