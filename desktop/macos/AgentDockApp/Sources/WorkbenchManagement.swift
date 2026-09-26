import Foundation

enum WorkbenchResource: String, CaseIterable {
    case tasks, conversations, calls, approvals, skills, plugins, mcp, workspaces, display
    var title: String {
        switch self {
        case .calls: return L10n.text("Calls and child calls")
        case .tasks: return L10n.text("Task center")
        case .conversations: return L10n.text("Complete conversation history")
        case .approvals: return L10n.text("Approvals and history")
        case .skills: return "Skill"
        case .plugins: return L10n.text("Plugins")
        case .mcp: return "MCP"
        case .workspaces: return L10n.text("Workspaces")
        case .display: return L10n.text("Display settings")
        }
    }
    var endpoint: String {
        switch self {
        case .tasks: return "/internal/runtime/execution/tasks"
        case .display: return "/internal/runtime/execution/display"
        default: return "/internal/runtime/" + rawValue
        }
    }
    var arrayKey: String { self == .mcp ? "servers" : rawValue }
    var pageable: Bool { self == .tasks || self == .conversations || self == .approvals || self == .calls }
    func identity(_ item: WorkbenchJSON) -> String {
        switch self {
        case .calls: return item.text("call_id")
        case .skills: return item.firstText("skill_ref", "name", "id")
        case .tasks: return item.firstText("task_id", "id")
        case .conversations: return item.text("conversation_id")
        case .approvals: return item.firstText("approval_id", "id")
        case .workspaces: return item.firstText("workspace_id", "id")
        default: return item.firstText("name", "id")
        }
    }
}

struct WorkbenchManagementPage {
    let items: [WorkbenchJSON]
    let hasMore: Bool
    let nextOffset: Int
    let total: Int?
    init(_ json: WorkbenchJSON, resource: WorkbenchResource, offset: Int) throws {
        if resource == .display {
            items = [json]; hasMore = false; nextOffset = 0; total = 1
            return
        }
        guard let values = json[resource.arrayKey].arrayValue else {
            throw WorkbenchClientError.invalidResponse(L10n.text("Invalid resource-list format; the previous page was retained."))
        }
        guard values.count <= (resource.pageable ? 200 : 10000) else {
            throw WorkbenchClientError.responseTooLarge(limit: resource.pageable ? 200 : 10000)
        }
        var seen = Set<String>()
        for item in values {
            let id = resource.identity(item)
            if resource == .conversations && item.flag("is_unattributed") && id.isEmpty { continue }
            guard !id.isEmpty, seen.insert(id).inserted else {
                throw WorkbenchClientError.invalidResponse(L10n.text("Missing or duplicate resource identifiers; the previous page was retained."))
            }
        }
        hasMore = json.flag("has_more")
        nextOffset = Int(json.integer(resource == .calls ? "next_before" : "next_offset", fallback: Int64(offset + values.count)))
        let advances = resource == .calls ? (nextOffset > 0 && (offset == 0 || nextOffset < offset)) : nextOffset > offset
        guard !hasMore || advances else {
            throw WorkbenchClientError.invalidResponse(L10n.text("The pagination cursor did not advance."))
        }
        items = values
        total = json["total"].int64Value.map(Int.init) ?? json["count"].int64Value.map(Int.init)
    }
}

extension WorkbenchAPIClient {
    func managementPage(_ resource: WorkbenchResource, offset: Int, search: String,
                        view: String, workspaceID: String = "", parentCallID: String = "") async throws -> WorkbenchManagementPage {
        var query = [URLQueryItem]()
        if resource == .calls {
            query += [URLQueryItem(name: "limit", value: "100"), URLQueryItem(name: "view", value: view),
                      URLQueryItem(name: "search", value: String(search.prefix(512)))]
            if offset > 0 { query.append(URLQueryItem(name: "before", value: String(offset))) }
            if !parentCallID.isEmpty { query.append(URLQueryItem(name: "parent_call_id", value: parentCallID)) }
        } else if resource.pageable {
            query += [URLQueryItem(name: "offset", value: String(offset)), URLQueryItem(name: "limit", value: "100")]
            if resource == .approvals {
                if view == "active" { query.append(URLQueryItem(name: "status", value: "pending")) }
            } else {
                query += [URLQueryItem(name: "view", value: view), URLQueryItem(name: "search", value: String(search.prefix(512)))]
                if !workspaceID.isEmpty { query.append(URLQueryItem(name: "workspace_id", value: workspaceID)) }
            }
        } else if resource == .skills { query.append(URLQueryItem(name: "summary", value: "true")) }
        var json = try await get(queryPath(resource.endpoint, query: query))
        if !resource.pageable, resource != .display, !search.isEmpty, var fields = json.objectValue {
            fields[resource.arrayKey] = .array(json.values(resource.arrayKey).filter {
                $0.firstText("title", "name", "description").localizedCaseInsensitiveContains(search)
                    || resource.identity($0).localizedCaseInsensitiveContains(search)
            })
            json = .object(fields)
        }
        return try WorkbenchManagementPage(json, resource: resource, offset: offset)
    }
    func resourceDetail(_ resource: WorkbenchResource, item: WorkbenchJSON) async throws -> WorkbenchJSON {
        if resource == .display || resource == .workspaces { return item }
        let id = try encodedPathComponent(resource == .skills ? item.text("name") : resource.identity(item))
        switch resource {
        case .display, .workspaces: return item
        case .tasks: return try await task(resource.identity(item))
        case .skills:
            var query = [URLQueryItem]()
            if let ref = item.optionalText("skill_ref") { query.append(URLQueryItem(name: "skill_ref", value: ref)) }
            return try await get(queryPath(resource.endpoint + "/" + id, query: query))
        default: return try await get(resource.endpoint + "/" + id)
        }
    }
}
