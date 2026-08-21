# DRY Principle

Applies to code you are already writing or editing. Do not refactor untouched code
as a side effect of another task — mention what you noticed and let me decide.

- **Extract duplicated logic**: when if/else, try/except, or other branches carry identical or near-identical blocks, pull the shared part into a helper. Past roughly 5 repeated lines it is worth extracting.
- **Branches that differ only by context**: when branches vary only by a flag, a config value, or an environment, put the shared flow in one function and leave only the per-branch setup in the branches.
- **One responsibility per function**: give each distinct operation its own function, small enough that another caller can reuse it.
