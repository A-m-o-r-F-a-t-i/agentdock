import Foundation

/// A tolerant JSON tree used at the desktop/Core boundary. The Runtime schema is
/// versioned by Core and may gain fields before the App is updated, so the
/// client preserves unknown values rather than decoding into a lossy DTO.
enum WorkbenchJSON: Codable, Equatable, Sendable {
    case object([String: WorkbenchJSON])
    case array([WorkbenchJSON])
    case string(String)
    case integer(Int64)
    case number(Double)
    case bool(Bool)
    case null

    init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if container.decodeNil() {
            self = .null
        } else if let value = try? container.decode(Bool.self) {
            self = .bool(value)
        } else if let value = try? container.decode(Int64.self) {
            self = .integer(value)
        } else if let value = try? container.decode(Double.self) {
            self = .number(value)
        } else if let value = try? container.decode(String.self) {
            self = .string(value)
        } else if let value = try? container.decode([String: WorkbenchJSON].self) {
            self = .object(value)
        } else if let value = try? container.decode([WorkbenchJSON].self) {
            self = .array(value)
        } else {
            throw DecodingError.dataCorruptedError(in: container, debugDescription: "Unsupported JSON value")
        }
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case let .object(value): try container.encode(value)
        case let .array(value): try container.encode(value)
        case let .string(value): try container.encode(value)
        case let .integer(value): try container.encode(value)
        case let .number(value): try container.encode(value)
        case let .bool(value): try container.encode(value)
        case .null: try container.encodeNil()
        }
    }

    static func decode(_ data: Data) throws -> WorkbenchJSON {
        try JSONDecoder().decode(WorkbenchJSON.self, from: data)
    }

    func encodedData(pretty: Bool = false) throws -> Data {
        let encoder = JSONEncoder()
        if pretty {
            encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        } else {
            encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        }
        return try encoder.encode(self)
    }

    var prettyPrinted: String {
        guard let data = try? encodedData(pretty: true) else { return "" }
        return String(data: data, encoding: .utf8) ?? ""
    }

    var objectValue: [String: WorkbenchJSON]? {
        guard case let .object(value) = self else { return nil }
        return value
    }

    var arrayValue: [WorkbenchJSON]? {
        guard case let .array(value) = self else { return nil }
        return value
    }

    var stringValue: String? {
        guard case let .string(value) = self else { return nil }
        return value
    }

    var int64Value: Int64? {
        switch self {
        case let .integer(value): return value
        case let .number(value) where value.rounded(.towardZero) == value && value <= Double(Int64.max) && value >= Double(Int64.min):
            return Int64(value)
        default: return nil
        }
    }

    var uint64Value: UInt64? {
        guard let value = int64Value, value >= 0 else { return nil }
        return UInt64(value)
    }

    var doubleValue: Double? {
        switch self {
        case let .integer(value): return Double(value)
        case let .number(value): return value
        default: return nil
        }
    }

    var boolValue: Bool? {
        guard case let .bool(value) = self else { return nil }
        return value
    }

    var isNull: Bool {
        if case .null = self { return true }
        return false
    }

    subscript(_ key: String) -> WorkbenchJSON {
        objectValue?[key] ?? .null
    }

    subscript(_ index: Int) -> WorkbenchJSON {
        guard let values = arrayValue, values.indices.contains(index) else { return .null }
        return values[index]
    }

    func text(_ key: String, fallback: String = "") -> String {
        self[key].stringValue ?? fallback
    }

    func optionalText(_ key: String) -> String? {
        self[key].stringValue
    }

    func integer(_ key: String, fallback: Int64 = 0) -> Int64 {
        self[key].int64Value ?? fallback
    }

    func unsigned(_ key: String, fallback: UInt64 = 0) -> UInt64 {
        self[key].uint64Value ?? fallback
    }

    func decimal(_ key: String, fallback: Double = 0) -> Double {
        self[key].doubleValue ?? fallback
    }

    func flag(_ key: String, fallback: Bool = false) -> Bool {
        self[key].boolValue ?? fallback
    }

    func optionalFlag(_ key: String) -> Bool? {
        self[key].boolValue
    }

    func values(_ key: String) -> [WorkbenchJSON] {
        self[key].arrayValue ?? []
    }

    func strings(_ key: String) -> [String] {
        values(key).compactMap(\.stringValue)
    }

    func date(_ key: String) -> Date? {
        self[key].dateValue
    }

    var dateValue: Date? {
        guard let raw = stringValue, !raw.isEmpty else { return nil }
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let value = fractional.date(from: raw) { return value }
        let standard = ISO8601DateFormatter()
        standard.formatOptions = [.withInternetDateTime]
        return standard.date(from: raw)
    }

    static func object(_ pairs: (String, WorkbenchJSON)...) -> WorkbenchJSON {
        .object(Dictionary(uniqueKeysWithValues: pairs))
    }

    static func stringArray(_ values: [String]) -> WorkbenchJSON {
        .array(values.map(WorkbenchJSON.string))
    }
}

extension WorkbenchJSON {
    /// Returns the first non-empty string at any of the supplied keys.
    func firstText(_ keys: String...) -> String {
        for key in keys {
            let value = text(key).trimmingCharacters(in: .whitespacesAndNewlines)
            if !value.isEmpty { return value }
        }
        return ""
    }

    /// Compatibility lookup for a value which moved under a nested object.
    func firstField(_ paths: [String]) -> WorkbenchJSON {
        for path in paths {
            var value = self
            var found = true
            for component in path.split(separator: ".").map(String.init) {
                guard let object = value.objectValue, let next = object[component] else {
                    found = false
                    break
                }
                value = next
            }
            if found, !value.isNull { return value }
        }
        return .null
    }
}
