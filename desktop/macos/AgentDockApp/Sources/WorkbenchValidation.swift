import Foundation

/// Validate navigation before replacing the last good snapshot. Reserved UI
/// keys never enter resource routes, task binding or conversation state.
enum WorkbenchValidation {
    static func sidebar(_ json: WorkbenchJSON) throws {
        guard json.objectValue != nil, let groups = json["groups"].arrayValue,
              groups.count <= 2000 else {
            throw WorkbenchClientError.invalidResponse("侧栏结构无效；原列表已保留。")
        }
        var ids = Set<String>()
        var workspaceIDs = Set<String>()
        var unattributedCount = 0
        var count = 0
        for group in groups {
            let workspace = group.text("workspace_id")
            guard !workspace.isEmpty, workspaceIDs.insert(workspace).inserted,
                  let rows = group["conversations"].arrayValue else {
                throw WorkbenchClientError.invalidResponse("工作区身份缺失或重复；原列表已保留。")
            }
            for row in rows {
                count += 1
                let id = row.firstText("conversation_id", "id")
                if row.flag("is_unattributed") {
                    unattributedCount += 1
                    guard workspace == "unattributed", id.isEmpty, unattributedCount == 1 else {
                        throw WorkbenchClientError.invalidResponse("未归属导航项格式无效；原列表已保留。")
                    }
                } else {
                    guard !id.isEmpty, id != "unattributed", !id.hasPrefix("footer:"),
                          ids.insert(id).inserted else {
                        throw WorkbenchClientError.invalidResponse("对话身份缺失或重复；原列表已保留。")
                    }
                }
            }
        }
        guard count <= 10000 else { throw WorkbenchClientError.responseTooLarge(limit: 10000) }
    }

    static func mutation(_ json: WorkbenchJSON) throws -> WorkbenchJSON {
        guard json.objectValue != nil else {
            throw WorkbenchClientError.invalidResponse("操作响应无效；结果待回读，不会自动重试写入。")
        }
        if json.optionalFlag("ok") == false || json.integer("failed") > 0 {
            throw WorkbenchClientError.invalidResponse("操作未完全成功：" +
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
        "本段 \(returnedScalars) 个 Unicode 字符 · 下一字节偏移 \(nextOffset)" +
        (hasMore ? " · 尚有后续内容" : " · 已到尾部")
    }
    init() {}
    init(json: WorkbenchJSON) throws {
        guard let text = json["text"].stringValue, let offset = json["next_offset"].int64Value,
              offset >= 0, text.unicodeScalars.count <= 100000 else {
            throw WorkbenchClientError.invalidResponse("输出分块响应无效。")
        }
        self.text = text
        nextOffset = Int(offset)
        hasMore = json.flag("has_more")
        returnedScalars = text.unicodeScalars.count
    }
}
