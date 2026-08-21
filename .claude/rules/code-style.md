# Code Style

- **Minimalist**: write only the code that is needed now. No speculative helpers, options, or abstractions for a caller that does not exist yet.
- **Few comments**: comment only where the code cannot explain itself. No function or class header comments unless I ask.
- **No docstrings unless I ask** — this holds even in a file where every other function has one.
- **Log, don't print**: diagnostics go through `logging`, not `print`.
