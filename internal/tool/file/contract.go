package file

import toolcontract "github.com/uvwt/agentdock/internal/tool/contract"

const (
	ToolReadFile   = "read_file"
	ToolListDir    = "list_dir"
	ToolSearchText = "search_text"
	ToolFileEdit   = "file_edit"

	structuredPatchGrammarDescription = ` Structured envelope format:
*** Begin Patch
*** Add File: <path>
+<added line>
*** Delete File: <path>
*** Update File: <path>
[*** Move to: <new path>]
@@ [<optional context line>]
 <context line>
-<removed line>
+<added line>
[*** End of File]
*** End Patch
Add/Delete/Update operations may repeat in one envelope. Update lines use a leading space for context, '-' for removal, and '+' for addition; @@ may be used without context or with one context line.`
)

func InputSchema(name string) (map[string]any, bool) {
	stringProp := toolcontract.String
	intProp := toolcontract.Integer
	boolProp := toolcontract.Boolean
	boundedIntProp := toolcontract.BoundedInteger
	props := map[string]any{}
	var required []string

	switch name {
	case ToolReadFile:
		props["path"] = stringProp(PathDescription("Host path. Relative paths resolve from ~/AgentDock."))
		AddRuntimeProperties(props)
		props["start_line"] = intProp("1-based start line.")
		props["end_line"] = intProp("Inclusive end line.")
		props["max_bytes"] = boundedIntProp("Maximum output bytes. Defaults to 262144 and is capped at 4194304.", 1, MaxTextOutputBytes)
		required = []string{"path"}
	case ToolListDir:
		props["path"] = stringProp(PathDescription("Host directory path. Relative paths resolve from ~/AgentDock."))
		AddRuntimeProperties(props)
		props["max_depth"] = boundedIntProp("Maximum traversal depth relative to path. Defaults to 1 and is capped at 20.", 1, 20)
		props["max_entries"] = boundedIntProp("Maximum returned entries. Defaults to 200 and is capped at 5000.", 1, 5000)
		props["patterns"] = map[string]any{"type": "array", "description": "Include glob patterns relative to path. * stays within one path segment; ** crosses directories. Defaults to [\"**/*\"].", "items": map[string]any{"type": "string"}}
		props["exclude_patterns"] = map[string]any{"type": "array", "description": "Exclude glob patterns relative to path, using the same * and ** semantics as patterns.", "items": map[string]any{"type": "string"}}
		props["entry_type"] = map[string]any{"type": "string", "description": "Return any entries, files only, or directories only. Defaults to any.", "enum": []string{"any", "file", "directory"}}
		props["include_hidden"] = boolProp("Include hidden paths.")
		props["include_ignored"] = boolProp("Include normally skipped or ignored paths.")
	case ToolSearchText:
		props["path"] = stringProp(PathDescription("Host path. Relative paths resolve from ~/AgentDock."))
		AddRuntimeProperties(props)
		props["query"] = stringProp("Text or regex query.")
		props["regex"] = boolProp("Treat query as regex.")
		props["case_sensitive"] = boolProp("Use case-sensitive search.")
		props["include_hidden"] = boolProp("Include hidden files and directories.")
		props["include_ignored"] = boolProp("Include normally skipped or ignored paths.")
		props["include_globs"] = map[string]any{"type": "array", "description": "Include glob patterns relative to path. * stays within one path segment; ** crosses directories.", "items": map[string]any{"type": "string"}}
		props["glob"] = stringProp("Single include glob relative to path. * stays within one path segment; ** crosses directories.")
		props["exclude_globs"] = map[string]any{"type": "array", "description": "Exclude glob patterns relative to path, using the same * and ** semantics as include_globs.", "items": map[string]any{"type": "string"}}
		props["context_lines"] = boundedIntProp("Context lines around each match. Capped at 20.", 0, 20)
		props["max_results"] = boundedIntProp("Maximum matches. Defaults to 100 and is capped at 1000.", 1, 1000)
		required = []string{"query"}
	case ToolFileEdit:
		props["action"] = map[string]any{"type": "string", "description": "File edit action.", "enum": []string{"replace", "patch", "add", "delete", "move"}}
		toolcontract.ActivityProperties(props)
		toolcontract.TargetProperties(props)
		props["path"] = stringProp(PathDescription("Host path for replace, add, delete, or move. Relative paths resolve from ~/AgentDock."))
		AddRuntimeProperties(props)
		props["old"] = stringProp("Exact UTF-8 text to replace.")
		props["new"] = stringProp("Replacement UTF-8 text for action=replace.")
		props["replace_all"] = boolProp("Replace every match instead of only the first.")
		props["expected_matches"] = map[string]any{"type": "integer", "description": "Required number of matches. Defaults to 1; zero asserts no matches.", "minimum": 0}
		props["content"] = stringProp("Text content for action=add.")
		props["new_path"] = stringProp(PathDescription("Destination path for action=move."))
		props["overwrite"] = boolProp("Allow add or move to replace an existing destination file.")
		props["recursive"] = boolProp("Required for deleting directories.")
		props["patch"] = stringProp(PatchDescription("Patch text for action=patch."))
		props["workdir"] = stringProp(PathDescription("Patch working directory."))
		props["dry_run"] = boolProp("Preview or validate without writing.")
		props["max_diff_bytes"] = boundedIntProp("Maximum diff preview bytes. Defaults to 65536 and is capped at 4194304.", 1, MaxTextOutputBytes)
		required = []string{"action"}
	default:
		return nil, false
	}
	schema := toolcontract.InputObject(props, required...)
	if name == ToolFileEdit {
		schema["allOf"] = []any{
			map[string]any{
				"if": map[string]any{
					"properties": map[string]any{"action": map[string]any{"const": "replace"}},
					"required":   []string{"action"},
				},
				"then": map[string]any{"required": []string{"path", "old", "new"}},
			},
			map[string]any{
				"if": map[string]any{
					"properties": map[string]any{"action": map[string]any{"const": "patch"}},
					"required":   []string{"action"},
				},
				"then": map[string]any{"required": []string{"patch"}},
			},
			map[string]any{
				"if": map[string]any{
					"properties": map[string]any{"action": map[string]any{"const": "add"}},
					"required":   []string{"action"},
				},
				"then": map[string]any{"required": []string{"path", "content"}},
			},
			map[string]any{
				"if": map[string]any{
					"properties": map[string]any{"action": map[string]any{"const": "delete"}},
					"required":   []string{"action"},
				},
				"then": map[string]any{"required": []string{"path"}},
			},
			map[string]any{
				"if": map[string]any{
					"properties": map[string]any{"action": map[string]any{"const": "move"}},
					"required":   []string{"action"},
				},
				"then": map[string]any{"required": []string{"path", "new_path"}},
			},
		}
	}
	return schema, true
}

