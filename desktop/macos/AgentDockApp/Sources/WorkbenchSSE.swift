import Foundation

struct WorkbenchStreamEvent: Equatable, Sendable {
    let id: String
    let name: String
    let data: WorkbenchJSON
}

/// Incremental, bounded Server-Sent Events parser. It intentionally does not
/// use `AsyncBytes.lines`, because an untrusted Core/proxy response could then
/// allocate an unbounded line before the desktop client gets to inspect it.
struct WorkbenchSSEParser {
    private let maximumLineBytes: Int
    private let maximumEventBytes: Int
    private var line = Data()
    private var eventName = "message"
    private var eventID = ""
    private var dataLines = [Data]()
    private var eventBytes = 0
    private var previousWasCR = false

    init(maximumLineBytes: Int, maximumEventBytes: Int) {
        self.maximumLineBytes = maximumLineBytes
        self.maximumEventBytes = maximumEventBytes
    }

    mutating func feed(_ byte: UInt8) throws -> [WorkbenchStreamEvent] {
        if byte == 0x0A && previousWasCR {
            previousWasCR = false
            return []
        }
        previousWasCR = byte == 0x0D
        if byte == 0x0A || byte == 0x0D {
            let completed = try consume(line)
            line.removeAll(keepingCapacity: true)
            return completed
        }
        guard line.count < maximumLineBytes else {
            throw WorkbenchClientError.streamLineTooLarge(limit: maximumLineBytes)
        }
        line.append(byte)
        return []
    }

    mutating func finish() throws -> [WorkbenchStreamEvent] {
        var events = [WorkbenchStreamEvent]()
        if !line.isEmpty {
            events.append(contentsOf: try consume(line))
            line.removeAll()
        }
        events.append(contentsOf: try dispatch())
        return events
    }

    private mutating func consume(_ raw: Data) throws -> [WorkbenchStreamEvent] {
        if raw.isEmpty { return try dispatch() }
        if raw.first == 0x3A { return [] }
        guard let text = String(data: raw, encoding: .utf8) else {
            throw WorkbenchClientError.invalidJSON("活动流包含无效 UTF-8。")
        }
        let pieces = text.split(separator: ":", maxSplits: 1, omittingEmptySubsequences: false)
        let field = String(pieces[0])
        var value = pieces.count == 2 ? String(pieces[1]) : ""
        if value.first == " " { value.removeFirst() }
        switch field {
        case "event":
            eventName = String(value.prefix(128))
        case "id":
            // The most recent ID applies to later events even when an event has
            // no data, matching the SSE specification's Last-Event-ID model.
            if !value.contains("\0") { eventID = String(value.prefix(256)) }
        case "data":
            guard let encoded = value.data(using: .utf8) else {
                throw WorkbenchClientError.invalidJSON("活动流 data 字段不是 UTF-8。")
            }
            eventBytes += encoded.count + (dataLines.isEmpty ? 0 : 1)
            guard eventBytes <= maximumEventBytes else {
                throw WorkbenchClientError.streamEventTooLarge(limit: maximumEventBytes)
            }
            dataLines.append(encoded)
        default:
            break
        }
        return []
    }

    private mutating func dispatch() throws -> [WorkbenchStreamEvent] {
        defer {
            eventName = "message"
            dataLines.removeAll(keepingCapacity: true)
            eventBytes = 0
        }
        guard !dataLines.isEmpty else { return [] }
        var combined = Data()
        for (index, data) in dataLines.enumerated() {
            if index > 0 { combined.append(0x0A) }
            combined.append(data)
        }
        do {
            let value = try WorkbenchJSON.decode(combined)
            return [WorkbenchStreamEvent(id: eventID, name: eventName, data: value)]
        } catch {
            throw WorkbenchClientError.invalidJSON("活动流 JSON 无法解析：\(error.localizedDescription)")
        }
    }
}
