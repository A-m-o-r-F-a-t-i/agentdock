import Foundation

struct WorkbenchConnection: Equatable, Sendable {
    let baseURL: URL
    let bearerToken: String

    init(paths: AppPaths = AppPaths()) throws {
        guard let configuration = ServiceConfiguration.load(from: paths.environment) else {
            throw WorkbenchClientError.configuration(L10n.text("AgentDock Core is not configured. Complete installation or repair the configuration first."))
        }
        let host = configuration.healthHost.lowercased()
        guard ["127.0.0.1", "::1", "localhost"].contains(host) else {
            throw WorkbenchClientError.configuration(L10n.format("Workbench connects only to Core's direct loopback address. Current address: %@.", String(describing: configuration.healthHost)))
        }
        let token = configuration.authToken.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !token.isEmpty else {
            throw WorkbenchClientError.configuration(L10n.text("Core configuration is missing the local Bearer Token."))
        }
        var components = URLComponents()
        components.scheme = "http"
        components.host = host
        components.port = configuration.port
        components.path = "/"
        guard let url = components.url else {
            throw WorkbenchClientError.configuration(L10n.text("Unable to construct the Core loopback address."))
        }
        baseURL = url
        bearerToken = token
    }

    init(baseURL: URL, bearerToken: String) throws {
        guard baseURL.scheme?.lowercased() == "http",
              let host = baseURL.host?.lowercased(),
              ["127.0.0.1", "::1", "localhost"].contains(host) else {
            throw WorkbenchClientError.configuration(L10n.text("A test or runtime connection must use direct loopback HTTP."))
        }
        guard baseURL.user == nil, baseURL.password == nil, baseURL.query == nil, baseURL.fragment == nil,
              !bearerToken.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              !bearerToken.contains("\r"), !bearerToken.contains("\n") else {
            throw WorkbenchClientError.configuration(L10n.text("Bearer Token cannot be empty."))
        }
        self.baseURL = baseURL
        self.bearerToken = bearerToken
    }
}

struct WorkbenchSidebarRequest: Equatable, Sendable {
    var view: WorkbenchListView = .active
    var search = ""
    var limits: [String: Int] = [:]
    var modes: [String: String] = [:]
    var cursors: [String: String] = [:]
    var defaultMode = "auto"
    var selectedConversationID = ""

    var json: WorkbenchJSON {
        .object([
            "view": .string(view.rawValue),
            "search": .string(String(search.prefix(512))),
            "limits": .object(limits.mapValues { .integer(Int64($0)) }),
            "modes": .object(modes.mapValues(WorkbenchJSON.string)),
            "cursors": .object(cursors.mapValues(WorkbenchJSON.string)),
            "default_mode": .string(defaultMode),
            "selected_id": .string(selectedConversationID)
        ])
    }
}

final class WorkbenchAPIClient {
    static let maximumResponseBytes = 8 * 1024 * 1024
    static let maximumStreamLineBytes = 1 * 1024 * 1024
    static let maximumStreamEventBytes = 2 * 1024 * 1024

    typealias ConnectionProvider = () throws -> WorkbenchConnection

    private let session: URLSession
    private let ownsSession: Bool
    private let connectionProvider: ConnectionProvider
    private let maximumResponseBytes: Int
    private let redirectGuard = WorkbenchRedirectGuard()

    init(
        session: URLSession? = nil,
        maximumResponseBytes: Int = WorkbenchAPIClient.maximumResponseBytes,
        connectionProvider: @escaping ConnectionProvider = { try WorkbenchConnection() }
    ) {
        if let session {
            self.session = session
            ownsSession = false
        } else {
            let configuration = URLSessionConfiguration.ephemeral
            configuration.timeoutIntervalForRequest = 12
            configuration.timeoutIntervalForResource = 20
            configuration.waitsForConnectivity = false
            configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
            configuration.urlCache = nil
            configuration.httpCookieStorage = nil
            configuration.httpShouldSetCookies = false
            configuration.httpMaximumConnectionsPerHost = 4
            configuration.connectionProxyDictionary = [:]
            self.session = URLSession(configuration: configuration)
            ownsSession = true
        }
        self.maximumResponseBytes = max(1024, maximumResponseBytes)
        self.connectionProvider = connectionProvider
    }

