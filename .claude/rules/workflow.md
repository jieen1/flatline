# Workflow

- **Look for existing code before adding a file**: search for functionality that already exists (`**/download*.py`, `**/fetch*.py`, and so on) and extend it. Create a new file only when nothing there fits.
- **Read before you edit**: read the code you are about to change, and the code it calls, first. Then act on what you find — check in only when two readings of the request would lead to materially different work.
- **In an interactive session with a human, never commit or push before asking.** The autonomous sprint agents (dev / QA / reviewer / gatekeeper / lead under `sdlc/teams/`) commit, push and merge exactly as their runbooks dispatch — that is their job, not an exception to seek permission for.
- **Show the run, not a claim**: before reporting work as done, actually run it — the test, the script, the command — and paste the real output. If you could not run it, say that plainly rather than describing what would have happened.
