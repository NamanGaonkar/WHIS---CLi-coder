import { createMemo, createResource, Show } from "solid-js"
import { DialogSelect, type DialogSelectRef } from "../ui/dialog-select"
import { useSDK } from "../context/sdk"
import { usePromptRef } from "../context/prompt"
import { useProject } from "../context/project"

export type McpProjectDialogProps = {
  /** Slash command to prefill, e.g. "explore_codebase" */
  command: string
  /** Remaining args the user already typed (appended after the project) */
  presetArgs?: string
}

type Project = { name: string; root_path?: string }

function extractProjects(raw: unknown): Project[] {
  const content = (raw as { content?: Array<{ type?: string; text?: string }> })?.content
  const text = Array.isArray(content)
    ? content
        .map((item) => (typeof item?.text === "string" ? item.text : ""))
        .join("\n")
    : ""
  try {
    const parsed = JSON.parse(text)
    const list = parsed?.projects ?? parsed
    if (!Array.isArray(list)) return []
    return list
      .map((item: any) =>
        typeof item === "string"
          ? { name: item }
          : { name: String(item?.name ?? ""), root_path: item?.root_path ? String(item.root_path) : undefined },
      )
      .filter((item: Project) => item.name)
  } catch {
    return []
  }
}

const LIST_TOOL_CANDIDATES = ["list_projects", "list-project", "projects", "list_repos"]

/** True when the command template asks for an indexed project (MCP picker). */
export function wantsProjectSelection(template: string | undefined): boolean {
  return !!template && /project/i.test(template)
}

export function DialogMcpProject(props: McpProjectDialogProps) {
  const sdk = useSDK()
  const promptRef = usePromptRef()
  const project = useProject()
  let ref: DialogSelectRef<string>

  const [projects] = createResource(async () => {
    // Self-discovery: try every connected server for a project-list tool, so
    // any MCP server exposing one works without per-server configuration.
    const status = await sdk.client.mcp.status({ workspace: project.workspace.current() })
    const connected = Object.entries(status.data ?? {})
      .filter(([, value]) => (value as { status?: string })?.status === "connected")
      .map(([name]) => name)
    for (const server of connected) {
      for (const tool of LIST_TOOL_CANDIDATES) {
        try {
          const result = await sdk.client.mcp.tools.call({
            name: server,
            workspace: project.workspace.current(),
            tool,
          })
          const list = extractProjects(result.data)
          if (list.length) return list
        } catch {}
      }
    }
    return [] as Project[]
  })

  const options = createMemo(() =>
    (projects() ?? []).map((project) => ({
      title: project.name,
      description: project.root_path,
      value: project.name,
      onSelect: (dialog: { clear: () => void }) => {
        dialog.clear()
        const preset = props.presetArgs?.trim() ? ` ${props.presetArgs.trim()}` : ""
        promptRef.current?.set({ input: `/${props.command} ${project.name}${preset} `, parts: [] })
        promptRef.current?.focus()
      },
    })),
  )

  return (
    <Show
      when={!projects.error && (projects()?.length ?? 0) > 0}
      fallback={
        <DialogSelect
          ref={(value) => (ref = value)}
          title="MCP projects"
          options={[
            {
              title: projects.loading ? "Loading projects..." : "No indexed projects found",
              description: projects.error ? String(projects.error) : "Index a repository first, then try again.",
              value: "none",
              disabled: true,
            },
          ]}
        />
      }
    >
      <DialogSelect ref={(value) => (ref = value)} title="Select project" options={options()} />
    </Show>
  )
}
