// grepple hijack — managed by `grepple setup opencode`
const REDIRECT = "grepple: native search tools are disabled in this project. For semantic questions use the grepple MCP tools (search_codebase, get_symbol, get_context, get_references). For exact text search use the grep_content MCP tool or run bash: /home/phantomreactor/.cargo/bin/grepple grep \"<pattern>\" [path]. For file names run: /home/phantomreactor/.cargo/bin/grepple files \"<glob>\"."

export const GreppleHijack = async () => {
  const blockedFirstWords = ["grep", "egrep", "fgrep", "rg", "rga", "ag", "ack", "find"]
  return {
    "tool.execute.before": async (input, output) => {
      if (input.tool === "grep" || input.tool === "glob") {
        throw new Error(REDIRECT)
      }
      if (input.tool === "bash") {
        const command = String(output.args?.command ?? "").replace(/\$\(/g, "|").replace(/`/g, "|")
        const firstWords = command.split(/[|;&\n]/).map(seg => seg.trim().split(/\s+/)[0] ?? "")
        if (firstWords.some(w => blockedFirstWords.includes(w))) {
          throw new Error(REDIRECT)
        }
      }
    },
  }
}
