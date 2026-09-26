import Foundation

enum WorkbenchClientError: LocalizedError, Equatable, Sendable {
    case configuration(String)
    case transport(String)
    case cancelled
    case responseTooLarge(limit: Int)
    case streamLineTooLarge(limit: Int)
    case streamEventTooLarge(limit: Int)
    case invalidResponse(String)
    case invalidJSON(String)
    case http(status: Int, code: String, message: String)

    var errorDescription: String? {
        switch self {
        case let .configuration(message), let .transport(message), let .invalidResponse(message), let .invalidJSON(message):
            return message
        case .cancelled:
            return L10n.text("The request was cancelled.")
        case let .responseTooLarge(limit):
            return L10n.format("The Core response exceeds the desktop limit of %@ bytes.", String(describing: limit))
        case let .streamLineTooLarge(limit):
            return L10n.format("An activity-stream line exceeds the limit of %@ bytes.", String(describing: limit))
        case let .streamEventTooLarge(limit):
            return L10n.format("An activity-stream event exceeds the limit of %@ bytes.", String(describing: limit))
        case let .http(status, code, message):
            let identity = code.isEmpty ? "HTTP \(status)" : "\(code) · HTTP \(status)"
            return "\(identity)：\(message)"
        }
    }

    var retryable: Bool {
        switch self {
        case .transport:
            return true
        case let .http(status, _, _):
            return status == 408 || status == 429 || (500...599).contains(status)
        default:
            return false
        }
    }

    var capabilityUnavailable: Bool {
        switch self {
        case let .http(status, code, _):
            return status == 404 || status == 501 || (status == 503 && code.hasSuffix("_UNAVAILABLE"))
        default:
            return false
        }
    }
}
