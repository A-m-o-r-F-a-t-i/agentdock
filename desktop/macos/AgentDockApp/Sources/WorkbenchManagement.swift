import Foundation

enum WorkbenchResource: String, CaseIterable {
    case tasks, conversations, approvals, skills, plugins, mcp, workspaces, display
    var title: String {
        switch self {
        case .tasks: return "任务中心"
        case .conversations: return "完整对话历史"
        case .approvals: return "审批与历史"
        case .skills: return "Skill"
        case .plugins: return "插件"
        case .mcp: return "MCP"
        case .workspaces: return "工作区"
        case .display: return "显示设置"
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
    var pageable: Bool { self == .tasks || self == .conversations || self == .approvals }
    func identity(_ item: WorkbenchJSON) -> String {
        switch self {
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
            throw WorkbenchClientError.invalidResponse("资源列表格式无效；原页面已保留。")
        }
        guard values.count <= (resource.pageable ? 200 : 10000) else {
            throw WorkbenchClientError.responseTooLarge(limit: resource.pageable ? 200 : 10000)
        }
        var seen = Set<String>()
        for item in values {
            let id = resource.identity(item)
            if resource == .conversations && item.flag("is_unattributed") && id.isEmpty { continue }
            guard !id.isEmpty, seen.insert(id).inserted else {
                throw WorkbenchClientError.invalidResponse("资源标识缺失或重复；原页面已保留。")
            }
        }
        hasMore = json.flag("has_more")
        nextOffset = Int(json.integer("next_offset", fallback: Int64(offset + values.count)))
        guard !hasMore || nextOffset > offset else {
            throw WorkbenchClientError.invalidResponse("分页游标没有前进。")
        }
        items = values
        total = json["total"].int64Value.map(Int.init) ?? json["count"].int64Value.map(Int.init)
    }
}

extension WorkbenchAPIClient {
    func managementPage(_ resource: WorkbenchResource, offset: Int, search: String,
                        view: String, workspaceID: String = "") async throws -> WorkbenchManagementPage {
        var query = [URLQueryItem]()
        if resource.pageable {
            query += [URLQueryItem(name: "offset", value: String(offset)), URLQueryItem(name: "limit", value: "100")]
            if resource == .approvals {
                if view == "active" { query.append(URLQueryItem(name: "status", value: "pending")) }
            } else {
                query += [URLQueryItem(name: "view", value: view), URLQueryItem(name: "search", value: String(search.prefix(512)))]
                if !workspaceID.isEmpty { query.append(URLQueryItem(name: "workspace_id", value: workspaceID)) }
            }
        } else if resource == .skills { query.append(URLQueryItem(name: "summary", value: "true")) }
        let json = try await get(queryPath(resource.endpoint, query: query))
        return try WorkbenchManagementPage(json, resource: resource, offset: offset)
    }
    func resourceDetail(_ resource: WorkbenchResource, item: WorkbenchJSON) async throws -> WorkbenchJSON {
        if resource == .display || resource == .workspaces { return item }
        let id = try encodedPathComponent(resource.identity(item))
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