func OutputSchema(name string) (map[string]any, bool) {
	stringProp := toolcontract.String
	intProp := toolcontract.Integer
	boolProp := toolcontract.Boolean
	arrayProp := toolcontract.ObjectArray
	stringArrayProp := toolcontract.StringArray
	props := map[string]any{}
	var required []string

	switch name {
	case ToolReadFile:
		props["path"] = stringProp("Host path or skill:// resource URI. Relative Host paths resolve from ~/AgentDock.")
		props["content"] = stringProp("Text content slice.")
		props["encoding"] = stringProp("Detected text encoding.")
		props["size_bytes"] = intProp("File size in bytes.")
		props["truncated"] = boolProp("Whether output was truncated.")
		props["truncated_reason"] = stringProp("Reason output was truncated.")
		props["start_line"] = intProp("Returned start line.")
		props["end_line"] = intProp("Returned end line.")
		props["next_start_line"] = intProp("Next line to read when output was truncated.")
		props["total_lines"] = intProp("Total line count.")
	case ToolListDir:
		props["path"] = stringProp("Listed Host directory path. Relative paths resolve from ~/AgentDock.")
		props["entries"] = map[string]any{
			"type":        "array",
			"description": "Matched directory entries. Each entry path is slash-normalized and relative to the requested path.",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"name", "path", "type", "size_bytes", "modified", "is_hidden"},
				"properties": map[string]any{
					"name":       stringProp("Entry base name."),
					"path":       stringProp("Slash-normalized path relative to the requested directory."),
					"type":       map[string]any{"type": "string", "enum": []string{"file", "directory"}},
					"size_bytes": intProp("Entry size in bytes as reported by the selected runtime."),
					"modified":   stringProp("Entry modification time in RFC3339-compatible form."),
					"is_hidden":  boolProp("Whether the entry path contains a hidden component."),
				},
			},
		}
		props["truncated"] = boolProp("Whether at least one additional matching entry existed beyond max_entries.")
		props["partial"] = boolProp("Whether unreadable descendant paths were skipped while returning readable entries.")
		props["skipped_paths"] = stringArrayProp("Unreadable descendant paths skipped during traversal, relative to the requested path.")
		required = []string{"path", "entries", "truncated", "partial", "skipped_paths"}
	case ToolSearchText:
		props["matches"] = arrayProp("Text search matches.")
		props["engine"] = stringProp("Search engine used: rg or go_fallback.")
		props["truncated"] = boolProp("Whether matches were truncated.")
		props["partial"] = boolProp("Whether oversized files were skipped by the Go fallback.")
		props["files_scanned"] = intProp("Regular files read by the Go fallback.")
		props["bytes_scanned"] = intProp("File bytes read by the Go fallback.")
		props["skipped_large_files"] = intProp("Oversized files skipped by the Go fallback.")
	case ToolFileEdit:
		props["action"] = stringProp("File edit action.")
		props["summary"] = stringProp("Result summary.")
		props["path"] = stringProp("Host path. Relative paths resolve from ~/AgentDock.")
		props["new_path"] = stringProp("Move destination path.")
		props["workdir"] = stringProp("Patch working directory.")
		props["affected_files"] = map[string]any{
			"type":        "array",
			"description": "Files affected by a patch. Structured-envelope results use operation/move_to; unified-diff results use status/binary.",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"path"},
				"properties": map[string]any{
					"path":      stringProp("Affected file path."),
					"operation": map[string]any{"type": "string", "enum": []string{"add", "delete", "update", "move"}},
					"move_to":   stringProp("Move destination for operation=move."),
					"status":    stringProp("Unified diff file status."),
					"binary":    boolProp("Whether a unified diff file is binary."),
				},
			},
		}
		props["dry_run"] = boolProp("Whether this was a dry run.")
		props["matches"] = intProp("Match count for replace.")
		props["changed"] = boolProp("Whether content changed.")
		props["recursive"] = boolProp("Whether delete was allowed to remove a directory recursively.")
		props["diff_preview"] = stringProp("Diff preview.")
		props["truncated"] = boolProp("Whether the diff preview was truncated.")
		props["files_changed"] = intProp("Changed file count.")
		props["file_statistics"] = map[string]any{"type": "array", "description": "Per-file logical line statistics from the same protected transaction as the root totals. Proposed only for dry runs.", "items": map[string]any{"type": "object", "required": []string{"path", "operation", "insertions", "deletions"}, "properties": map[string]any{"path": stringProp("Original file path."), "operation": stringProp("Edit operation, including move or overwrite."), "move_to": stringProp("Move destination when present."), "insertions": intProp("Inserted logical lines."), "deletions": intProp("Deleted logical lines.")}}}
		props["unverified_files"] = stringArrayProp("Paths excluded from partial totals because their final state or ownership could not be confirmed.")
		props["cleanup_pending"] = boolProp("The edit committed successfully, but transaction cleanup needs recovery; do not repeat the edit.")
		props["transaction_id"] = stringProp("Transaction identity for retained cleanup state.")
		props["stats_state"] = map[string]any{"type": "string", "enum": []string{"known", "partial", "preview", "unknown", "unsupported"}, "description": "Known actual committed counts, proposed dry-run counts, unknown outcome, or unsupported non-text statistics."}
		props["insertions"] = intProp("Inserted logical lines across this transaction; proposed only when dry_run=true. Missing when unknown or unsupported.")
		props["deletions"] = intProp("Deleted logical lines across this transaction; CRLF/LF or final newline changes alone do not add/remove lines. Missing when unknown or unsupported.")
	default:
		return nil, false
	}
	AddRuntimeOutputProperties(props)
	return toolcontract.OutputObject(props, required...), true
}