    deinit {
        if ownsSession { session.invalidateAndCancel() }
    }

    func get(_ path: String) async throws -> WorkbenchJSON {
        try await request(method: "GET", path: path, body: nil)
    }

    func post(_ path: String, body: WorkbenchJSON) async throws -> WorkbenchJSON {
        try WorkbenchValidation.mutation(await request(method: "POST", path: path, body: body))
    }

    func eventStream(path streamPath: String, lastEventID: String = "") -> AsyncThrowingStream<WorkbenchStreamEvent, Error> {
        AsyncThrowingStream(bufferingPolicy: .bufferingOldest(256)) { continuation in
            let task = Task {
                do {
                    let connection = try connectionProvider()
                    var request = try makeRequest(connection: connection, method: "GET", path: streamPath, body: nil)
                    request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
                    request.setValue("no-cache", forHTTPHeaderField: "Cache-Control")
                    if !lastEventID.isEmpty { request.setValue(lastEventID, forHTTPHeaderField: "Last-Event-ID") }
                    let (bytes, response) = try await session.bytes(for: request, delegate: redirectGuard)
                    defer { bytes.task.cancel() }
                    guard let http = response as? HTTPURLResponse else {
                        throw WorkbenchClientError.invalidResponse(L10n.text("The Core activity stream did not return an HTTP response."))
                    }
                    guard (200...299).contains(http.statusCode) else {
                        let data = try await read(bytes: bytes, limit: maximumResponseBytes)
                        throw Self.httpError(status: http.statusCode, data: data)
                    }
                    let contentType = http.value(forHTTPHeaderField: "Content-Type")?.lowercased() ?? ""
                    guard contentType.contains("text/event-stream") else {
                        throw WorkbenchClientError.invalidResponse(L10n.format("The Core activity stream returned a non-SSE content type: %@.", String(describing: contentType.isEmpty ? L10n.text("Not provided") : contentType)))
                    }
                    var parser = WorkbenchSSEParser(
                        maximumLineBytes: Self.maximumStreamLineBytes,
                        maximumEventBytes: Self.maximumStreamEventBytes
                    )
                    for try await byte in bytes {
                        try Task.checkCancellation()
                        for event in try parser.feed(byte) {
                            if case .dropped = continuation.yield(event) {
                                throw WorkbenchClientError.transport(L10n.text("The activity-stream consumer fell behind. The connection was closed for cursor-based resynchronization."))
                            }
                        }
                    }
                    for event in try parser.finish() { continuation.yield(event) }
                    continuation.finish()
                } catch is CancellationError {
                    continuation.finish(throwing: WorkbenchClientError.cancelled)
                } catch let error as WorkbenchClientError {
                    continuation.finish(throwing: error)
                } catch {
                    continuation.finish(throwing: Self.transportError(error))
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    private func request(method: String, path: String, body: WorkbenchJSON?) async throws -> WorkbenchJSON {
        do {
            let connection = try connectionProvider()
            let request = try makeRequest(connection: connection, method: method, path: path, body: body)
            let (bytes, response) = try await session.bytes(for: request, delegate: redirectGuard)
            defer { bytes.task.cancel() }
            guard let http = response as? HTTPURLResponse else {
                throw WorkbenchClientError.invalidResponse(L10n.text("Core did not return an HTTP response."))
            }
            let expected = response.expectedContentLength
            if expected > Int64(maximumResponseBytes) {
                throw WorkbenchClientError.responseTooLarge(limit: maximumResponseBytes)
            }
            let data = try await read(bytes: bytes, limit: maximumResponseBytes)
            guard (200...299).contains(http.statusCode) else {
                throw Self.httpError(status: http.statusCode, data: data)
            }
            let contentType = http.value(forHTTPHeaderField: "Content-Type")?.lowercased() ?? ""
            guard contentType.contains("application/json") || contentType.contains("+json") else {
                throw WorkbenchClientError.invalidResponse(L10n.format("Core returned a non-JSON content type: %@.", String(describing: contentType.isEmpty ? L10n.text("Not provided") : contentType)))
            }
            do {
                return try WorkbenchJSON.decode(data)
            } catch {
                throw WorkbenchClientError.invalidJSON(L10n.format("Unable to parse Core JSON: %@", String(describing: error.localizedDescription)))
            }
        } catch is CancellationError {
            throw WorkbenchClientError.cancelled
        } catch let error as WorkbenchClientError {
            throw error
        } catch {
            throw Self.transportError(error)
        }
    }

    private func makeRequest(connection: WorkbenchConnection, method: String, path: String, body: WorkbenchJSON?) throws -> URLRequest {
        guard let url = URL(string: path, relativeTo: connection.baseURL)?.absoluteURL,
              url.scheme == connection.baseURL.scheme,
              url.host?.lowercased() == connection.baseURL.host?.lowercased(),
              url.port == connection.baseURL.port else {
            throw WorkbenchClientError.configuration(L10n.text("Access outside the loopback Core origin was rejected."))
        }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.timeoutInterval = 12
        request.setValue("Bearer \(connection.bearerToken)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("AgentDock-Workbench/macOS", forHTTPHeaderField: "User-Agent")
        if let body {
            request.httpBody = try body.encodedData()
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        return request
    }

    private func read(bytes: URLSession.AsyncBytes, limit: Int) async throws -> Data {
        var data = Data()
        data.reserveCapacity(min(limit, 64 * 1024))
        for try await byte in bytes {
            try Task.checkCancellation()
            guard data.count < limit else { throw WorkbenchClientError.responseTooLarge(limit: limit) }
            data.append(byte)
        }
        return data
    }

    private static func transportError(_ error: Error) -> WorkbenchClientError {
        if let urlError = error as? URLError {
            if urlError.code == .cancelled { return .cancelled }
            switch urlError.code {
            case .cannotConnectToHost, .networkConnectionLost, .notConnectedToInternet, .timedOut, .cannotFindHost:
                return .transport(L10n.format("Unable to connect to AgentDock Core: %@", String(describing: urlError.localizedDescription)))
            default:
                return .transport(L10n.format("Core request failed: %@", String(describing: urlError.localizedDescription)))
            }
        }
        return .transport(L10n.format("Core request failed: %@", String(describing: error.localizedDescription)))
    }

    private static func httpError(status: Int, data: Data) -> WorkbenchClientError {
        let value = try? WorkbenchJSON.decode(data)
        let nested = value?["error"] ?? .null
        let rootCode = value?.firstText("code") ?? ""
        let rootMessage = value?.firstText("message", "error_description", "error") ?? ""
        let code = rootCode.isEmpty ? nested.firstText("code") : rootCode
        let message = rootMessage.isEmpty ? nested.firstText("message", "detail") : rootMessage
        return .http(
            status: status,
            code: code,
            message: message.isEmpty ? HTTPURLResponse.localizedString(forStatusCode: status) : message
        )
    }

    func queryPath(_ base: String, query: [URLQueryItem]) -> String {
        guard !query.isEmpty else { return base }
        var components = URLComponents()
        components.path = base
        components.queryItems = query
        return components.string ?? base
    }

    func encodedPathComponent(_ value: String) throws -> String {
        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "-._~"))
        guard !value.isEmpty, value.count <= 256, value != ".", value != "..",
              !value.contains("/"), !value.contains("\\"), !value.contains("%"),
              let encoded = value.addingPercentEncoding(withAllowedCharacters: allowed) else {
            throw WorkbenchClientError.configuration(L10n.text("Invalid Runtime resource ID."))
        }
        return encoded
    }
}

/// Apply to every task, including tests using an injected session. Never follow
/// a redirect carrying local management authority, even to another loopback port.
private final class WorkbenchRedirectGuard: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}
