import Foundation

/// Validate navigation before replacing the last good snapshot. Reserved UI
/// keys never enter resource routes, task binding or conversation state.
enum WorkbenchValidation {
    static func sidebar(_ json: WorkbenchJSON) throws {
        guard json.objectValue != nil, let groups = json["groups"].arrayValue,
              groups.count <= 2000 else {
            throw WorkbenchClientError.invalidResponse(L10n.text("Invalid sidebar structure; the previous list was retained."))
        }
        var ids = Set<String>()
        var workspaceIDs = Set<String>()
        var unattributedCount = 0
        var count = 0
        for group in groups {
            let workspace = group.text("workspace_id")
            guard !workspace.isEmpty, workspaceIDs.insert(workspace).inserted,
                  let rows = group["conversations"].arrayValue else {
                throw WorkbenchClientError.invalidResponse(L10n.text("Missing or duplicate workspace identities; the previous list was retained."))
            }
            for row in rows {
                count += 1
                let id = row.firstText("conversation_id", "id")
                if row.flag("is_unattributed") {
                    unattributedCount += 1
                    guard workspace == "unattributed", id.isEmpty, unattributedCount == 1 else {
                        throw WorkbenchClientError.invalidResponse(L10n.text("Invalid unattributed navigation row; the previous list was retained."))
                    }
                } else {
                    guard !id.isEmpty, id != "unattributed", !id.hasPrefix("footer:"),
                          ids.insert(id).inserted else {
                        throw WorkbenchClientError.invalidResponse(L10n.text("Missing or duplicate conversation identities; the previous list was retained."))
                    }
                }
            }
        }
        guard count <= 10000 else { throw WorkbenchClientError.responseTooLarge(limit: 10000) }
    }

    static func mutation(_ json: WorkbenchJSON) throws -> WorkbenchJSON {
        guard json.objectValue != nil else {
            throw WorkbenchClientError.invalidResponse(L10n.text("Invalid operation response. The result requires readback; writes will not be retried automatically."))
        }
        if json.optionalFlag("ok") == false || json.integer("failed") > 0 {
            throw WorkbenchClientError.invalidResponse(L10n.text("The operation did not fully succeed: ") +
                String(json.prettyPrinted.prefix(4096)))
        }
        return json
    }
}

struct WorkbenchPayloadSlice: Equatable {
    var text = ""
    var nextOffset = 0
    var hasMore = true
    var returnedScalars = 0
    var caption: String {
        L10n.format("This chunk: %@ Unicode scalars · Next byte offset: %@", String(describing: returnedScalars), String(describing: nextOffset)) +
        (hasMore ? L10n.text(" · More content remains") : L10n.text(" · End of content"))
    }
    init() {}
    init(json: WorkbenchJSON) throws {
        guard let text = json["text"].stringValue, let offset = json["next_offset"].int64Value,
              offset >= 0, text.unicodeScalars.count <= 100000 else {
            throw WorkbenchClientError.invalidResponse(L10n.text("Invalid output-chunk response."))
        }
        self.text = text
        nextOffset = Int(offset)
        hasMore = json.flag("has_more")
        returnedScalars = text.unicodeScalars.count
    }
}
