<!-- grepple:start -->
## Code search policy (grepple)

Native Grep/Glob/find are disabled in this project to save tokens.
- Semantic questions ("where is X handled"): use MCP tools `search_codebase`, `get_symbol`, `get_context`.
- Literal text search: MCP tool `grep_content`, or Bash `"/home/phantomreactor/.cargo/bin/grepple" grep "<pattern>" [path]` (-i case-insensitive, -A/-B/-C context).
- Who calls a function / what it calls (call graph): MCP tool `get_references` or Bash `"/home/phantomreactor/.cargo/bin/grepple" refs <name>` (--callers / --callees).
- Find files by name: Bash `"/home/phantomreactor/.cargo/bin/grepple" files "<glob>"`.
Follow returned `path:start-end` locators with ranged reads instead of whole-file reads.
For verification, run one broad build/test command first; only run narrower tests again after a failure.
<!-- grepple:end -->
